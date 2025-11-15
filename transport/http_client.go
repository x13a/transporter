package transport

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"transporter/proto"
)

type httpClientConn struct {
	baseURL *url.URL
	client  *http.Client
}

func newHTTPClientConn(base string) FrameConn {
	parsed := ensureBaseURL(base)
	return &httpClientConn{
		baseURL: parsed,
		client: &http.Client{
			Timeout: 20 * time.Second,
		},
	}
}

func (c *httpClientConn) Close() error {
	return nil
}

func (c *httpClientConn) Send(ctx context.Context, frame *proto.Frame) error {
	data, err := proto.EncodeFrame(frame)
	if err != nil {
		return err
	}
	uploadURL := c.baseURL.ResolveReference(&url.URL{Path: "/upload"})
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		uploadURL.String(),
		bytes.NewReader(data),
	)
	if err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upload failed: %s", bytes.TrimSpace(body))
	}
	return nil
}

func (c *httpClientConn) Receive(ctx context.Context) (*proto.Frame, error) {
	for {
		downloadURL := c.baseURL.ResolveReference(&url.URL{Path: "/download"})
		req, err := http.NewRequestWithContext(
			ctx,
			http.MethodGet,
			downloadURL.String(),
			nil,
		)
		if err != nil {
			return nil, err
		}
		resp, err := c.client.Do(req)
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		switch resp.StatusCode {
		case http.StatusOK:
			return proto.DecodeFrame(body)
		case http.StatusNoContent:
			continue
		default:
			return nil, fmt.Errorf("download failed: status %d", resp.StatusCode)
		}
	}
}

func ensureBaseURL(raw string) *url.URL {
	if raw == "" {
		raw = "http://127.0.0.1:8080"
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + strings.TrimPrefix(raw, "//")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		panic(fmt.Sprintf("invalid http base url %q: %v", raw, err))
	}
	if parsed.Host == "" && parsed.Path != "" {
		parsed.Host = parsed.Path
		parsed.Path = ""
	}
	if parsed.Host == "" {
		panic(fmt.Sprintf("invalid http base url %q: host missing", raw))
	}
	return parsed
}
