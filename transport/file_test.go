package transport

import (
	"context"
	"testing"
	"time"

	"transporter/proto"
)

func TestFileTransportRoundTrip(t *testing.T) {
	dir := t.TempDir()

	clientConn, err := newFileConn(dir, RoleClient)
	if err != nil {
		t.Fatalf("client conn: %v", err)
	}
	serverConn, err := newFileConn(dir, RoleServer)
	if err != nil {
		t.Fatalf("server conn: %v", err)
	}
	t.Cleanup(func() {
		_ = clientConn.Close()
		_ = serverConn.Close()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	clientFrame := &proto.Frame{
		Version:       proto.Version,
		Command:       proto.CommandOpen,
		Proto:         proto.ProtocolTCP,
		Stream:        1,
		TargetAddress: "example.com",
		TargetPort:    80,
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- clientConn.Send(ctx, clientFrame)
	}()

	serverReceived, err := serverConn.Receive(ctx)
	if err != nil {
		t.Fatalf("server receive: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("client send: %v", err)
	}

	if serverReceived.Command != clientFrame.Command ||
		serverReceived.TargetAddress != clientFrame.TargetAddress ||
		serverReceived.TargetPort != clientFrame.TargetPort {
		t.Fatalf("unexpected frame: %+v", serverReceived)
	}

	dataFrame := &proto.Frame{
		Version: proto.Version,
		Command: proto.CommandData,
		Proto:   proto.ProtocolTCP,
		Stream:  1,
		Payload: []byte("hello"),
	}

	errCh = make(chan error, 1)
	go func() {
		errCh <- serverConn.Send(ctx, dataFrame)
	}()

	clientReceived, err := clientConn.Receive(ctx)
	if err != nil {
		t.Fatalf("client receive: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("server send: %v", err)
	}

	if clientReceived.Command != proto.CommandData ||
		string(clientReceived.Payload) != "hello" {
		t.Fatalf("unexpected data frame: %+v", clientReceived)
	}
}
