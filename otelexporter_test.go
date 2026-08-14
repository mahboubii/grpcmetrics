package grpcmetrics

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/stats"
)

// exporter is a test metric.Exporter that stores the last export for assertions.
type exporter struct {
	data atomic.Value
}

func (e *exporter) Temporality(k sdkmetric.InstrumentKind) metricdata.Temporality {
	return sdkmetric.DefaultTemporalitySelector(k)
}

func (e *exporter) Aggregation(k sdkmetric.InstrumentKind) sdkmetric.Aggregation {
	return sdkmetric.DefaultAggregationSelector(k)
}

func (e *exporter) Export(ctx context.Context, data *metricdata.ResourceMetrics) error {
	copied := *data
	e.data.Store(copied)

	return ctx.Err()
}

func (e *exporter) Read() metricdata.ResourceMetrics {
	d, ok := e.data.Load().(metricdata.ResourceMetrics)
	if !ok {
		return metricdata.ResourceMetrics{}
	}

	return d
}

func (e *exporter) ForceFlush(ctx context.Context) error {
	return ctx.Err()
}

func (e *exporter) Shutdown(ctx context.Context) error {
	return ctx.Err()
}

func TestExporterStoresLastExport(t *testing.T) {
	exp := &exporter{}
	require.Equal(t, sdkmetric.DefaultTemporalitySelector(sdkmetric.InstrumentKindCounter), exp.Temporality(sdkmetric.InstrumentKindCounter))
	require.Equal(t, sdkmetric.DefaultAggregationSelector(sdkmetric.InstrumentKindHistogram), exp.Aggregation(sdkmetric.InstrumentKindHistogram))

	want := metricdata.ResourceMetrics{
		ScopeMetrics: []metricdata.ScopeMetrics{{
			Metrics: []metricdata.Metrics{{Name: "rpc.server.requests_per_rpc"}},
		}},
	}
	require.NoError(t, exp.Export(context.Background(), &want))
	got := exp.Read()
	require.Len(t, got.ScopeMetrics, 1)
	assert.Equal(t, "rpc.server.requests_per_rpc", got.ScopeMetrics[0].Metrics[0].Name)

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	require.Error(t, exp.ForceFlush(canceled))
	require.Error(t, exp.Shutdown(canceled))
}

func TestPushExporterCollectsUnaryMetrics(t *testing.T) {
	exp := &exporter{}
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp)))
	t.Cleanup(func() {
		require.NoError(t, mp.Shutdown(context.Background()))
	})

	h, err := NewServerHandler(WithMeterProvider(mp))
	require.NoError(t, err)

	handler, ok := h.(*Handler)
	require.True(t, ok)

	ctx := handler.TagRPC(context.Background(), &stats.RPCTagInfo{FullMethodName: "/testserver.TestsService/Ok"})
	handler.HandleRPC(ctx, &stats.InPayload{Length: 4})
	handler.HandleRPC(ctx, &stats.OutPayload{Length: 4})
	handler.HandleRPC(ctx, &stats.End{})

	require.NoError(t, mp.ForceFlush(context.Background()))

	rm := exp.Read()
	require.NotEmpty(t, rm.ScopeMetrics)

	attrs := []attribute.KeyValue{
		{Key: "rpc.grpc.status", Value: attribute.StringValue("OK")},
		{Key: "rpc.grpc.status_code", Value: attribute.IntValue(int(codes.OK))},
		{Key: "rpc.method", Value: attribute.StringValue("Ok")},
		{Key: "rpc.service", Value: attribute.StringValue("testserver.TestsService")},
		{Key: "rpc.system", Value: attribute.StringValue("grpc")},
	}
	assertMetric(t, rm.ScopeMetrics, attrs, metricdata.Metrics{Name: "rpc.server.requests_per_rpc", Unit: "1", Data: metricdata.Sum[int64]{
		IsMonotonic: true,
		DataPoints:  []metricdata.DataPoint[int64]{{Value: 1}},
	}})
	assertMetric(t, rm.ScopeMetrics, attrs, metricdata.Metrics{Name: "rpc.server.responses_per_rpc", Unit: "1", Data: metricdata.Sum[int64]{
		IsMonotonic: true,
		DataPoints:  []metricdata.DataPoint[int64]{{Value: 1}},
	}})
}
