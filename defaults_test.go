package grpcmetrics

import (
	"context"
	"testing"

	"github.com/mahboubii/grpcmetrics/testserver"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/test/bufconn"
)

func TestDefaultOptionsOmitOptionalHistograms(t *testing.T) {
	ctx := context.Background()
	lis := bufconn.Listen(1024 * 1024)
	t.Cleanup(func() { require.NoError(t, lis.Close()) })

	sMetrics := newTestServer(t, lis)
	cli, cMetrics := newTestClient(t, lis)

	_, err := cli.Ok(ctx, &testserver.Empty{})
	require.NoError(t, err)

	attrs := rpcTestAttrs("OK", codes.OK, "Ok", "testserver.TestsService")

	serverMetrics := sMetrics().ScopeMetrics
	assertMetric(t, serverMetrics, attrs, metricdata.Metrics{
		Name: "rpc.server.requests_per_rpc", Unit: "1",
		Data: metricdata.Sum[int64]{IsMonotonic: true, DataPoints: []metricdata.DataPoint[int64]{{Value: 1}}},
	})
	assertMetric(t, serverMetrics, attrs, metricdata.Metrics{
		Name: "rpc.server.responses_per_rpc", Unit: "1",
		Data: metricdata.Sum[int64]{IsMonotonic: true, DataPoints: []metricdata.DataPoint[int64]{{Value: 1}}},
	})
	assertNoMetric(t, serverMetrics, "rpc.server.duration")
	assertNoMetric(t, serverMetrics, "rpc.server.request.size")
	assertNoMetric(t, serverMetrics, "rpc.server.response.size")

	clientMetrics := cMetrics().ScopeMetrics
	assertMetric(t, clientMetrics, attrs, metricdata.Metrics{
		Name: "rpc.client.requests_per_rpc", Unit: "1",
		Data: metricdata.Sum[int64]{IsMonotonic: true, DataPoints: []metricdata.DataPoint[int64]{{Value: 1}}},
	})
	assertMetric(t, clientMetrics, attrs, metricdata.Metrics{
		Name: "rpc.client.responses_per_rpc", Unit: "1",
		Data: metricdata.Sum[int64]{IsMonotonic: true, DataPoints: []metricdata.DataPoint[int64]{{Value: 1}}},
	})
	assertNoMetric(t, clientMetrics, "rpc.client.duration")
	assertNoMetric(t, clientMetrics, "rpc.client.request.size")
	assertNoMetric(t, clientMetrics, "rpc.client.response.size")
}

func TestLatencyOnlyDoesNotEmitSizes(t *testing.T) {
	ctx := context.Background()
	lis := bufconn.Listen(1024 * 1024)
	t.Cleanup(func() { require.NoError(t, lis.Close()) })

	sMetrics := newTestServer(t, lis, WithInstrumentLatency(true))
	cli, cMetrics := newTestClient(t, lis, WithInstrumentLatency(true))

	_, err := cli.Ok(ctx, &testserver.Empty{})
	require.NoError(t, err)

	attrs := rpcTestAttrs("OK", codes.OK, "Ok", "testserver.TestsService")
	serverMetrics := sMetrics().ScopeMetrics
	assertMetric(t, serverMetrics, attrs, metricdata.Metrics{
		Name: "rpc.server.duration", Unit: "ms",
		Data: metricdata.Histogram[float64]{DataPoints: []metricdata.HistogramDataPoint[float64]{{Count: 1}}},
	})
	assertNoMetric(t, serverMetrics, "rpc.server.request.size")
	assertNoMetric(t, serverMetrics, "rpc.server.response.size")

	clientMetrics := cMetrics().ScopeMetrics
	assertMetric(t, clientMetrics, attrs, metricdata.Metrics{
		Name: "rpc.client.duration", Unit: "ms",
		Data: metricdata.Histogram[float64]{DataPoints: []metricdata.HistogramDataPoint[float64]{{Count: 1}}},
	})
	assertNoMetric(t, clientMetrics, "rpc.client.request.size")
	assertNoMetric(t, clientMetrics, "rpc.client.response.size")
}

func TestSizesOnlyDoesNotEmitDuration(t *testing.T) {
	ctx := context.Background()
	lis := bufconn.Listen(1024 * 1024)
	t.Cleanup(func() { require.NoError(t, lis.Close()) })

	sMetrics := newTestServer(t, lis, WithInstrumentSizes(true))
	cli, cMetrics := newTestClient(t, lis, WithInstrumentSizes(true))

	_, err := cli.Ok(ctx, &testserver.Empty{})
	require.NoError(t, err)

	attrs := rpcTestAttrs("OK", codes.OK, "Ok", "testserver.TestsService")
	serverMetrics := sMetrics().ScopeMetrics
	assertMetric(t, serverMetrics, attrs, metricdata.Metrics{
		Name: "rpc.server.response.size", Unit: "By",
		Data: metricdata.Histogram[int64]{DataPoints: []metricdata.HistogramDataPoint[int64]{{Count: 1, Sum: 2}}},
	})
	assertNoMetric(t, serverMetrics, "rpc.server.duration")

	clientMetrics := cMetrics().ScopeMetrics
	assertMetric(t, clientMetrics, attrs, metricdata.Metrics{
		Name: "rpc.client.response.size", Unit: "By",
		Data: metricdata.Histogram[int64]{DataPoints: []metricdata.HistogramDataPoint[int64]{{Count: 1, Sum: 2}}},
	})
	assertNoMetric(t, clientMetrics, "rpc.client.duration")
}
