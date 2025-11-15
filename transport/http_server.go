package transport

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"transporter/proto"
)

type httpServerConn struct {
	srv    *http.Server
	recvCh chan []byte
	sendCh chan []byte
	done   chan struct{}
}

func newHTTPServerConn(ctx context.Context, addr string) (FrameConn, error) {
	if addr == "" {
		addr = ":8080"
	}
	conn := &httpServerConn{
		recvCh: make(chan []byte, 128),
		sendCh: make(chan []byte, 128),
		done:   make(chan struct{}),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/upload", conn.handleUpload)
	mux.HandleFunc("/download", conn.handleDownload)

	srv := &http.Server{
		Addr:    addr,
		Handler: mux,
	}
	conn.srv = srv

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("http transport server exited: %v", err)
		}
		close(conn.done)
	}()

	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	return conn, nil
}

func (c *httpServerConn) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return c.srv.Shutdown(ctx)
}

func (c *httpServerConn) Send(ctx context.Context, frame *proto.Frame) error {
	data, err := proto.EncodeFrame(frame)
	if err != nil {
		return err
	}
	data = append([]byte(nil), data...)

	select {
	case c.sendCh <- data:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return io.EOF
	}
}

func (c *httpServerConn) Receive(ctx context.Context) (*proto.Frame, error) {
	select {
	case data := <-c.recvCh:
		return proto.DecodeFrame(data)
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.done:
		return nil, io.EOF
	}
}

func (c *httpServerConn) handleUpload(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	data, err := io.ReadAll(io.LimitReader(r.Body, proto.MaxPayloadLen+65535))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	select {
	case c.recvCh <- data:
		w.WriteHeader(http.StatusOK)
	case <-c.done:
		http.Error(w, "transport closed", http.StatusGone)
	default:
		http.Error(w, "backpressure", http.StatusTooManyRequests)
	}
}

func (c *httpServerConn) handleDownload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	select {
	case data := <-c.sendCh:
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	case <-ctx.Done():
		http.Error(w, "client canceled", http.StatusRequestTimeout)
	case <-time.After(15 * time.Second):
		w.WriteHeader(http.StatusNoContent)
	case <-c.done:
		http.Error(w, "transport closed", http.StatusGone)
	}
}
