package transport

import (
	"context"
	"net"
	"testing"

	"transporter/proto"
)

func TestUDSConnWrap(t *testing.T) {
	tmp := t.TempDir()
	path := tmp + "/sock"

	serverCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Skipf("skipping uds test: %v", err)
	}
	ln.Close()

	serverConn, err := newUDSServerConn(serverCtx, path)
	if err != nil {
		t.Fatalf("server conn: %v", err)
	}
	t.Cleanup(func() { serverConn.Close() })

	clientConn, err := newUDSClientConn(path)
	if err != nil {
		t.Fatalf("client conn: %v", err)
	}
	t.Cleanup(func() { clientConn.Close() })

	frame := &proto.Frame{
		Version:       proto.Version,
		Command:       proto.CommandOpen,
		Proto:         proto.ProtocolTCP,
		Stream:        1,
		TargetAddress: "example.com",
		TargetPort:    80,
	}

	ctx := context.Background()
	errCh := make(chan error, 1)
	go func() {
		errCh <- clientConn.Send(ctx, frame)
	}()
	got, err := serverConn.Receive(ctx)
	if err != nil {
		t.Fatalf("server receive: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("client send: %v", err)
	}
	if got.TargetAddress != frame.TargetAddress {
		t.Fatalf("unexpected frame: %+v", got)
	}
}
