package transport

import (
	"bufio"
	"context"
	"net"
	"sync"

	"transporter/proto"
)

const defaultTCPAddr = "127.0.0.1:9090"

type tcpConn struct {
	conn    net.Conn
	writer  *bufio.Writer
	encoder *proto.Encoder
	decoder *proto.Decoder
	mu      sync.Mutex
}

func newTCPClientConn(addr string) (FrameConn, error) {
	if addr == "" {
		addr = defaultTCPAddr
	}
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	return wrapTCPConn(conn), nil
}

func newTCPServerConn(ctx context.Context, addr string) (FrameConn, error) {
	if addr == "" {
		addr = defaultTCPAddr
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	connCh := make(chan net.Conn, 1)
	errCh := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			errCh <- err
			return
		}
		connCh <- conn
	}()

	var conn net.Conn
	select {
	case conn = <-connCh:
	case err := <-errCh:
		return nil, err
	case <-ctx.Done():
		ln.Close()
		return nil, ctx.Err()
	}
	go func() {
		<-ctx.Done()
		conn.Close()
	}()
	return wrapTCPConn(conn), nil
}

func wrapTCPConn(conn net.Conn) *tcpConn {
	bw := bufio.NewWriter(conn)
	return &tcpConn{
		conn:    conn,
		writer:  bw,
		encoder: proto.NewEncoder(bw),
		decoder: proto.NewDecoder(bufio.NewReader(conn)),
	}
}

func (t *tcpConn) Send(_ context.Context, frame *proto.Frame) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.encoder.WriteFrame(frame); err != nil {
		return err
	}
	return t.writer.Flush()
}

func (t *tcpConn) Receive(_ context.Context) (*proto.Frame, error) {
	return t.decoder.ReadFrame()
}

func (t *tcpConn) Close() error {
	return t.conn.Close()
}
