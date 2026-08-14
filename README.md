# Go OpenTelemetry gRPC Metrics Instrumentation

[![ci](https://github.com/mahboubii/grpcmetrics/actions/workflows/workflow.yaml/badge.svg?branch=main)](https://github.com/mahboubii/grpcmetrics/actions/workflows/workflow.yaml)
[![Go Report Card](https://goreportcard.com/badge/github.com/mahboubii/grpcmetrics)](https://goreportcard.com/report/github.com/mahboubii/grpcmetrics)
[![Documentation](https://pkg.go.dev/badge/github.com/mahboubii/grpcmetrics.svg)](https://pkg.go.dev/github.com/mahboubii/grpcmetrics)

OpenTelemetry (OTel) metric instrumentation for Go gRPC servers and clients, based on [gRPC Stats](https://pkg.go.dev/google.golang.org/grpc/stats).

Requires **Go 1.25** or later.

## Install

```bash
go get github.com/mahboubii/grpcmetrics
```

## Usage

Metrics follow the historical [RPC metric conventions](https://github.com/open-telemetry/semantic-conventions/blob/v1.17.0/docs/rpc/rpc-metrics.md) used by this module, with one documented exception: the following instruments are counters instead of histograms to keep cardinality low:

1. `rpc.server.requests_per_rpc`
2. `rpc.server.responses_per_rpc`
3. `rpc.client.requests_per_rpc`
4. `rpc.client.responses_per_rpc`

`duration`, `request.size`, and `response.size` are not reported by default. Enable them with the [options](https://pkg.go.dev/github.com/mahboubii/grpcmetrics#Option) on the constructor.

### Server side metrics

```go
handler, err := grpcmetrics.NewServerHandler()
if err != nil {
    log.Panic(err)
}

server := grpc.NewServer(
    grpc.StatsHandler(handler),
)
```

If you also use [`otelgrpc`](https://pkg.go.dev/go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc), attach only one metrics stats handler so RPC duration is not recorded twice.

### Client side metrics

```go
handler, err := grpcmetrics.NewClientHandler()
if err != nil {
    log.Panic(err)
}

connection, err := grpc.NewClient("server:8080",
    grpc.WithTransportCredentials(insecure.NewCredentials()),
    grpc.WithStatsHandler(handler),
)
```
