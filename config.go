package grpcmetrics

import "go.opentelemetry.io/otel/metric"

// Option applies an option value when creating a Handler.
type Option interface {
	apply(*config)
}

type optionFunc func(*config)

func (f optionFunc) apply(c *config) {
	f(c)
}

type config struct {
	meterProvider       metric.MeterProvider
	instrumentationName string
	instrumentSizes     bool
	instrumentLatency   bool
}

// WithInstrumentationName returns an Option to set custom name for metrics scope.
func WithInstrumentationName(name string) Option {
	return optionFunc(func(c *config) {
		c.instrumentationName = name
	})
}

// WithMeterProvider returns an Option to use custom MetricProvider when creating metrics.
func WithMeterProvider(p metric.MeterProvider) Option {
	return optionFunc(func(c *config) {
		c.meterProvider = p
	})
}

// WithInstrumentSizes enable instrument for rpc.{server|client}.response.size and rpc.{server|client}.request.size.
// This is a histogram which is quite costly.
func WithInstrumentSizes(instrumentSizes bool) Option {
	return optionFunc(func(c *config) {
		c.instrumentSizes = instrumentSizes
	})
}

// WithInstrumentLatency enable instrument for rpc.{server|client}.duration.
// This is a histogram which is quite costly.
func WithInstrumentLatency(instrumentLatency bool) Option {
	return optionFunc(func(c *config) {
		c.instrumentLatency = instrumentLatency
	})
}
