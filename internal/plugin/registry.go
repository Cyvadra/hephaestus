package plugin

import (
	"context"

	"github.com/Cyvadra/hephaestus/internal/notify"
)

// Registry holds every Plugin the platform knows about, keyed by name.
type Registry struct {
	byName map[string]Plugin
	notify *notify.Notifier
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

// KnownNames returns the set of registered plugin names, for use by
// registry.Registry.Validate.
func (r *Registry) KnownNames() map[string]bool {
	out := make(map[string]bool, len(r.byName))
	for name := range r.byName {
		out[name] = true
	}
	return out
}

// Run executes the named plugins, in order, for a single hook/phase. Each
// plugin's own Timeout bounds its execution; a plugin that errors or times
// out is skipped and reported, and the next plugin receives the turn state
// as left by the last plugin that succeeded.
func (r *Registry) Run(ctx context.Context, names []string, hook Hook, phase Phase, turn TurnContext) TurnContext {
	for _, name := range names {
		p, ok := r.byName[name]
		if !ok {
			r.notify.Warn("plugin: session config references unknown plugin %q", name)
			continue
		}

		pluginCtx, cancel := context.WithTimeout(ctx, p.Timeout())
		next, err := p.Handle(pluginCtx, hook, phase, turn)
		cancel()

		if err != nil {
			r.notify.Warn("plugin %q failed at %s/%s: %v", name, hook, phase, err)
			continue
		}
		turn = next
	}
	return turn
}
