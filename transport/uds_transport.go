package transport

import (
	"context"
	"flag"
	"fmt"
)

type udsFactory struct {
	path *string
}

func init() {
	Register(&udsFactory{})
}

func (u *udsFactory) Name() string { return "uds" }

func (u *udsFactory) AddFlags(fs *flag.FlagSet) {
	if u.path == nil {
		u.path = stringFlag(fs, "path", "/tmp/transporter.sock", "Unix domain socket path")
	}
}

func (u *udsFactory) Describe(role Role) string {
	return "uds path " + flagString(u.path)
}

func (u *udsFactory) Open(ctx context.Context, role Role) (FrameConn, error) {
	path := flagString(u.path)
	if path == "" {
		path = "/tmp/transporter.sock"
	}
	switch role {
	case RoleServer:
		return newUDSServerConn(ctx, path)
	case RoleClient:
		return newUDSClientConn(path)
	default:
		return nil, fmt.Errorf("invalid role %q for uds transport", role)
	}
}
