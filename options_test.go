package grpcmetrics

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/embedded"
	"go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/stats"
	"google.golang.org/grpc/status"
)

func TestPublicConstructors(t *testing.T) {
	server, err := NewServerHandler(WithMeterProvider(noop.NewMeterProvider()))
	require.NoError(t, err)
	require.NotNil(t, server)
	sh, ok := server.(*Handler)
	require.True(t, ok)
	assert.False(t, sh.isClient)

	client, err := NewClientHandler(WithMeterProvider(noop.NewMeterProvider()))
	require.NoError(t, err)
	require.NotNil(t, client)
	ch, ok := client.(*Handler)
	require.True(t, ok)
	assert.True(t, ch.isClient)
}

func TestNilMeterProviderUsesGlobal(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(mp)
	t.Cleanup(func() {
		otel.SetMeterProvider(prev)
		require.NoError(t, mp.Shutdown(context.Background()))
	})

	h, err := NewServerHandler()
	require.NoError(t, err)
	handler, ok := h.(*Handler)
	require.True(t, ok)

	ctx := handler.TagRPC(context.Background(), &stats.RPCTagInfo{FullMethodName: "/svc.API/Call"})
	handler.HandleRPC(ctx, &stats.InPayload{Length: 1})
	handler.HandleRPC(ctx, &stats.OutPayload{Length: 1})
	handler.HandleRPC(ctx, &stats.End{})

	rm := collect(t, reader)
	require.NotEmpty(t, rm.ScopeMetrics)
	assert.Equal(t, DefaultInstrumentationName, rm.ScopeMetrics[0].Scope.Name)
	assertMetric(t, rm.ScopeMetrics, rpcTestAttrs("OK", codes.OK, "Call", "svc.API"), metricdata.Metrics{
		Name: "rpc.server.requests_per_rpc",
		Unit: "1",
		Data: metricdata.Sum[int64]{IsMonotonic: true, DataPoints: []metricdata.DataPoint[int64]{{Value: 1}}},
	})
}

func TestCustomInstrumentationName(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { require.NoError(t, mp.Shutdown(context.Background())) })

	h, err := newHandler(false, []Option{
		WithMeterProvider(mp),
		WithInstrumentationName("custom.scope"),
	})
	require.NoError(t, err)

	ctx := h.TagRPC(context.Background(), &stats.RPCTagInfo{FullMethodName: "/svc.API/Call"})
	h.HandleRPC(ctx, &stats.End{})

	rm := collect(t, reader)
	require.Len(t, rm.ScopeMetrics, 1)
	assert.Equal(t, "custom.scope", rm.ScopeMetrics[0].Scope.Name)
}

func TestEmptyInstrumentationNameUsesDefault(t *testing.T) {
	h, err := newHandler(false, []Option{
		WithMeterProvider(noop.NewMeterProvider()),
		WithInstrumentationName(""),
	})
	require.NoError(t, err)
	require.NotNil(t, h.rpcRequestsPerRPC)
}

func TestOptionalInstrumentsStayDisabled(t *testing.T) {
	h, err := newHandler(true, []Option{
		WithMeterProvider(noop.NewMeterProvider()),
		WithInstrumentLatency(false),
		WithInstrumentSizes(false),
	})
	require.NoError(t, err)
	assert.Nil(t, h.rpcDuration)
	assert.Nil(t, h.rpcRequestSize)
	assert.Nil(t, h.rpcResponseSize)
}

func TestNewHandlerInstrumentErrors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		failName string
		opts     []Option
	}{
		{name: "requests counter", failName: "rpc.server.requests_per_rpc"},
		{name: "responses counter", failName: "rpc.server.responses_per_rpc"},
		{name: "duration histogram", failName: "rpc.server.duration", opts: []Option{WithInstrumentLatency(true)}},
		{name: "request size histogram", failName: "rpc.server.request.size", opts: []Option{WithInstrumentSizes(true)}},
		{name: "response size histogram", failName: "rpc.server.response.size", opts: []Option{WithInstrumentSizes(true)}},
		{name: "client requests counter", failName: "rpc.client.requests_per_rpc"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isClient := strings.HasPrefix(tc.failName, "rpc.client.")
			opts := append([]Option{WithMeterProvider(failMeterProvider{failName: tc.failName})}, tc.opts...)
			h, err := newHandler(isClient, opts)
			require.Error(t, err)
			assert.Nil(t, h)
			assert.Contains(t, err.Error(), "grpcmetrics: create")
		})
	}
}

func TestGetAttributesMalformedMethods(t *testing.T) {
	baseOK := []attribute.KeyValue{
		attrRPCSystem.String(rpcSystemGRPC),
		attrRPCGRPCStatusCode.Int(0),
		attrRPCGRPCStatus.String("OK"),
	}

	tests := []struct {
		name   string
		method string
		err    error
		want   []attribute.KeyValue
	}{
		{
			name:   "canonical",
			method: "/product.Products/ListTags",
			want: append(append([]attribute.KeyValue{}, baseOK...),
				attrRPCService.String("product.Products"),
				attrRPCMethod.String("ListTags"),
			),
		},
		{
			name:   "missing leading slash",
			method: "product.Products/ListTags",
			want:   baseOK,
		},
		{
			name:   "extra segment",
			method: "/a/b/c",
			want:   baseOK,
		},
		{
			name:   "empty",
			method: "",
			want:   baseOK,
		},
		{
			name:   "status from non-rpc error",
			method: "/svc.API/Call",
			err:    errors.New("boom"),
			want: []attribute.KeyValue{
				attrRPCSystem.String(rpcSystemGRPC),
				attrRPCGRPCStatusCode.Int(int(codes.Internal)),
				attrRPCGRPCStatus.String("Internal"),
				attrRPCService.String("svc.API"),
				attrRPCMethod.String("Call"),
			},
		},
		{
			name:   "grpc status permission denied",
			method: "/svc.API/Call",
			err:    status.Error(codes.PermissionDenied, "nope"),
			want: []attribute.KeyValue{
				attrRPCSystem.String(rpcSystemGRPC),
				attrRPCGRPCStatusCode.Int(int(codes.PermissionDenied)),
				attrRPCGRPCStatus.String("PermissionDenied"),
				attrRPCService.String("svc.API"),
				attrRPCMethod.String("Call"),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := getAttributes(tc.method, tc.err)
			assert.ElementsMatch(t, tc.want, got.ToSlice())
		})
	}
}

func TestMetricPrefix(t *testing.T) {
	assert.Equal(t, "rpc.server", metricPrefix(false))
	assert.Equal(t, "rpc.client", metricPrefix(true))
}

type failMeterProvider struct {
	embedded.MeterProvider
	failName string
}

func (p failMeterProvider) Meter(string, ...metric.MeterOption) metric.Meter {
	return failMeter{failName: p.failName}
}

type failMeter struct {
	noop.Meter
	failName string
}

func (m failMeter) Int64Counter(name string, opts ...metric.Int64CounterOption) (metric.Int64Counter, error) {
	if name == m.failName {
		return nil, errors.New("forced counter error")
	}

	return m.Meter.Int64Counter(name, opts...)
}

func (m failMeter) Int64Histogram(name string, opts ...metric.Int64HistogramOption) (metric.Int64Histogram, error) {
	if name == m.failName {
		return nil, errors.New("forced histogram error")
	}

	return m.Meter.Int64Histogram(name, opts...)
}

func (m failMeter) Float64Histogram(name string, opts ...metric.Float64HistogramOption) (metric.Float64Histogram, error) {
	if name == m.failName {
		return nil, errors.New("forced histogram error")
	}

	return m.Meter.Float64Histogram(name, opts...)
}

func rpcTestAttrs(statusName string, code codes.Code, method, service string) []attribute.KeyValue {
	return []attribute.KeyValue{
		{Key: "rpc.grpc.status", Value: attribute.StringValue(statusName)},
		{Key: "rpc.grpc.status_code", Value: attribute.IntValue(int(code))},
		{Key: "rpc.method", Value: attribute.StringValue(method)},
		{Key: "rpc.service", Value: attribute.StringValue(service)},
		{Key: "rpc.system", Value: attribute.StringValue("grpc")},
	}
}
