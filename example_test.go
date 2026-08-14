package grpcmetrics_test

import (
	"log"

	"github.com/mahboubii/grpcmetrics"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func ExampleNewServerHandler() {
	handler, err := grpcmetrics.NewServerHandler()
	if err != nil {
		log.Fatal(err)
	}

	_ = grpc.NewServer(grpc.StatsHandler(handler))
}

func ExampleNewClientHandler() {
	handler, err := grpcmetrics.NewClientHandler()
	if err != nil {
		log.Fatal(err)
	}

	conn, err := grpc.NewClient("localhost:8080",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithStatsHandler(handler),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()
}
