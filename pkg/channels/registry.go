package channels

import (
	"fmt"
	"sort"
	"sync"
)

// Factory creates a configured channel implementation.
type Factory func(config any) (Channel, error)

var (
	factoriesMu sync.RWMutex
	factories   = map[string]Factory{}
)

// RegisterFactory registers an implementation, normally from its init func.
func RegisterFactory(name string, factory Factory) {
	factoriesMu.Lock()
	defer factoriesMu.Unlock()
	if name == "" || factory == nil {
		panic("channels: invalid factory registration")
	}
	if _, exists := factories[name]; exists {
		panic("channels: duplicate factory " + name)
	}
	factories[name] = factory
}

// New creates a registered channel by name.
func New(name string, config any) (Channel, error) {
	factoriesMu.RLock()
	factory := factories[name]
	factoriesMu.RUnlock()
	if factory == nil {
		return nil, fmt.Errorf("channels: unknown channel %q", name)
	}
	return factory(config)
}

// Registered returns registered channel names in stable order.
func Registered() []string {
	factoriesMu.RLock()
	defer factoriesMu.RUnlock()
	names := make([]string, 0, len(factories))
	for name := range factories {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
