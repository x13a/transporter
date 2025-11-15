package transport

import (
	"context"
	"io"
	"testing"

	"transporter/proto"
)

func TestStdIOTransportRoundTrip(t *testing.T) {
	ctx := context.Background()
	aReader, aWriter := io.Pipe()
	bReader, bWriter := io.Pipe()

	client := newStdIOConnWithRW(aReader, bWriter)
	server := newStdIOConnWithRW(bReader, aWriter)

	frame := &proto.Frame{
		Version:       proto.Version,
		Command:       proto.CommandOpen,
		Proto:         proto.ProtocolTCP,
		Stream:        1,
		TargetAddress: "example.com",
		TargetPort:    80,
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- client.Send(ctx, frame)
	}()

	got, err := server.Receive(ctx)
	if err != nil {
		t.Fatalf("server receive: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("client send: %v", err)
	}
	if got.TargetAddress != frame.TargetAddress || got.TargetPort != frame.TargetPort {
		t.Fatalf("unexpected frame: %+v", got)
	}
}
