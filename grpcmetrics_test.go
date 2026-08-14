package grpcmetrics

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"

	"github.com/mahboubii/grpcmetrics/testserver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/stats"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func TestRPCInfoCtx(t *testing.T) {
	ctx := context.Background()
	ri := &rpcInfo{fullMethodName: "method"}

	ctx = setRPCInfo(ctx, ri)
	riCtx := getRPCInfo(ctx)

	assert.Equal(t, ri, riCtx)
	assert.Nil(t, getRPCInfo(context.Background()))
}

func TestGetRPCStatus(t *testing.T) {
	assert.Equal(t, status.New(codes.OK, "OK"), getRPCStatus(nil))
	assert.Equal(t, codes.Internal, getRPCStatus(errors.New("non rpc err")).Code())
	assert.Equal(t, codes.NotFound, getRPCStatus(status.Error(codes.NotFound, "")).Code())
}

func TestGetAttributes(t *testing.T) {
	listAttrs := getAttributes("/product.Products/ListTags", nil)
	assert.ElementsMatch(t,
		[]attribute.KeyValue{
			attrRPCSystem.String(rpcSystemGRPC),
			attrRPCGRPCStatusCode.Int(0),
			attrRPCGRPCStatus.String("OK"),
			attrRPCService.String("product.Products"),
			attrRPCMethod.String("ListTags"),
		},
		listAttrs.ToSlice(),
	)

	listAttrsErr := getAttributes("/product.Products/ListTags", status.Error(codes.InvalidArgument, ""))

	assert.ElementsMatch(t,
		[]attribute.KeyValue{
			attrRPCSystem.String(rpcSystemGRPC),
			attrRPCGRPCStatusCode.Int(3),
			attrRPCGRPCStatus.String("InvalidArgument"),
			attrRPCService.String("product.Products"),
			attrRPCMethod.String("ListTags"),
		},
		listAttrsErr.ToSlice(),
	)

	malformed := getAttributes("not-a-grpc-method", nil)
	assert.ElementsMatch(t,
		[]attribute.KeyValue{
			attrRPCSystem.String(rpcSystemGRPC),
			attrRPCGRPCStatusCode.Int(0),
			attrRPCGRPCStatus.String("OK"),
		},
		malformed.ToSlice(),
	)
}

func TestNewHandler(t *testing.T) {
	withDefaults, err := newHandler(false, nil)
	require.NoError(t, err)
	assert.Nil(t, withDefaults.rpcDuration)
	assert.Nil(t, withDefaults.rpcRequestSize)
	assert.Nil(t, withDefaults.rpcResponseSize)
	assert.NotNil(t, withDefaults.rpcRequestsPerRPC)
	assert.NotNil(t, withDefaults.rpcResponsesPerRPC)

	withConfigs, err := newHandler(true, []Option{
		WithInstrumentLatency(true),
		WithInstrumentationName("my_name"),
		WithInstrumentSizes(true),
		WithMeterProvider(noop.NewMeterProvider()),
	})

	require.NoError(t, err)
	assert.NotNil(t, withConfigs.rpcDuration)
	assert.NotNil(t, withConfigs.rpcRequestSize)
	assert.NotNil(t, withConfigs.rpcResponseSize)
	assert.NotNil(t, withConfigs.rpcRequestsPerRPC)
	assert.NotNil(t, withConfigs.rpcResponsesPerRPC)
}

func TestHandleRPCWithoutInfo(t *testing.T) {
	h, err := newHandler(false, nil)
	require.NoError(t, err)

	assert.NotPanics(t, func() {
		h.HandleRPC(context.Background(), &stats.End{})
		h.HandleRPC(context.Background(), &stats.DelayedPickComplete{})
		h.HandleRPC(context.Background(), &stats.Begin{})
	})
}

func newTestServer(t *testing.T, lis *bufconn.Listener, opts ...Option) func() metricdata.ResourceMetrics {
	t.Helper()

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	opts = append([]Option{WithMeterProvider(mp)}, opts...)
	handler, err := NewServerHandler(opts...)
	require.NoError(t, err)

	s := grpc.NewServer(grpc.StatsHandler(handler))
	testserver.RegisterTestsServiceServer(s, &testserver.Server{})

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- s.Serve(lis)
	}()
	t.Cleanup(s.Stop)

	return func() metricdata.ResourceMetrics {
		s.GracefulStop()
		<-serveErr

		var rm metricdata.ResourceMetrics
		require.NoError(t, reader.Collect(context.Background(), &rm))
		require.NoError(t, mp.Shutdown(context.Background()))

		return rm
	}
}

func newTestClient(t *testing.T, lis *bufconn.Listener, opts ...Option) (testserver.TestsServiceClient, func() metricdata.ResourceMetrics) {
	t.Helper()

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	opts = append([]Option{WithMeterProvider(mp)}, opts...)
	handler, err := NewClientHandler(opts...)
	require.NoError(t, err)

	bufDialer := func(context.Context, string) (net.Conn, error) {
		return lis.Dial()
	}

	conn, err := grpc.NewClient(
		"passthrough://bufnet",
		grpc.WithStatsHandler(handler),
		grpc.WithContextDialer(bufDialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)

	return testserver.NewTestsServiceClient(conn), func() metricdata.ResourceMetrics {
		require.NoError(t, conn.Close())

		var rm metricdata.ResourceMetrics
		require.NoError(t, reader.Collect(context.Background(), &rm))
		require.NoError(t, mp.Shutdown(context.Background()))

		return rm
	}
}

func TestUnary(t *testing.T) {
	ctx := context.Background()
	lis := bufconn.Listen(1024 * 1024)
	t.Cleanup(func() { require.NoError(t, lis.Close()) })

	sMetrics := newTestServer(t, lis, WithInstrumentLatency(true), WithInstrumentSizes(true))
	cli, cMetrics := newTestClient(t, lis, WithInstrumentLatency(true), WithInstrumentSizes(true))

	_, err := cli.Ok(ctx, &testserver.Empty{})
	require.NoError(t, err)

	_, err = cli.Ok(ctx, &testserver.Empty{})
	require.NoError(t, err)

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
	assertMetric(t, serverMetrics, attrs, metricdata.Metrics{Name: "rpc.server.duration", Unit: "ms", Data: metricdata.Histogram[float64]{
		DataPoints: []metricdata.HistogramDataPoint[float64]{{Count: 2}},
	}})
	assertMetric(t, serverMetrics, attrs, metricdata.Metrics{Name: "rpc.server.request.size", Unit: "By", Data: metricdata.Histogram[int64]{
		DataPoints: []metricdata.HistogramDataPoint[int64]{{Count: 2}},
	}})
	assertMetric(t, serverMetrics, attrs, metricdata.Metrics{Name: "rpc.server.response.size", Unit: "By", Data: metricdata.Histogram[int64]{
		DataPoints: []metricdata.HistogramDataPoint[int64]{{Count: 2, Sum: 4}},
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
	assertMetric(t, clientMetrics, attrs, metricdata.Metrics{Name: "rpc.client.duration", Unit: "ms", Data: metricdata.Histogram[float64]{
		DataPoints: []metricdata.HistogramDataPoint[float64]{{Count: 2}},
	}})
	assertMetric(t, clientMetrics, attrs, metricdata.Metrics{Name: "rpc.client.request.size", Unit: "By", Data: metricdata.Histogram[int64]{
		DataPoints: []metricdata.HistogramDataPoint[int64]{{Count: 2}},
	}})
	assertMetric(t, clientMetrics, attrs, metricdata.Metrics{Name: "rpc.client.response.size", Unit: "By", Data: metricdata.Histogram[int64]{
		DataPoints: []metricdata.HistogramDataPoint[int64]{{Count: 2, Sum: 4}},
	}})
}

func TestError(t *testing.T) {
	ctx := context.Background()
	lis := bufconn.Listen(1024 * 1024)
	t.Cleanup(func() { require.NoError(t, lis.Close()) })

	sMetrics := newTestServer(t, lis, WithInstrumentLatency(true), WithInstrumentSizes(true))
	cli, cMetrics := newTestClient(t, lis, WithInstrumentLatency(true), WithInstrumentSizes(true))

	_, err := cli.Error(ctx, &testserver.Empty{})
	require.Error(t, err)

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
	assertMetric(t, serverMetrics, attrs, metricdata.Metrics{Name: "rpc.server.duration", Unit: "ms", Data: metricdata.Histogram[float64]{
		DataPoints: []metricdata.HistogramDataPoint[float64]{{Count: 1}},
	}})
	assertMetric(t, serverMetrics, attrs, metricdata.Metrics{Name: "rpc.server.request.size", Unit: "By", Data: metricdata.Histogram[int64]{
		DataPoints: []metricdata.HistogramDataPoint[int64]{{Count: 1}},
	}})
	assertMetric(t, serverMetrics, attrs, metricdata.Metrics{Name: "rpc.server.response.size", Unit: "By", Data: metricdata.Histogram[int64]{
		DataPoints: []metricdata.HistogramDataPoint[int64]{{Count: 1}},
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
	assertMetric(t, clientMetrics, attrs, metricdata.Metrics{Name: "rpc.client.duration", Unit: "ms", Data: metricdata.Histogram[float64]{
		DataPoints: []metricdata.HistogramDataPoint[float64]{{Count: 1}},
	}})
	assertMetric(t, clientMetrics, attrs, metricdata.Metrics{Name: "rpc.client.request.size", Unit: "By", Data: metricdata.Histogram[int64]{
		DataPoints: []metricdata.HistogramDataPoint[int64]{{Count: 1}},
	}})
	assertMetric(t, clientMetrics, attrs, metricdata.Metrics{Name: "rpc.client.response.size", Unit: "By", Data: metricdata.Histogram[int64]{
		DataPoints: []metricdata.HistogramDataPoint[int64]{{Count: 1}},
	}})
}

func TestStream(t *testing.T) {
	ctx := context.Background()
	lis := bufconn.Listen(1024 * 1024)
	t.Cleanup(func() { require.NoError(t, lis.Close()) })

	sMetrics := newTestServer(t, lis, WithInstrumentLatency(true), WithInstrumentSizes(true))
	cli, cMetrics := newTestClient(t, lis, WithInstrumentLatency(true), WithInstrumentSizes(true))

	res, err := cli.Stream(ctx, &testserver.Empty{})
	require.NoError(t, err)

	for {
		_, err := res.Recv()
		if err != nil {
			require.ErrorIs(t, err, io.EOF)

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
	assertMetric(t, serverMetrics, attrs, metricdata.Metrics{Name: "rpc.server.duration", Unit: "ms", Data: metricdata.Histogram[float64]{
		DataPoints: []metricdata.HistogramDataPoint[float64]{{Count: 1}},
	}})
	assertMetric(t, serverMetrics, attrs, metricdata.Metrics{Name: "rpc.server.request.size", Unit: "By", Data: metricdata.Histogram[int64]{
		DataPoints: []metricdata.HistogramDataPoint[int64]{{Count: 1}},
	}})
	assertMetric(t, serverMetrics, attrs, metricdata.Metrics{Name: "rpc.server.response.size", Unit: "By", Data: metricdata.Histogram[int64]{
		DataPoints: []metricdata.HistogramDataPoint[int64]{{Count: 1, Sum: 18}},
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
	assertMetric(t, clientMetrics, attrs, metricdata.Metrics{Name: "rpc.client.duration", Unit: "ms", Data: metricdata.Histogram[float64]{
		DataPoints: []metricdata.HistogramDataPoint[float64]{{Count: 1}},
	}})
	assertMetric(t, clientMetrics, attrs, metricdata.Metrics{Name: "rpc.client.request.size", Unit: "By", Data: metricdata.Histogram[int64]{
		DataPoints: []metricdata.HistogramDataPoint[int64]{{Count: 1}},
	}})
	assertMetric(t, clientMetrics, attrs, metricdata.Metrics{Name: "rpc.client.response.size", Unit: "By", Data: metricdata.Histogram[int64]{
		DataPoints: []metricdata.HistogramDataPoint[int64]{{Count: 1, Sum: 18}},
	}})
}

func assertMetric(t *testing.T, inMetrics []metricdata.ScopeMetrics, attrs []attribute.KeyValue, has metricdata.Metrics) {
	t.Helper()

	for _, sm := range inMetrics {
		assert.Equal(t, DefaultInstrumentationName, sm.Scope.Name)

		for _, m := range sm.Metrics {
			if m.Name != has.Name {
				continue
			}

			assert.Equal(t, has.Unit, m.Unit)

			switch d := m.Data.(type) {
			case metricdata.Histogram[int64]:
				inData, ok := has.Data.(metricdata.Histogram[int64])
				require.True(t, ok, "invalid data type")
				require.Len(t, d.DataPoints, len(inData.DataPoints))

				for i := range inData.DataPoints {
					assert.Equal(t, inData.DataPoints[i].Count, d.DataPoints[i].Count)

					if m.Unit != "ms" { // ignore sum check for time duration which is flaky
						assert.Equal(t, inData.DataPoints[i].Sum, d.DataPoints[i].Sum)
					}

					assert.ElementsMatch(t, attrs, d.DataPoints[i].Attributes.ToSlice())
				}
			case metricdata.Histogram[float64]:
				inData, ok := has.Data.(metricdata.Histogram[float64])
				require.True(t, ok, "invalid data type")
				require.Len(t, d.DataPoints, len(inData.DataPoints))

				for i := range inData.DataPoints {
					assert.Equal(t, inData.DataPoints[i].Count, d.DataPoints[i].Count)

					if m.Unit != "ms" {
						assert.InDelta(t, inData.DataPoints[i].Sum, d.DataPoints[i].Sum, 0.01)
					}

					assert.ElementsMatch(t, attrs, d.DataPoints[i].Attributes.ToSlice())
				}
			case metricdata.Sum[int64]:
				inData, ok := has.Data.(metricdata.Sum[int64])
				require.True(t, ok, "invalid data type")
				assert.Equal(t, inData.IsMonotonic, d.IsMonotonic)
				require.Len(t, d.DataPoints, len(inData.DataPoints))

				for i := range inData.DataPoints {
					assert.Equal(t, inData.DataPoints[i].Value, d.DataPoints[i].Value)
					assert.ElementsMatch(t, attrs, d.DataPoints[i].Attributes.ToSlice())
				}
			default:
				assert.Failf(t, "unexpected metric data type", "%T", m.Data)
			}

			return
		}
	}

	assert.Fail(t, "could not find metric for "+has.Name)
}

func assertNoMetric(t *testing.T, inMetrics []metricdata.ScopeMetrics, name string) {
	t.Helper()

	for _, sm := range inMetrics {
		for _, m := range sm.Metrics {
			if m.Name == name {
				assert.Fail(t, "did not expect metric "+name)
			}
		}
	}
}

func findMetric(t *testing.T, inMetrics []metricdata.ScopeMetrics, name string) metricdata.Metrics {
	t.Helper()

	for _, sm := range inMetrics {
		for _, m := range sm.Metrics {
			if m.Name == name {
				return m
			}
		}
	}

	t.Fatalf("could not find metric for %s", name)

	return metricdata.Metrics{}
}

func collect(t *testing.T, reader *sdkmetric.ManualReader) metricdata.ResourceMetrics {
	t.Helper()

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))

	return rm
}
