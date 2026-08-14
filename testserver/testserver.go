package testserver

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Server struct {
	TestsServiceServer
}

func (s *Server) Ok(context.Context, *Empty) (*NonEmpty, error) {
	return &NonEmpty{Value: 1}, nil
}

func (s *Server) Error(context.Context, *Empty) (*Empty, error) {
	return nil, status.Error(codes.NotFound, "error")
}

func (s *Server) Stream(_ *Empty, stream TestsService_StreamServer) error {
	for i := range 10 {
		if err := stream.Send(&NonEmpty{Value: int32(i)}); err != nil {
			return err
		}
	}

	return nil
}
