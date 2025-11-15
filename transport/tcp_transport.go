package transport

import (
	"context"
	"flag"
	"fmt"
)

type tcpFactory struct {
	listenAddr  *string
	connectAddr *string
}

func init() {
	Register(&tcpFactory{})
}

func (t *tcpFactory) Name() string { return "tcp" }

func (t *tcpFactory) AddFlags(fs *flag.FlagSet) {
	if t.listenAddr == nil {
		t.listenAddr = stringFlag(fs, "bind", "127.0.0.1:9090", "TCP listen address (server mode)")
	}
	if t.connectAddr == nil {
		t.connectAddr = stringFlag(fs, "dest", "127.0.0.1:9090", "TCP connect address (client mode)")
	}
}

func (t *tcpFactory) Describe(role Role) string {
	switch role {
	case RoleServer:
		return "tcp listen " + flagString(t.listenAddr)
	case RoleClient:
		return "tcp connect " + flagString(t.connectAddr)
	default:
		return "tcp transport"
	}
}

func (t *tcpFactory) Open(ctx context.Context, role Role) (FrameConn, error) {
	switch role {
	case RoleServer:
		return newTCPServerConn(ctx, flagString(t.listenAddr))
	case RoleClient:
		return newTCPClientConn(flagString(t.connectAddr))
	default:
		return nil, fmt.Errorf("invalid role %q for tcp transport", role)
	}
}
