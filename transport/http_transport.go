package transport

import (
	"context"
	"flag"
	"fmt"
)

type httpFactory struct {
	listenAddr *string
	baseURL    *string
}

func init() {
	Register(&httpFactory{})
}

func (h *httpFactory) Name() string { return "http" }

func (h *httpFactory) AddFlags(fs *flag.FlagSet) {
	if h.listenAddr == nil {
		h.listenAddr = stringFlag(fs, "bind", "127.0.0.1:8080", "HTTP listen address (server mode)")
	}
	if h.baseURL == nil {
		h.baseURL = stringFlag(fs, "dest", "", "HTTP destination URL (client mode)")
	}
}

func (h *httpFactory) Describe(role Role) string {
	switch role {
	case RoleServer:
		if h.listenAddr == nil {
			return "http server (listen :8080)"
		}
		return fmt.Sprintf("http listen %s", *h.listenAddr)
	case RoleClient:
		if h.baseURL == nil || *h.baseURL == "" {
			return "http client (default http://127.0.0.1:8080)"
		}
		return fmt.Sprintf("http target %s", *h.baseURL)
	default:
		return "http transport"
	}
}

func (h *httpFactory) Open(ctx context.Context, role Role) (FrameConn, error) {
	switch role {
	case RoleServer:
		var addr string
		if h.listenAddr != nil {
			addr = *h.listenAddr
		}
		return newHTTPServerConn(ctx, addr)
	case RoleClient:
		var base string
		if h.baseURL != nil {
			base = *h.baseURL
		}
		return newHTTPClientConn(base), nil
	default:
		return nil, fmt.Errorf("invalid role %q for http transport", role)
	}
}
