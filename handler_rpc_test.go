package grpcmetrics

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/stats"
	"google.golang.org/grpc/status"
)

func TestTagConnAndHandleConn(t *testing.T) {
	h, err := newHandler(false, nil)
	require.NoError(t, err)

	ctx := context.Background()
	assert.Equal(t, ctx, h.TagConn(ctx, &stats.ConnTagInfo{}))
	assert.NotPanics(t, func() {
		h.HandleConn(ctx, &stats.ConnBegin{})
		h.HandleConn(ctx, &stats.ConnEnd{})
	})
}

func TestHandleRPCIgnoresWrongContextValue(t *testing.T) {
	h, err := newHandler(false, nil)
	require.NoError(t, err)

	ctx := context.WithValue(context.Background(), rpcInfoKey{}, "not-rpc-info")
	assert.NotPanics(t, func() {
		h.HandleRPC(ctx, &stats.InPayload{Length: 9})
		h.HandleRPC(ctx, &stats.End{})
	})
}

func TestHandleRPCKnownStatsAreNoopsUntilEnd(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { require.NoError(t, mp.Shutdown(context.Background())) })

	h, err := newHandler(false, []Option{WithMeterProvider(mp), WithInstrumentLatency(true), WithInstrumentSizes(true)})
	require.NoError(t, err)

	ctx := h.TagRPC(context.Background(), &stats.RPCTagInfo{FullMethodName: "/svc.API/Call"})
	h.HandleRPC(ctx, &stats.Begin{BeginTime: time.Now()})
	h.HandleRPC(ctx, &stats.InHeader{})
	h.HandleRPC(ctx, &stats.OutHeader{})
	h.HandleRPC(ctx, &stats.InTrailer{})
	h.HandleRPC(ctx, &stats.OutTrailer{})
	h.HandleRPC(ctx, &stats.DelayedPickComplete{})

	rm := collect(t, reader)
	assert.Empty(t, rm.ScopeMetrics)
}

func TestHandleRPCServerCountsRecvAsRequests(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { require.NoError(t, mp.Shutdown(context.Background())) })

	h, err := newHandler(false, []Option{WithMeterProvider(mp), WithInstrumentSizes(true), WithInstrumentLatency(true)})
	require.NoError(t, err)

	begin := time.Now().Add(-8 * time.Millisecond)
	ctx := h.TagRPC(context.Background(), &stats.RPCTagInfo{FullMethodName: "/svc.API/Call"})
	h.HandleRPC(ctx, &stats.InPayload{Length: 10})
	h.HandleRPC(ctx, &stats.InPayload{Length: 15})
	h.HandleRPC(ctx, &stats.OutPayload{Length: 7})
	h.HandleRPC(ctx, &stats.End{BeginTime: begin, Error: nil})

	rm := collect(t, reader)
	attrs := rpcTestAttrs("OK", codes.OK, "Call", "svc.API")

	assertMetric(t, rm.ScopeMetrics, attrs, metricdata.Metrics{
		Name: "rpc.server.requests_per_rpc", Unit: "1",
		Data: metricdata.Sum[int64]{IsMonotonic: true, DataPoints: []metricdata.DataPoint[int64]{{Value: 2}}},
	})
	assertMetric(t, rm.ScopeMetrics, attrs, metricdata.Metrics{
		Name: "rpc.server.responses_per_rpc", Unit: "1",
		Data: metricdata.Sum[int64]{IsMonotonic: true, DataPoints: []metricdata.DataPoint[int64]{{Value: 1}}},
	})
	assertMetric(t, rm.ScopeMetrics, attrs, metricdata.Metrics{
		Name: "rpc.server.request.size", Unit: "By",
		Data: metricdata.Histogram[int64]{DataPoints: []metricdata.HistogramDataPoint[int64]{{Count: 1, Sum: 25}}},
	})
	assertMetric(t, rm.ScopeMetrics, attrs, metricdata.Metrics{
		Name: "rpc.server.response.size", Unit: "By",
		Data: metricdata.Histogram[int64]{DataPoints: []metricdata.HistogramDataPoint[int64]{{Count: 1, Sum: 7}}},
	})

	duration := findMetric(t, rm.ScopeMetrics, "rpc.server.duration")
	assert.Equal(t, "ms", duration.Unit)
	assert.NotEmpty(t, duration.Description)
	hist, ok := duration.Data.(metricdata.Histogram[float64])
	require.True(t, ok)
	require.Len(t, hist.DataPoints, 1)
	assert.Equal(t, uint64(1), hist.DataPoints[0].Count)
	assert.GreaterOrEqual(t, hist.DataPoints[0].Sum, 0.0)
}

func TestHandleRPCClientSwapsRequestAndResponse(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { require.NoError(t, mp.Shutdown(context.Background())) })

	h, err := newHandler(true, []Option{WithMeterProvider(mp), WithInstrumentSizes(true)})
	require.NoError(t, err)

	ctx := h.TagRPC(context.Background(), &stats.RPCTagInfo{FullMethodName: "/svc.API/Call"})
	h.HandleRPC(ctx, &stats.OutPayload{Length: 11})
	h.HandleRPC(ctx, &stats.OutPayload{Length: 9})
	h.HandleRPC(ctx, &stats.InPayload{Length: 3})
	h.HandleRPC(ctx, &stats.End{})

	rm := collect(t, reader)
	attrs := rpcTestAttrs("OK", codes.OK, "Call", "svc.API")

	assertMetric(t, rm.ScopeMetrics, attrs, metricdata.Metrics{
		Name: "rpc.client.requests_per_rpc", Unit: "1",
		Data: metricdata.Sum[int64]{IsMonotonic: true, DataPoints: []metricdata.DataPoint[int64]{{Value: 2}}},
	})
	assertMetric(t, rm.ScopeMetrics, attrs, metricdata.Metrics{
		Name: "rpc.client.responses_per_rpc", Unit: "1",
		Data: metricdata.Sum[int64]{IsMonotonic: true, DataPoints: []metricdata.DataPoint[int64]{{Value: 1}}},
	})
	assertMetric(t, rm.ScopeMetrics, attrs, metricdata.Metrics{
		Name: "rpc.client.request.size", Unit: "By",
		Data: metricdata.Histogram[int64]{DataPoints: []metricdata.HistogramDataPoint[int64]{{Count: 1, Sum: 20}}},
	})
	assertMetric(t, rm.ScopeMetrics, attrs, metricdata.Metrics{
		Name: "rpc.client.response.size", Unit: "By",
		Data: metricdata.Histogram[int64]{DataPoints: []metricdata.HistogramDataPoint[int64]{{Count: 1, Sum: 3}}},
	})
	assertNoMetric(t, rm.ScopeMetrics, "rpc.client.duration")
}

func TestHandleRPCDoesNotCountBytesWhenSizesDisabled(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { require.NoError(t, mp.Shutdown(context.Background())) })

	h, err := newHandler(false, []Option{WithMeterProvider(mp)})
	require.NoError(t, err)

	ctx := h.TagRPC(context.Background(), &stats.RPCTagInfo{FullMethodName: "/svc.API/Call"})
	h.HandleRPC(ctx, &stats.InPayload{Length: 100})
	h.HandleRPC(ctx, &stats.OutPayload{Length: 50})
	h.HandleRPC(ctx, &stats.End{})

	rm := collect(t, reader)
	attrs := rpcTestAttrs("OK", codes.OK, "Call", "svc.API")
	assertMetric(t, rm.ScopeMetrics, attrs, metricdata.Metrics{
		Name: "rpc.server.requests_per_rpc", Unit: "1",
		Data: metricdata.Sum[int64]{IsMonotonic: true, DataPoints: []metricdata.DataPoint[int64]{{Value: 1}}},
	})
	assertNoMetric(t, rm.ScopeMetrics, "rpc.server.request.size")
	assertNoMetric(t, rm.ScopeMetrics, "rpc.server.response.size")
	assertNoMetric(t, rm.ScopeMetrics, "rpc.server.duration")
}

func TestHandleRPCCanceledContextStillRecords(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { require.NoError(t, mp.Shutdown(context.Background())) })

	h, err := newHandler(false, []Option{WithMeterProvider(mp)})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	ctx = h.TagRPC(ctx, &stats.RPCTagInfo{FullMethodName: "/svc.API/Call"})
	h.HandleRPC(ctx, &stats.InPayload{Length: 1})
	h.HandleRPC(ctx, &stats.OutPayload{Length: 1})
	cancel()
	h.HandleRPC(ctx, &stats.End{Error: status.Error(codes.Canceled, "done")})

	rm := collect(t, reader)
	attrs := rpcTestAttrs("Canceled", codes.Canceled, "Call", "svc.API")
	assertMetric(t, rm.ScopeMetrics, attrs, metricdata.Metrics{
		Name: "rpc.server.requests_per_rpc", Unit: "1",
		Data: metricdata.Sum[int64]{IsMonotonic: true, DataPoints: []metricdata.DataPoint[int64]{{Value: 1}}},
	})
}

func TestHandleRPCErrorStatusAttributes(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { require.NoError(t, mp.Shutdown(context.Background())) })

	h, err := newHandler(true, []Option{WithMeterProvider(mp)})
	require.NoError(t, err)

	ctx := h.TagRPC(context.Background(), &stats.RPCTagInfo{FullMethodName: "/svc.API/Call"})
	h.HandleRPC(ctx, &stats.OutPayload{Length: 2})
	h.HandleRPC(ctx, &stats.End{Error: status.Error(codes.Unavailable, "down")})

	rm := collect(t, reader)
	attrs := rpcTestAttrs("Unavailable", codes.Unavailable, "Call", "svc.API")
	assertMetric(t, rm.ScopeMetrics, attrs, metricdata.Metrics{
		Name: "rpc.client.requests_per_rpc", Unit: "1",
		Data: metricdata.Sum[int64]{IsMonotonic: true, DataPoints: []metricdata.DataPoint[int64]{{Value: 1}}},
	})
	assertMetric(t, rm.ScopeMetrics, attrs, metricdata.Metrics{
		Name: "rpc.client.responses_per_rpc", Unit: "1",
		Data: metricdata.Sum[int64]{IsMonotonic: true, DataPoints: []metricdata.DataPoint[int64]{{Value: 0}}},
	})
}

func TestInstrumentDescriptions(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { require.NoError(t, mp.Shutdown(context.Background())) })

	h, err := newHandler(false, []Option{WithMeterProvider(mp), WithInstrumentLatency(true), WithInstrumentSizes(true)})
	require.NoError(t, err)

	ctx := h.TagRPC(context.Background(), &stats.RPCTagInfo{FullMethodName: "/svc.API/Call"})
	h.HandleRPC(ctx, &stats.InPayload{Length: 1})
	h.HandleRPC(ctx, &stats.OutPayload{Length: 1})
	h.HandleRPC(ctx, &stats.End{BeginTime: time.Now()})

	rm := collect(t, reader)
	assert.NotEmpty(t, findMetric(t, rm.ScopeMetrics, "rpc.server.requests_per_rpc").Description)
	assert.NotEmpty(t, findMetric(t, rm.ScopeMetrics, "rpc.server.responses_per_rpc").Description)
	assert.NotEmpty(t, findMetric(t, rm.ScopeMetrics, "rpc.server.duration").Description)
	assert.NotEmpty(t, findMetric(t, rm.ScopeMetrics, "rpc.server.request.size").Description)
	assert.NotEmpty(t, findMetric(t, rm.ScopeMetrics, "rpc.server.response.size").Description)
}
