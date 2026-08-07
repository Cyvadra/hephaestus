package plugin

import (
	"context"
	"fmt"

	"github.com/Cyvadra/hephaestus/internal/notify"
)

// Registry holds every Plugin the platform knows about, keyed by name.
type Registry struct {
	byName       map[string]Plugin
	fixedPlugins []string
	notify       *notify.Notifier
}

// NewRegistry creates a Registry that reports failed/timed-out plugins to n.
func NewRegistry(n *notify.Notifier) *Registry {
	return &Registry{byName: map[string]Plugin{}, notify: n}
}

// Register adds a Plugin, panicking on duplicate names since that indicates
// a programming error in this platform's own plugin registrations.
func (r *Registry) Register(p Plugin) {
	if _, dup := r.byName[p.Name()]; dup {
		panic("plugin: duplicate plugin name " + p.Name())
	}
	r.byName[p.Name()] = p
}

// SetFixedPlugins configures the ordered Plugins that run for every session,
// independently of the session's mutable Plugin settings.
func (r *Registry) SetFixedPlugins(names []string) error {
	seen := make(map[string]bool, len(names))
	fixed := make([]string, 0, len(names))
	for _, name := range names {
		if !r.Has(name) {
			return fmt.Errorf("plugin: fixed plugin %q is not registered", name)
		}
		if !seen[name] {
			fixed = append(fixed, name)
			seen[name] = true
		}
	}
	r.fixedPlugins = fixed
	return nil
}

// KnownNames returns the set of registered plugin names, for use by
// registry.Registry.Validate.
func (r *Registry) KnownNames() map[string]bool {
	out := make(map[string]bool, len(r.byName))
	for name := range r.byName {
		out[name] = true
	}
	return out
}

// Has reports whether name is registered by the platform.
func (r *Registry) Has(name string) bool {
	_, ok := r.byName[name]
	return ok
}

// IsFixed reports whether name is configured to run for every session.
func (r *Registry) IsFixed(name string) bool {
	for _, fixed := range r.fixedPlugins {
		if fixed == name {
			return true
		}
	}
	return false
}

// Run executes the named plugins, in order, for a single hook/phase. Each
// plugin's own Timeout bounds its execution; a plugin that errors or times
// out is skipped and reported, and the next plugin receives the turn state
// as left by the last plugin that succeeded.
//
// Handle runs in its own goroutine per invocation, raced against the
// plugin's Timeout via pluginCtx.Done(): a plugin that ignores ctx
// cancellation (which this platform must not assume is possible to force,
// per design) is abandoned rather than allowed to stall the pipeline, and
// each invocation gets its own cloned Messages/Metadata so a leaked,
// still-running goroutine can only mutate its own copy.
func (r *Registry) Run(ctx context.Context, names []string, hook Hook, phase Phase, turn TurnContext) TurnContext {
	for _, name := range r.executionNames(names) {
		p, ok := r.byName[name]
		if !ok {
			r.notify.Warn("plugin: session config references unknown plugin %q", name)
			continue
		}

		pluginCtx, cancel := context.WithTimeout(ctx, p.Timeout())
		input := turn.clone()
		resultCh := make(chan TurnContext, 1)
		errCh := make(chan error, 1)
		go func() {
			next, err := p.Handle(pluginCtx, hook, phase, input)
			if err != nil {
				errCh <- err
				return
			}
			resultCh <- next
		}()

		select {
		case next := <-resultCh:
			turn = next
		case err := <-errCh:
			r.notify.Warn("plugin %q failed at %s/%s: %v", name, hook, phase, err)
		case <-pluginCtx.Done():
			r.notify.Warn("plugin %q exceeded its %s timeout at %s/%s", name, p.Timeout(), hook, phase)
		}
		cancel()
	}
	return turn
}

func (r *Registry) executionNames(sessionPlugins []string) []string {
	if len(r.fixedPlugins) == 0 {
		return sessionPlugins
	}
	names := make([]string, 0, len(r.fixedPlugins)+len(sessionPlugins))
	seen := make(map[string]bool, cap(names))
	for _, name := range r.fixedPlugins {
		if !seen[name] {
			names = append(names, name)
			seen[name] = true
		}
	}
	for _, name := range sessionPlugins {
		if !seen[name] {
			names = append(names, name)
			seen[name] = true
		}
	}
	return names
}
