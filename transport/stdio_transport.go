package transport

import (
	"context"
	"flag"
)

type stdioFactory struct{}

func init() {
	Register(&stdioFactory{})
}

func (s *stdioFactory) Name() string { return "stdio" }

func (s *stdioFactory) AddFlags(_ *flag.FlagSet) {}

func (s *stdioFactory) Describe(role Role) string {
	return "stdio (stdin/stdout pipes)"
}

func (s *stdioFactory) Open(ctx context.Context, role Role) (FrameConn, error) {
	return newStdIOConn(ctx, role), nil
}
