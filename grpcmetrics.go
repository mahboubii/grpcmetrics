//nolint:contextcheck
package grpcmetrics

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric/global"
	"go.opentelemetry.io/otel/metric/instrument"
	semconv "go.opentelemetry.io/otel/semconv/v1.17.0"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/stats"
	"google.golang.org/grpc/status"
)

const (
	// DefaultInstrumentationName is the default used when creating meters.
	DefaultInstrumentationName = "github.com/mahboubii/grpcmetrics"
)

// rpcInfo is data used for recording metrics about the rpc attempt client side, and the overall rpc server side.
type rpcInfo struct {
	fullMethodName string

	// access these counts atomically for hedging in the future
	// number of messages sent from side (client || server)
	sentMsgs int64
	// number of bytes sent (within each message) from side (client || server)
	sentBytes int64
	// number of messages received on side (client || server)
	recvMsgs int64
	// number of bytes received (within each message) received on side (client || server)
	recvBytes int64
}

type rpcInfoKey struct{}

func setRPCInfo(ctx context.Context, ri *rpcInfo) context.Context {
	return context.WithValue(ctx, rpcInfoKey{}, ri)
}

// getRPCInfo returns the rpcInfo stored in the context, or nil if there isn't one.
func getRPCInfo(ctx context.Context) *rpcInfo {
	ri, ok := ctx.Value(rpcInfoKey{}).(*rpcInfo)
	if !ok {
		return nil
	}

	return ri
}

var grpcStatusOK = status.New(codes.OK, "OK")

func getRPCStatus(err error) *status.Status {
	if err == nil {
		return grpcStatusOK
	}

	s, ok := status.FromError(err)
	if ok {
		return s
	}

	return status.New(codes.Internal, err.Error())
}

func getAttributes(fullMethodName string, err error) []attribute.KeyValue {
	rpcStatus := getRPCStatus(err)

	// https://opentelemetry.io/docs/reference/specification/metrics/semantic_conventions/rpc-metrics/
	attr := make([]attribute.KeyValue, 0, 5) //nolint:gomnd
	attr = append(attr, semconv.RPCSystemGRPC)
	attr = append(attr, semconv.RPCGRPCStatusCodeKey.Int(int(rpcStatus.Code())))
	attr = append(attr, attribute.Key("rpc.grpc.status").String(rpcStatus.Code().String()))

	parts := strings.Split(fullMethodName, "/")
	if len(parts) == 3 { //nolint:gomnd
		attr = append(attr, semconv.RPCServiceKey.String(parts[1]))
		attr = append(attr, semconv.RPCMethodKey.String(parts[2]))
	}

	return attr
}

// Handler implements https://pkg.go.dev/google.golang.org/grpc/stats#Handler
type Handler struct {
	isClient bool

	rpcDuration     instrument.Float64Histogram
	rpcRequestSize  instrument.Int64Histogram
	rpcResponseSize instrument.Int64Histogram

	// RFC suggests using histogram for counts mostly for Streams
	// It lead to high cardinality of lables so we are using counter.
	rpcRequestsPerRPC  instrument.Int64Counter
	rpcResponsesPerRPC instrument.Int64Counter
}

func newHandler(isClient bool, options []Option) (*Handler, error) {
	c := config{}

	for _, o := range options {
		o.apply(&c)
	}

	if c.meterProvider == nil {
		c.meterProvider = global.MeterProvider()
	}

	if c.instrumentationName == "" {
		c.instrumentationName = DefaultInstrumentationName
	}

	// metrics from https://opentelemetry.io/docs/reference/specification/metrics/semantic_conventions/rpc-metrics/
	meter := c.meterProvider.Meter(c.instrumentationName)

	var err error

	h := &Handler{isClient: isClient}

	prefix := "rpc.server"
	if h.isClient {
		prefix = "rpc.client"
	}

	h.rpcRequestsPerRPC, err = meter.Int64Counter(prefix+".requests_per_rpc", instrument.WithUnit("1"))
	if err != nil {
		return nil, err
	}

	h.rpcResponsesPerRPC, err = meter.Int64Counter(prefix+".responses_per_rpc", instrument.WithUnit("1"))
	if err != nil {
		return nil, err
	}

	if c.instrumentLatency {
		h.rpcDuration, err = meter.Float64Histogram(prefix+".duration", instrument.WithUnit("ms"))
		if err != nil {
			return nil, err
		}
	}

	if c.instrumentSizes {
		h.rpcRequestSize, err = meter.Int64Histogram(prefix+".request.size", instrument.WithUnit("By"))
		if err != nil {
			return nil, err
		}

		h.rpcResponseSize, err = meter.Int64Histogram(prefix+".response.size", instrument.WithUnit("By"))
		if err != nil {
			return nil, err
		}
	}

	return h, nil
}

func NewServerHandler(options ...Option) (stats.Handler, error) {
	return newHandler(false, options)
}

func NewClientHandler(options ...Option) (stats.Handler, error) {
	return newHandler(true, options)
}

// TagConn exists to satisfy gRPC stats.Handler interface.
func (h *Handler) TagConn(ctx context.Context, _ *stats.ConnTagInfo) context.Context { return ctx }

// HandleConn exists to satisfy gRPC stats.Handler interface.
func (h *Handler) HandleConn(_ context.Context, _ stats.ConnStats) {}

func (h *Handler) TagRPC(ctx context.Context, info *stats.RPCTagInfo) context.Context {
	return setRPCInfo(ctx, &rpcInfo{fullMethodName: info.FullMethodName})
}

// HandleRPC implements per-RPC stats instrumentation.
func (h *Handler) HandleRPC(ctx context.Context, rs stats.RPCStats) {
	// this should never be null, but we always check, just to be sure.
	ri := getRPCInfo(ctx)
	if ri == nil {
		return
	}

	switch rs := rs.(type) {
	case *stats.InHeader, *stats.OutHeader, *stats.InTrailer, *stats.OutTrailer:
		// Headers and Trailers are not relevant to the measures
	case *stats.Begin:
		// Potentially measure total number of client RPCs ever opened, including those that have not completed.
	case *stats.InPayload:
		atomic.AddInt64(&ri.recvMsgs, 1)

		if h.rpcRequestSize != nil {
			atomic.AddInt64(&ri.recvBytes, int64(rs.Length))
		}
	case *stats.OutPayload:
		atomic.AddInt64(&ri.sentMsgs, 1)

		if h.rpcResponseSize != nil {
			atomic.AddInt64(&ri.sentBytes, int64(rs.Length))
		}
	case *stats.End:
		// use a new context since original ctx could be canceled during this state.
		subCtx := context.Background()

		attrs := getAttributes(ri.fullMethodName, rs.Error)

		if h.isClient {
			// gRPC stats handler treats client stats exactly similar to server stats while technically name should be reversed.
			h.rpcRequestsPerRPC.Add(subCtx, atomic.LoadInt64(&ri.sentMsgs), attrs...)
			h.rpcResponsesPerRPC.Add(subCtx, atomic.LoadInt64(&ri.recvMsgs), attrs...)
		} else {
			h.rpcRequestsPerRPC.Add(subCtx, atomic.LoadInt64(&ri.recvMsgs), attrs...)
			h.rpcResponsesPerRPC.Add(subCtx, atomic.LoadInt64(&ri.sentMsgs), attrs...)
		}

		if h.rpcDuration != nil {
			h.rpcDuration.Record(subCtx, float64(time.Since(rs.BeginTime).Milliseconds()), attrs...)
		}

		if h.rpcRequestSize != nil {
			if h.isClient {
				h.rpcRequestSize.Record(subCtx, atomic.LoadInt64(&ri.sentBytes), attrs...)
			} else {
				h.rpcRequestSize.Record(subCtx, atomic.LoadInt64(&ri.recvBytes), attrs...)
			}
		}

		if h.rpcResponseSize != nil {
			if h.isClient {
				h.rpcResponseSize.Record(subCtx, atomic.LoadInt64(&ri.recvBytes), attrs...)
			} else {
				h.rpcResponseSize.Record(subCtx, atomic.LoadInt64(&ri.sentBytes), attrs...)
			}
		}

	default:
		otel.Handle(fmt.Errorf("received unhandled stats with type (%T) and data: %v", rs, rs))
	}
}
