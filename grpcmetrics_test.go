package grpcmetrics

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/mahboubii/grpcmetrics/testserver"
	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	semconv "go.opentelemetry.io/otel/semconv/v1.17.0"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func TestRPCInfoCtx(t *testing.T) {
	ctx := context.Background()
	ri := &rpcInfo{fullMethodName: "method"}

	ctx = setRPCInfo(ctx, ri)
	riCtx := getRPCInfo(ctx)

	assert.Equal(t, ri, riCtx)
}

func TestGetRPCStatus(t *testing.T) {
	assert.Equal(t, status.New(codes.OK, "OK"), getRPCStatus(nil))
	assert.Equal(t, codes.Internal, getRPCStatus(errors.New("non rpc err")).Code())
	assert.Equal(t, codes.NotFound, getRPCStatus(status.Error(codes.NotFound, "")).Code())
}

func TestGetAttributes(t *testing.T) {
	assert.ElementsMatch(t,
		[]attribute.KeyValue{
			semconv.RPCSystemGRPC,
			semconv.RPCGRPCStatusCodeKey.Int(0),
			attribute.Key("rpc.grpc.status").String("OK"),
			semconv.RPCServiceKey.String("product.Products"),
			semconv.RPCMethodKey.String("ListTags"),
		},
		getAttributes("/product.Products/ListTags", nil))

	assert.ElementsMatch(t,
		[]attribute.KeyValue{
			semconv.RPCSystemGRPC,
			semconv.RPCGRPCStatusCodeKey.Int(3),
			attribute.Key("rpc.grpc.status").String("InvalidArgument"),
			semconv.RPCServiceKey.String("product.Products"),
			semconv.RPCMethodKey.String("ListTags"),
		},
		getAttributes("/product.Products/ListTags", status.Error(codes.InvalidArgument, "")))
}

func TestNewHandler(t *testing.T) {
	withDefaults, err := newHandler(false, nil)
	assert.NoError(t, err)
	assert.Nil(t, withDefaults.rpcDuration)
	assert.Nil(t, withDefaults.rpcRequestSize)
	assert.Nil(t, withDefaults.rpcResponseSize)
	assert.NotNil(t, withDefaults.rpcRequestsPerRPC)
	assert.NotNil(t, withDefaults.rpcResponsesPerRPC)

	withConfigs, err := newHandler(true, []Option{
		WithInstrumentLatency(true),
		WithInstrumentationName("my_name"),
		WithInstrumentSizes(true),
		WithMeterProvider(metric.NewNoopMeterProvider()),
	})

	assert.NoError(t, err)
	assert.NotNil(t, withConfigs.rpcDuration)
	assert.NotNil(t, withConfigs.rpcRequestSize)
	assert.NotNil(t, withConfigs.rpcResponseSize)
	assert.NotNil(t, withConfigs.rpcRequestsPerRPC)
	assert.NotNil(t, withConfigs.rpcResponsesPerRPC)
}

func newTestServer(t *testing.T, lis *bufconn.Listener) func() metricdata.ResourceMetrics {
	t.Helper()

	exp := &exporter{}
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp)))
	handler, err := NewServerHandler(WithMeterProvider(mp), WithInstrumentLatency(true), WithInstrumentSizes(true))
	assert.NoError(t, err)

	s := grpc.NewServer(grpc.StatsHandler(handler))
	testserver.RegisterTestsServiceServer(s, &testserver.Server{})

	go func() {
		// s.Serve() cancels ctx during http2Server.finishStream() on server side
		hs := &http.Server{
			Handler:           h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.ServeHTTP(w, r) }), &http2.Server{}),
			ReadHeaderTimeout: time.Minute,
		}

		assert.NoError(t, hs.Serve(lis))
	}()

	return func() metricdata.ResourceMetrics {
		s.GracefulStop()
		mp.ForceFlush(context.Background())

		return exp.Read()
	}
}

func newTestClient(t *testing.T, lis *bufconn.Listener) (testserver.TestsServiceClient, func() metricdata.ResourceMetrics) {
	t.Helper()

	exp := &exporter{}
	// xx, _ := stdoutmetric.New()
	// mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp)), sdkmetric.WithReader(sdkmetric.NewPeriodicReader(xx)))
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp)))
	handler, err := NewClientHandler(WithMeterProvider(mp), WithInstrumentLatency(true), WithInstrumentSizes(true))
	assert.NoError(t, err)

	bufDialer := func(_ context.Context, address string) (net.Conn, error) {
		return lis.Dial()
	}

	conn, err := grpc.Dial("", grpc.WithStatsHandler(handler), grpc.WithContextDialer(bufDialer), grpc.WithBlock(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	assert.NoError(t, err)

	return testserver.NewTestsServiceClient(conn), func() metricdata.ResourceMetrics {
		assert.NoError(t, conn.Close())
		mp.ForceFlush(context.Background())

		return exp.Read()
	}
}

func TestUnary(t *testing.T) {
	ctx := context.Background()
	lis := bufconn.Listen(1024 * 1024)

	sMetrics := newTestServer(t, lis)
	cli, cMetrics := newTestClient(t, lis)

	_, err := cli.Ok(ctx, &testserver.Empty{})
	assert.NoError(t, err)

	_, err = cli.Ok(ctx, &testserver.Empty{})
	assert.NoError(t, err)

	attrs := []attribute.KeyValue{
		{Key: "rpc.grpc.status", Value: attribute.StringValue("OK")},
		{Key: "rpc.grpc.status_code", Value: attribute.IntValue(int(codes.OK))},
		{Key: "rpc.method", Value: attribute.StringValue("Ok")},
		{Key: "rpc.service", Value: attribute.StringValue("testserver.TestsService")},
		{Key: "rpc.system", Value: attribute.StringValue("grpc")},
	}

	serverMetrics := sMetrics().ScopeMetrics

	assertMetric(t, serverMetrics, attrs, metricdata.Metrics{Name: "rpc.server.requests_per_rpc", Unit: "1", Data: metricdata.Sum[int64]{
		IsMonotonic: true,
		DataPoints:  []metricdata.DataPoint[int64]{{Value: 2}},
	}})
	assertMetric(t, serverMetrics, attrs, metricdata.Metrics{Name: "rpc.server.responses_per_rpc", Unit: "1", Data: metricdata.Sum[int64]{
		IsMonotonic: true,
		DataPoints:  []metricdata.DataPoint[int64]{{Value: 2}},
	}})
	assertMetric(t, serverMetrics, attrs, metricdata.Metrics{Name: "rpc.server.duration", Unit: "ms", Data: metricdata.Histogram{
		DataPoints: []metricdata.HistogramDataPoint{{Count: 2}},
	}})
	assertMetric(t, serverMetrics, attrs, metricdata.Metrics{Name: "rpc.server.request.size", Unit: "By", Data: metricdata.Histogram{
		DataPoints: []metricdata.HistogramDataPoint{{Count: 2}},
	}})
	assertMetric(t, serverMetrics, attrs, metricdata.Metrics{Name: "rpc.server.response.size", Unit: "By", Data: metricdata.Histogram{
		DataPoints: []metricdata.HistogramDataPoint{{Count: 2, Sum: 4}},
	}})

	clientMetrics := cMetrics().ScopeMetrics

	assertMetric(t, clientMetrics, attrs, metricdata.Metrics{Name: "rpc.client.requests_per_rpc", Unit: "1", Data: metricdata.Sum[int64]{
		IsMonotonic: true,
		DataPoints:  []metricdata.DataPoint[int64]{{Value: 2}},
	}})
	assertMetric(t, clientMetrics, attrs, metricdata.Metrics{Name: "rpc.client.responses_per_rpc", Unit: "1", Data: metricdata.Sum[int64]{
		IsMonotonic: true,
		DataPoints:  []metricdata.DataPoint[int64]{{Value: 2}},
	}})
	assertMetric(t, clientMetrics, attrs, metricdata.Metrics{Name: "rpc.client.duration", Unit: "ms", Data: metricdata.Histogram{
		DataPoints: []metricdata.HistogramDataPoint{{Count: 2}},
	}})
	assertMetric(t, clientMetrics, attrs, metricdata.Metrics{Name: "rpc.client.request.size", Unit: "By", Data: metricdata.Histogram{
		DataPoints: []metricdata.HistogramDataPoint{{Count: 2}},
	}})
	assertMetric(t, clientMetrics, attrs, metricdata.Metrics{Name: "rpc.client.response.size", Unit: "By", Data: metricdata.Histogram{
		DataPoints: []metricdata.HistogramDataPoint{{Count: 2, Sum: 4}},
	}})
}

func TestError(t *testing.T) {
	ctx := context.Background()
	lis := bufconn.Listen(1024 * 1024)

	sMetrics := newTestServer(t, lis)
	cli, cMetrics := newTestClient(t, lis)

	_, err := cli.Error(ctx, &testserver.Empty{})
	assert.Error(t, err)

	attrs := []attribute.KeyValue{
		{Key: "rpc.grpc.status", Value: attribute.StringValue("NotFound")},
		{Key: "rpc.grpc.status_code", Value: attribute.IntValue(int(codes.NotFound))},
		{Key: "rpc.method", Value: attribute.StringValue("Error")},
		{Key: "rpc.service", Value: attribute.StringValue("testserver.TestsService")},
		{Key: "rpc.system", Value: attribute.StringValue("grpc")},
	}

	serverMetrics := sMetrics().ScopeMetrics

	assertMetric(t, serverMetrics, attrs, metricdata.Metrics{Name: "rpc.server.requests_per_rpc", Unit: "1", Data: metricdata.Sum[int64]{
		IsMonotonic: true,
		DataPoints:  []metricdata.DataPoint[int64]{{Value: 1}},
	}})
	assertMetric(t, serverMetrics, attrs, metricdata.Metrics{Name: "rpc.server.responses_per_rpc", Unit: "1", Data: metricdata.Sum[int64]{
		IsMonotonic: true,
		DataPoints:  []metricdata.DataPoint[int64]{{Value: 0}}, // zero out since errored
	}})
	assertMetric(t, serverMetrics, attrs, metricdata.Metrics{Name: "rpc.server.duration", Unit: "ms", Data: metricdata.Histogram{
		DataPoints: []metricdata.HistogramDataPoint{{Count: 1}},
	}})
	assertMetric(t, serverMetrics, attrs, metricdata.Metrics{Name: "rpc.server.request.size", Unit: "By", Data: metricdata.Histogram{
		DataPoints: []metricdata.HistogramDataPoint{{Count: 1}},
	}})
	assertMetric(t, serverMetrics, attrs, metricdata.Metrics{Name: "rpc.server.response.size", Unit: "By", Data: metricdata.Histogram{
		DataPoints: []metricdata.HistogramDataPoint{{Count: 1}},
	}})

	clientMetrics := cMetrics().ScopeMetrics

	assertMetric(t, clientMetrics, attrs, metricdata.Metrics{Name: "rpc.client.requests_per_rpc", Unit: "1", Data: metricdata.Sum[int64]{
		IsMonotonic: true,
		DataPoints:  []metricdata.DataPoint[int64]{{Value: 1}},
	}})
	assertMetric(t, clientMetrics, attrs, metricdata.Metrics{Name: "rpc.client.responses_per_rpc", Unit: "1", Data: metricdata.Sum[int64]{
		IsMonotonic: true,
		DataPoints:  []metricdata.DataPoint[int64]{{Value: 0}},
	}})
	assertMetric(t, clientMetrics, attrs, metricdata.Metrics{Name: "rpc.client.duration", Unit: "ms", Data: metricdata.Histogram{
		DataPoints: []metricdata.HistogramDataPoint{{Count: 1}},
	}})
	assertMetric(t, clientMetrics, attrs, metricdata.Metrics{Name: "rpc.client.request.size", Unit: "By", Data: metricdata.Histogram{
		DataPoints: []metricdata.HistogramDataPoint{{Count: 1}},
	}})
	assertMetric(t, clientMetrics, attrs, metricdata.Metrics{Name: "rpc.client.response.size", Unit: "By", Data: metricdata.Histogram{
		DataPoints: []metricdata.HistogramDataPoint{{Count: 1}},
	}})
}

func TestStream(t *testing.T) {
	ctx := context.Background()
	lis := bufconn.Listen(1024 * 1024)

	sMetrics := newTestServer(t, lis)
	cli, cMetrics := newTestClient(t, lis)

	res, err := cli.Stream(ctx, &testserver.Empty{})
	assert.NoError(t, err)

	for {
		_, err := res.Recv()
		if err != nil {
			assert.ErrorIs(t, io.EOF, err)

			break
		}
	}

	attrs := []attribute.KeyValue{
		{Key: "rpc.grpc.status", Value: attribute.StringValue("OK")},
		{Key: "rpc.grpc.status_code", Value: attribute.IntValue(int(codes.OK))},
		{Key: "rpc.method", Value: attribute.StringValue("Stream")},
		{Key: "rpc.service", Value: attribute.StringValue("testserver.TestsService")},
		{Key: "rpc.system", Value: attribute.StringValue("grpc")},
	}

	serverMetrics := sMetrics().ScopeMetrics

	assertMetric(t, serverMetrics, attrs, metricdata.Metrics{Name: "rpc.server.requests_per_rpc", Unit: "1", Data: metricdata.Sum[int64]{
		IsMonotonic: true,
		DataPoints:  []metricdata.DataPoint[int64]{{Value: 1}},
	}})
	assertMetric(t, serverMetrics, attrs, metricdata.Metrics{Name: "rpc.server.responses_per_rpc", Unit: "1", Data: metricdata.Sum[int64]{
		IsMonotonic: true,
		DataPoints:  []metricdata.DataPoint[int64]{{Value: 10}},
	}})
	assertMetric(t, serverMetrics, attrs, metricdata.Metrics{Name: "rpc.server.duration", Unit: "ms", Data: metricdata.Histogram{
		DataPoints: []metricdata.HistogramDataPoint{{Count: 1}},
	}})
	assertMetric(t, serverMetrics, attrs, metricdata.Metrics{Name: "rpc.server.request.size", Unit: "By", Data: metricdata.Histogram{
		DataPoints: []metricdata.HistogramDataPoint{{Count: 1}},
	}})
	assertMetric(t, serverMetrics, attrs, metricdata.Metrics{Name: "rpc.server.response.size", Unit: "By", Data: metricdata.Histogram{
		DataPoints: []metricdata.HistogramDataPoint{{Count: 1, Sum: 18}},
	}})

	clientMetrics := cMetrics().ScopeMetrics

	assertMetric(t, clientMetrics, attrs, metricdata.Metrics{Name: "rpc.client.requests_per_rpc", Unit: "1", Data: metricdata.Sum[int64]{
		IsMonotonic: true,
		DataPoints:  []metricdata.DataPoint[int64]{{Value: 1}},
	}})
	assertMetric(t, clientMetrics, attrs, metricdata.Metrics{Name: "rpc.client.responses_per_rpc", Unit: "1", Data: metricdata.Sum[int64]{
		IsMonotonic: true,
		DataPoints:  []metricdata.DataPoint[int64]{{Value: 10}},
	}})
	assertMetric(t, clientMetrics, attrs, metricdata.Metrics{Name: "rpc.client.duration", Unit: "ms", Data: metricdata.Histogram{
		DataPoints: []metricdata.HistogramDataPoint{{Count: 1}},
	}})
	assertMetric(t, clientMetrics, attrs, metricdata.Metrics{Name: "rpc.client.request.size", Unit: "By", Data: metricdata.Histogram{
		DataPoints: []metricdata.HistogramDataPoint{{Count: 1}},
	}})
	assertMetric(t, clientMetrics, attrs, metricdata.Metrics{Name: "rpc.client.response.size", Unit: "By", Data: metricdata.Histogram{
		DataPoints: []metricdata.HistogramDataPoint{{Count: 1, Sum: 18}},
	}})
}

func assertMetric(t *testing.T, inMetrics []metricdata.ScopeMetrics, attrs []attribute.KeyValue, has metricdata.Metrics) {
	t.Helper()

	for _, sm := range inMetrics {
		assert.Equal(t, DefaultInstrumentationName, sm.Scope.Name)

		for _, m := range sm.Metrics {
			if m.Name == has.Name {
				assert.Equal(t, has.Unit, m.Unit)

				switch d := m.Data.(type) {
				case metricdata.Histogram:
					inData, ok := has.Data.(metricdata.Histogram)
					assert.True(t, ok, "invalid data type")

					assert.Equal(t, len(inData.DataPoints), len(d.DataPoints))

					for i := range inData.DataPoints {
						assert.Equal(t, inData.DataPoints[i].Count, d.DataPoints[i].Count)

						if m.Unit != "ms" { // ignore sum check for time duration which is flaky
							assert.Equal(t, inData.DataPoints[i].Sum, d.DataPoints[i].Sum)
						}

						assert.ElementsMatch(t, attrs, d.DataPoints[i].Attributes.ToSlice())
					}
				case metricdata.Sum[int64]:
					inData, ok := has.Data.(metricdata.Sum[int64])
					assert.True(t, ok, "invalid data type")

					assert.Equal(t, inData.IsMonotonic, d.IsMonotonic)
					assert.Equal(t, len(inData.DataPoints), len(d.DataPoints))

					for i := range inData.DataPoints {
						assert.Equal(t, inData.DataPoints[i].Value, d.DataPoints[i].Value)
						assert.ElementsMatch(t, attrs, d.DataPoints[i].Attributes.ToSlice())
					}
				}

				return
			}
		}
	}

	assert.Fail(t, "could not find metric for "+has.Name)
}
