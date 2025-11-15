package transport

import (
	"context"
	"net"
	"testing"

	"transporter/proto"
)

func TestTCPConnWrap(t *testing.T) {
	a, b := net.Pipe()
	tConn := wrapTCPConn(a)
	defer tConn.Close()

	decoder := proto.NewDecoder(b)

	ctx := context.Background()
	frame := &proto.Frame{
		Version:       proto.Version,
		Command:       proto.CommandOpen,
		Proto:         proto.ProtocolTCP,
		Stream:        123,
		TargetAddress: "example.com",
		TargetPort:    443,
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- tConn.Send(ctx, frame)
	}()

	got, err := decoder.ReadFrame()
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("send frame: %v", err)
	}
	if got.Stream != frame.Stream || got.TargetAddress != frame.TargetAddress {
		t.Fatalf("unexpected frame: %+v", got)
	}
}
