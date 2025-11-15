package transport

import (
	"bufio"
	"context"
	"net"
	"os"

	"transporter/proto"
)

type udsConn struct {
	netConn net.Conn
	encoder *proto.Encoder
	decoder *proto.Decoder
	writer  *bufio.Writer
}

func newUDSClientConn(path string) (FrameConn, error) {
	conn, err := net.Dial("unix", path)
	if err != nil {
		return nil, err
	}
	return wrapUnixConn(conn), nil
}

func newUDSServerConn(ctx context.Context, path string) (FrameConn, error) {
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	conn, err := ln.Accept()
	if err != nil {
		ln.Close()
		return nil, err
	}
	return wrapUnixConn(conn), nil
}

func wrapUnixConn(conn net.Conn) *udsConn {
	bw := bufio.NewWriter(conn)
	return &udsConn{
		netConn: conn,
		encoder: proto.NewEncoder(bw),
		decoder: proto.NewDecoder(bufio.NewReader(conn)),
		writer:  bw,
	}
}

func (u *udsConn) Send(_ context.Context, frame *proto.Frame) error {
	if err := u.encoder.WriteFrame(frame); err != nil {
		return err
	}
	return u.writer.Flush()
}

func (u *udsConn) Receive(_ context.Context) (*proto.Frame, error) {
	return u.decoder.ReadFrame()
}

func (u *udsConn) Close() error {
	return u.netConn.Close()
}
