package transport

import (
	"flag"
	"sync"
)

var flagCache sync.Map

func stringFlag(fs *flag.FlagSet, name, def, usage string) *string {
	if existing, ok := flagCache.Load(name); ok {
		return existing.(*string)
	}
	ptr := fs.String(name, def, usage)
	flagCache.Store(name, ptr)
	return ptr
}

func flagString(ptr *string) string {
	if ptr == nil {
		return ""
	}
	return *ptr
}
