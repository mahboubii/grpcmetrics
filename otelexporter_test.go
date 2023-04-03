package grpcmetrics

import (
	"context"
	"sync/atomic"

	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/aggregation"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

type exporter struct {
	data atomic.Value
}

func (e *exporter) Temporality(k metric.InstrumentKind) metricdata.Temporality {
	return metric.DefaultTemporalitySelector(k)
}

func (e *exporter) Aggregation(k metric.InstrumentKind) aggregation.Aggregation {
	return metric.DefaultAggregationSelector(k)
}

func (e *exporter) Export(ctx context.Context, data metricdata.ResourceMetrics) error {
	e.data.Store(data)

	return ctx.Err()
}

func (e *exporter) Read() metricdata.ResourceMetrics {
	d, ok := e.data.Load().(metricdata.ResourceMetrics)
	if !ok {
		panic(ok)
	}

	return d
}

func (e *exporter) ForceFlush(ctx context.Context) error {
	return ctx.Err()
}

func (e *exporter) Shutdown(ctx context.Context) error {
	return ctx.Err()
}
