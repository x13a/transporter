package transport

import (
	"bufio"
	"context"
	"io"
	"os"

	"transporter/proto"
)

type stdioConn struct {
	enc    *proto.Encoder
	dec    *proto.Decoder
	writer *bufio.Writer
}

func newStdIOConn(_ context.Context, role Role) FrameConn {
	return newStdIOConnWithRW(os.Stdin, os.Stdout)
}

func newStdIOConnWithRW(r io.Reader, w io.Writer) *stdioConn {
	bw := bufio.NewWriter(w)
	return &stdioConn{
		enc:    proto.NewEncoder(bw),
		dec:    proto.NewDecoder(bufio.NewReader(r)),
		writer: bw,
	}
}

func (s *stdioConn) Send(_ context.Context, frame *proto.Frame) error {
	if err := s.enc.WriteFrame(frame); err != nil {
		return err
	}
	return s.writer.Flush()
}

func (s *stdioConn) Receive(_ context.Context) (*proto.Frame, error) {
	return s.dec.ReadFrame()
}

func (s *stdioConn) Close() error {
	return s.writer.Flush()
}
