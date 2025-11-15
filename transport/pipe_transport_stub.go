//go:build !unix

package transport

import (
	"context"
	"errors"
	"flag"
)

type pipeFactory struct{}

func init() {}

func (p *pipeFactory) Name() string              { return "pipe" }
func (p *pipeFactory) AddFlags(_ *flag.FlagSet)  {}
func (p *pipeFactory) Describe(role Role) string { return "pipe transport unavailable" }
func (p *pipeFactory) Open(ctx context.Context, role Role) (FrameConn, error) {
	return nil, errors.New("pipe transport is only supported on unix")
}
