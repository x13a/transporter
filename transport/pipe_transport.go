//go:build unix

package transport

import (
	"context"
	"errors"
	"flag"
	"fmt"
)

type pipeFactory struct {
	inPath  *string
	outPath *string
}

func init() {
	Register(&pipeFactory{})
}

func (p *pipeFactory) Name() string { return "pipe" }

func (p *pipeFactory) AddFlags(fs *flag.FlagSet) {
	if p.inPath == nil {
		p.inPath = stringFlag(fs, "in", "", "named pipe (FIFO) to read frames from")
	}
	if p.outPath == nil {
		p.outPath = stringFlag(fs, "out", "", "named pipe (FIFO) to write frames to")
	}
}

func (p *pipeFactory) Describe(role Role) string {
	return fmt.Sprintf("pipe in=%s out=%s", flagString(p.inPath), flagString(p.outPath))
}

func (p *pipeFactory) Open(ctx context.Context, role Role) (FrameConn, error) {
	in := flagString(p.inPath)
	out := flagString(p.outPath)
	if in == "" || out == "" {
		return nil, errors.New("pipe transport requires -in and -out")
	}
	return newPipeConn(in, out)
}
