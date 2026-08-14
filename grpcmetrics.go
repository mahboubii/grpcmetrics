// Package grpcmetrics is an OpenTelemetry metrics instrumentation for gRPC
// clients and servers, implemented as a [stats.Handler].
//
// Metric names, units, attributes, and defaults are kept compatible with
// earlier releases of this module. Duration and message-size histograms stay
// opt-in via [WithInstrumentLatency] and [WithInstrumentSizes].
package grpcmetrics

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/stats"
	"google.golang.org/grpc/status"
)

const (
	// DefaultInstrumentationName is the default used when creating meters.
	DefaultInstrumentationName = "github.com/mahboubii/grpcmetrics"

	rpcSystemGRPC         = "grpc"
	attrRPCSystem         = attribute.Key("rpc.system")
	attrRPCGRPCStatusCode = attribute.Key("rpc.grpc.status_code")
	attrRPCGRPCStatus     = attribute.Key("rpc.grpc.status")
	attrRPCService        = attribute.Key("rpc.service")
	attrRPCMethod         = attribute.Key("rpc.method")
)

// Ensure Handler continues to satisfy stats.Handler as gRPC evolves.
var _ stats.Handler = (*Handler)(nil)

// rpcInfo is data used for recording metrics about the rpc attempt client side, and the overall rpc server side.
type rpcInfo struct {
	fullMethodName string

	// access these counts atomically for hedging in the future
	// number of messages sent from side (client || server)
	sentMsgs atomic.Int64
	// number of bytes sent (within each message) from side (client || server)
	sentBytes atomic.Int64
	// number of messages received on side (client || server)
	recvMsgs atomic.Int64
	// number of bytes received (within each message) received on side (client || server)
	recvBytes atomic.Int64
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

func getAttributes(fullMethodName string, err error) attribute.Set {
	rpcStatus := getRPCStatus(err)

	// Attribute keys match the historical RPC metric conventions used by this
	// module so existing dashboards and alerts keep working.
	attr := make([]attribute.KeyValue, 0, 5)
	attr = append(attr, attrRPCSystem.String(rpcSystemGRPC))
	attr = append(attr, attrRPCGRPCStatusCode.Int(int(rpcStatus.Code())))
	attr = append(attr, attrRPCGRPCStatus.String(rpcStatus.Code().String()))

	parts := strings.Split(fullMethodName, "/")
	if len(parts) == 3 {
		attr = append(attr, attrRPCService.String(parts[1]))
		attr = append(attr, attrRPCMethod.String(parts[2]))
	}

	return attribute.NewSet(attr...)
}

func metricPrefix(isClient bool) string {
	if isClient {
		return "rpc.client"
	}

	return "rpc.server"
}

// Handler implements https://pkg.go.dev/google.golang.org/grpc/stats#Handler
type Handler struct {
	isClient bool

	rpcDuration     metric.Float64Histogram
	rpcRequestSize  metric.Int64Histogram
	rpcResponseSize metric.Int64Histogram

	// RFC suggests using histogram for counts mostly for Streams.
	// That leads to high cardinality of labels so we are using a counter.
	rpcRequestsPerRPC  metric.Int64Counter
	rpcResponsesPerRPC metric.Int64Counter
}

func newHandler(isClient bool, options []Option) (*Handler, error) {
	c := config{}

	for _, o := range options {
		o.apply(&c)
	}

	if c.meterProvider == nil {
		c.meterProvider = otel.GetMeterProvider()
	}

	if c.instrumentationName == "" {
		c.instrumentationName = DefaultInstrumentationName
	}

	// metrics from https://opentelemetry.io/docs/specs/semconv/rpc/rpc-metrics/
	meter := c.meterProvider.Meter(c.instrumentationName)

	h := &Handler{isClient: isClient}
	prefix := metricPrefix(h.isClient)

	var err error

	h.rpcRequestsPerRPC, err = meter.Int64Counter(
		prefix+".requests_per_rpc",
		metric.WithUnit("1"),
		metric.WithDescription("Number of messages received per RPC (streaming RPCs may be more than one)."),
	)
	if err != nil {
		return nil, fmt.Errorf("grpcmetrics: create %s.requests_per_rpc: %w", prefix, err)
	}

	h.rpcResponsesPerRPC, err = meter.Int64Counter(
		prefix+".responses_per_rpc",
		metric.WithUnit("1"),
		metric.WithDescription("Number of messages sent per RPC (streaming RPCs may be more than one)."),
	)
	if err != nil {
		return nil, fmt.Errorf("grpcmetrics: create %s.responses_per_rpc: %w", prefix, err)
	}

	if c.instrumentLatency {
		h.rpcDuration, err = meter.Float64Histogram(
			prefix+".duration",
			metric.WithUnit("ms"),
			metric.WithDescription("Elapsed time of the RPC in milliseconds."),
		)
		if err != nil {
			return nil, fmt.Errorf("grpcmetrics: create %s.duration: %w", prefix, err)
		}
	}

	if c.instrumentSizes {
		h.rpcRequestSize, err = meter.Int64Histogram(
			prefix+".request.size",
			metric.WithUnit("By"),
			metric.WithDescription("Uncompressed request message size in bytes."),
		)
		if err != nil {
			return nil, fmt.Errorf("grpcmetrics: create %s.request.size: %w", prefix, err)
		}

		h.rpcResponseSize, err = meter.Int64Histogram(
			prefix+".response.size",
			metric.WithUnit("By"),
			metric.WithDescription("Uncompressed response message size in bytes."),
		)
		if err != nil {
			return nil, fmt.Errorf("grpcmetrics: create %s.response.size: %w", prefix, err)
		}
	}

	return h, nil
}

// NewServerHandler creates a stats.Handler that records server-side RPC metrics.
func NewServerHandler(options ...Option) (stats.Handler, error) {
	return newHandler(false, options)
}

// NewClientHandler creates a stats.Handler that records client-side RPC metrics.
func NewClientHandler(options ...Option) (stats.Handler, error) {
	return newHandler(true, options)
}

// TagConn exists to satisfy gRPC stats.Handler interface.
func (h *Handler) TagConn(ctx context.Context, _ *stats.ConnTagInfo) context.Context { return ctx }

// HandleConn exists to satisfy gRPC stats.Handler interface.
func (h *Handler) HandleConn(_ context.Context, _ stats.ConnStats) {}

// TagRPC attaches per-RPC bookkeeping used by HandleRPC.
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
	case *stats.DelayedPickComplete:
		// Client-side picker delay; not part of the recorded measures.
	case *stats.InPayload:
		ri.recvMsgs.Add(1)

		if h.rpcRequestSize != nil {
			ri.recvBytes.Add(int64(rs.Length))
		}
	case *stats.OutPayload:
		ri.sentMsgs.Add(1)

		if h.rpcResponseSize != nil {
			ri.sentBytes.Add(int64(rs.Length))
		}
	case *stats.End:
		h.recordEnd(ctx, ri, rs)
	default:
		otel.Handle(fmt.Errorf("received unhandled stats with type (%T) and data: %v", rs, rs))
	}
}

func (h *Handler) recordEnd(ctx context.Context, ri *rpcInfo, rs *stats.End) {
	// Detach from cancel/deadline: the RPC context is often already done at End.
	subCtx := context.WithoutCancel(ctx)

	attrs := metric.WithAttributeSet(getAttributes(ri.fullMethodName, rs.Error))

	reqMsgs, respMsgs := ri.recvMsgs.Load(), ri.sentMsgs.Load()
	reqBytes, respBytes := ri.recvBytes.Load(), ri.sentBytes.Load()

	if h.isClient {
		// gRPC stats handler treats client stats exactly similar to server stats while technically name should be reversed.
		reqMsgs, respMsgs = ri.sentMsgs.Load(), ri.recvMsgs.Load()
		reqBytes, respBytes = ri.sentBytes.Load(), ri.recvBytes.Load()
	}

	h.rpcRequestsPerRPC.Add(subCtx, reqMsgs, attrs)
	h.rpcResponsesPerRPC.Add(subCtx, respMsgs, attrs)

	if h.rpcDuration != nil {
		h.rpcDuration.Record(subCtx, float64(time.Since(rs.BeginTime).Milliseconds()), attrs)
	}

	if h.rpcRequestSize != nil {
		h.rpcRequestSize.Record(subCtx, reqBytes, attrs)
	}

	if h.rpcResponseSize != nil {
		h.rpcResponseSize.Record(subCtx, respBytes, attrs)
	}
}
