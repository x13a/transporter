//go:build unix

package transport

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"syscall"

	"transporter/proto"
)

type fifoConn struct {
	r      *os.File
	w      *os.File
	dec    *proto.Decoder
	enc    *proto.Encoder
	writer *bufio.Writer
	mu     sync.Mutex
}

func newPipeConn(inPath, outPath string) (FrameConn, error) {
	if err := ensureFIFO(inPath); err != nil {
		return nil, err
	}
	if err := ensureFIFO(outPath); err != nil {
		return nil, err
	}

	readerCh := make(chan *os.File, 1)
	errCh := make(chan error, 1)
	go func() {
		f, err := os.OpenFile(inPath, os.O_RDONLY, 0)
		if err != nil {
			errCh <- err
			return
		}
		readerCh <- f
	}()

	writer, err := os.OpenFile(outPath, os.O_WRONLY, 0)
	if err != nil {
		return nil, err
	}

	var reader *os.File
	select {
	case reader = <-readerCh:
	case err := <-errCh:
		writer.Close()
		return nil, err
	}

	bw := bufio.NewWriter(writer)
	return &fifoConn{
		r:      reader,
		w:      writer,
		dec:    proto.NewDecoder(bufio.NewReader(reader)),
		enc:    proto.NewEncoder(bw),
		writer: bw,
	}, nil
}

func (f *fifoConn) Send(_ context.Context, frame *proto.Frame) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enc.WriteFrame(frame); err != nil {
		return err
	}
	return f.writer.Flush()
}

func (f *fifoConn) Receive(_ context.Context) (*proto.Frame, error) {
	return f.dec.ReadFrame()
}

func (f *fifoConn) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	err := f.writer.Flush()
	if err1 := f.r.Close(); err == nil {
		err = err1
	}
	if err2 := f.w.Close(); err == nil {
		err = err2
	}
	return err
}

func ensureFIFO(path string) error {
	if path == "" {
		return errors.New("fifo path empty")
	}
	info, err := os.Stat(path)
	if err == nil {
		if info.Mode()&os.ModeNamedPipe == 0 {
			return fmt.Errorf("%s exists and is not a fifo", path)
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syscall.Mkfifo(path, 0o600)
}
