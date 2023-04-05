# Go OTEL gRPC Metrics

[![ci](https://github.com/mahboubii/grpcmetrics/actions/workflows/workflow.yaml/badge.svg?branch=main)](https://github.com/mahboubii/grpcmetrics/actions/workflows/workflow.yaml)
[![Go Report Card](https://goreportcard.com/badge/github.com/mahboubii/grpcmetrics)](https://goreportcard.com/report/github.com/mahboubii/grpcmetrics)
[![Documentation](https://godoc.org/github.com/mahboubii/grpcmetrics?status.svg)](https://pkg.go.dev/mod/github.com/mahboubii/grpcmetrics)

It is an OpenTelemetry (OTel) metric instrumentation for Golang gRPC servers and clients based on [gRPC Stats](https://pkg.go.dev/google.golang.org/grpc/stats).

## Install

```bash
$ go get github.com/mahboubii/grpcmetrics
```

## Usage

Metrics are reported based on [General RFC conventions](https://opentelemetry.io/docs/reference/specification/metrics/semantic_conventions/rpc-metrics/) specefications with some exceptions for following metrics where a normal counter is used instead of histograms to reduce the metrics cardinality:

1. `rpc.server.requests_per_rpc`
2. `rpc.server.responses_per_rpc`
3. `rpc.client.requests_per_rpc`
4. `rpc.client.responses_per_rpc`

Keep in mind `durations`, `request.size` and `response.size` are not reported by default. If you need to enable them check out the [options](https://pkg.go.dev/github.com/mahboubii/grpcmetrics#Option).

### Server side metrics

```go
handler, err := grpcmetrics.NewServerHandler()
if err != nil {
    log.Panic(err)
}

server := grpc.NewServer(
    grpc.StatsHandler(handler),

    // disable default otelgrpc duration metric to avoid sending metrics twice:
    grpc.UnaryInterceptor(grpc_middleware.ChainUnaryServer(
        otelgrpc.UnaryServerInterceptor(otelgrpc.WithMeterProvider(otelmetric.NewNoopMeterProvider())),
    )),
    grpc.StreamInterceptor(grpc_middleware.ChainStreamServer(
        otelgrpc.StreamServerInterceptor(otelgrpc.WithMeterProvider(otelmetric.NewNoopMeterProvider())),
    )),
)
```

### Client side metrics

```go
handler, err := grpcmetrics.NewClientHandler()
if err != nil {
    log.Panic(err)
}

connection, err := grpc.Dial("server:8080", grpc.WithStatsHandler(handler))
```
