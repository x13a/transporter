//go:build unix

package transport

import (
	"context"
	"path/filepath"
	"testing"

	"transporter/proto"
)

func TestPipeConn(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.fifo")
	b := filepath.Join(dir, "b.fifo")
	if err := ensureFIFO(a); err != nil {
		t.Skipf("mkfifo not supported: %v", err)
	}
	if err := ensureFIFO(b); err != nil {
		t.Skipf("mkfifo not supported: %v", err)
	}

	serverCh := make(chan FrameConn, 1)
	errCh := make(chan error, 1)
	go func() {
		conn, err := newPipeConn(a, b)
		if err != nil {
			errCh <- err
			return
		}
		serverCh <- conn
	}()

	client, err := newPipeConn(b, a)
	if err != nil {
		t.Fatalf("client conn: %v", err)
	}
	defer client.Close()

	var server FrameConn
	select {
	case server = <-serverCh:
	case err := <-errCh:
		t.Fatalf("server conn: %v", err)
	}
	defer server.Close()

	frame := &proto.Frame{
		Version:       proto.Version,
		Command:       proto.CommandOpen,
		Proto:         proto.ProtocolTCP,
		Stream:        11,
		TargetAddress: "example.com",
		TargetPort:    80,
	}
	ctx := context.Background()
	sendErr := make(chan error, 1)
	go func() {
		sendErr <- client.Send(ctx, frame)
	}()
	got, err := server.Receive(ctx)
	if err != nil {
		t.Fatalf("server receive: %v", err)
	}
	if err := <-sendErr; err != nil {
		t.Fatalf("client send: %v", err)
	}
	if got.TargetAddress != frame.TargetAddress {
		t.Fatalf("unexpected frame: %+v", got)
	}
}
