package transport

import (
	"context"
	"flag"
	"fmt"
	"sort"
	"sync"

	"transporter/proto"
)

// Role identifies whether a transport instance is used by the client (Socks-side)
// or by the server (forwarder). Some transports need to know this to expose the
// correct endpoints.
type Role string

const (
	RoleClient Role = "client"
	RoleServer Role = "server"
)

// FrameConn is a bidirectional channel for encoded frames.
type FrameConn interface {
	Send(ctx context.Context, frame *proto.Frame) error
	Receive(ctx context.Context) (*proto.Frame, error)
	Close() error
}

// Factory describes how a transport is configured via command-line flags and opened.
type Factory interface {
	Name() string
	AddFlags(fs *flag.FlagSet)
	Describe(role Role) string
	Open(ctx context.Context, role Role) (FrameConn, error)
}

var (
	factoriesMu sync.RWMutex
	factories   = map[string]Factory{}
	order       []string
)

// Register adds a transport factory to the registry. Intended to be called from init().
func Register(factory Factory) {
	factoriesMu.Lock()
	defer factoriesMu.Unlock()
	name := factory.Name()
	if name == "" {
		panic("transport factory must have a name")
	}
	if _, exists := factories[name]; exists {
		panic("transport factory already registered: " + name)
	}
	factories[name] = factory
	order = append(order, name)
}

// AddFlags asks every registered transport to expose its specific flags.
func AddFlags(fs *flag.FlagSet) {
	factoriesMu.RLock()
	defer factoriesMu.RUnlock()
	for _, name := range order {
		factories[name].AddFlags(fs)
	}
}

// Names returns the registered transports in registration order.
func Names() []string {
	factoriesMu.RLock()
	defer factoriesMu.RUnlock()
	names := make([]string, len(order))
	copy(names, order)
	return names
}

// DefaultName returns the first registered transport, or empty string if none exist.
func DefaultName() string {
	factoriesMu.RLock()
	defer factoriesMu.RUnlock()
	if len(order) == 0 {
		return ""
	}
	return order[0]
}

// Open instantiates the requested transport based on the provided role and name.
func Open(ctx context.Context, role Role, name string) (FrameConn, error) {
	factoriesMu.RLock()
	factory, ok := factories[name]
	factoriesMu.RUnlock()
	if !ok {
		names := Names()
		sort.Strings(names)
		return nil, fmt.Errorf("unsupported transport %q (available: %v)", name, names)
	}
	return factory.Open(ctx, role)
}

// Describe returns a human-friendly string for logging.
func Describe(name string, role Role) string {
	factoriesMu.RLock()
	defer factoriesMu.RUnlock()
	if factory, ok := factories[name]; ok {
		return factory.Describe(role)
	}
	return ""
}
