package transport

import (
	"context"
	"flag"
	"fmt"
)

type fileFactory struct {
	dir *string
}

func init() {
	Register(&fileFactory{})
}

func (f *fileFactory) Name() string { return "file" }

func (f *fileFactory) AddFlags(fs *flag.FlagSet) {
	if f.dir == nil {
		f.dir = stringFlag(fs, "dir", "", "shared directory used by file transport")
	}
}

func (f *fileFactory) Describe(role Role) string {
	if f.dir == nil || *f.dir == "" {
		return fmt.Sprintf("file transport: auto temp dir (%s role)", role)
	}
	return fmt.Sprintf("file dir %s (%s role)", *f.dir, role)
}

func (f *fileFactory) Open(ctx context.Context, role Role) (FrameConn, error) {
	var dir string
	if f.dir != nil {
		dir = *f.dir
	}
	return newFileConn(dir, role)
}
