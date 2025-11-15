package transport

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"transporter/proto"
)

func TestHTTPTransportRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	addr := freeTCPPort(t)

	serverConn, err := newHTTPServerConn(ctx, addr)
	if err != nil {
		t.Fatalf("http server conn: %v", err)
	}
	defer serverConn.Close()

	clientConn := newHTTPClientConn(fmt.Sprintf("http://%s", addr))

	openFrame := &proto.Frame{
		Version:       proto.Version,
		Command:       proto.CommandOpen,
		Proto:         proto.ProtocolTCP,
		Stream:        42,
		TargetAddress: "example.com",
		TargetPort:    80,
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- clientConn.Send(ctx, openFrame)
	}()

	received, err := serverConn.Receive(ctx)
	if err != nil {
		t.Fatalf("server receive: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("client send: %v", err)
	}
	if received.Stream != openFrame.Stream || received.Command != proto.CommandOpen {
		t.Fatalf("unexpected frame: %+v", received)
	}

	dataFrame := &proto.Frame{
		Version: proto.Version,
		Command: proto.CommandData,
		Proto:   proto.ProtocolTCP,
		Stream:  openFrame.Stream,
		Payload: []byte("payload"),
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
	if clientReceived.Command != proto.CommandData || string(clientReceived.Payload) != "payload" {
		t.Fatalf("unexpected data frame: %+v", clientReceived)
	}
}

func freeTCPPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("skipping http transport test: %v", err)
	}
	defer ln.Close()
	return ln.Addr().String()
}
