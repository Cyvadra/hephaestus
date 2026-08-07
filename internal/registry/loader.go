package registry

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"
)

// Registry holds every loaded static config, keyed by name.
type Registry struct {
	Identities  map[string]Identity
	Impressions map[string]Impression
	ToolGroups  map[string]ToolGroup
	Concierges  map[string]Concierge
	Workflows   map[string]Workflow
	Jobs        map[string]Job
}

type fileKind struct {
	prefix string
	ext    string // required extension, without leading dot
}

// loader describes how one config kind is decoded and registered. Keeping
// the six kinds as data instead of a switch makes adding a kind a one-line
// table entry.
type loader[T any] struct {
	kind   fileKind
	decode func(path string, v any) error
	dest   func(*Registry) map[string]T
	name   func(T) string
	extra  func(filename string, v *T) error
}

// kindLoader pairs a kind's file signature with its load step, so Load can
// match filenames against a homogeneous slice.
type kindLoader struct {
	kind fileKind
	load func(reg *Registry, filename, path, expectedName string) error
}

// loadInto builds the load step for a kind: decode, check the name matches
// the filename, run kind-specific validation, then register (rejecting
// duplicates).
func loadInto[T any](l loader[T]) kindLoader {
	return kindLoader{
		kind: l.kind,
		load: func(reg *Registry, filename, path, expectedName string) error {
			var v T
			if err := l.decode(path, &v); err != nil {
				return err
			}
			if err := checkName(filename, expectedName, l.name(v)); err != nil {
				return err
			}
			if l.extra != nil {
				if err := l.extra(filename, &v); err != nil {
					return err
				}
			}
			dest := l.dest(reg)
			name := l.name(v)
			if _, dup := dest[name]; dup {
				return fmt.Errorf("registry: duplicate %s name %q (file %s)", strings.TrimSuffix(l.kind.prefix, "-"), name, filename)
			}
			dest[name] = v
			return nil
		},
	}
}

// loaders is the ordered set of config kinds Load recognizes.
var loaders = []kindLoader{
	loadInto(loader[Identity]{
		kind:   fileKind{prefix: "identity-", ext: "toml"},
		decode: decodeTOML,
		dest:   func(r *Registry) map[string]Identity { return r.Identities },
		name:   func(v Identity) string { return v.Name },
		extra: func(filename string, v *Identity) error {
			if v.SystemPrompt == "" {
				v.SystemPrompt = DefaultSystemPrompt
			}
			if err := validateReasoningEffort(v.Name, v.ReasoningEffort); err != nil {
				return err
			}
			if v.ContextWindowTokens <= 0 {
				return fmt.Errorf("registry: identity %q: context_window_tokens must be positive", v.Name)
			}
			return nil
		},
	}),
	loadInto(loader[Impression]{
		kind:   fileKind{prefix: "impression-", ext: "toml"},
		decode: decodeTOML,
		dest:   func(r *Registry) map[string]Impression { return r.Impressions },
		name:   func(v Impression) string { return v.Name },
	}),
	loadInto(loader[ToolGroup]{
		kind:   fileKind{prefix: "toolgroup-", ext: "yaml"},
		decode: decodeYAML,
		dest:   func(r *Registry) map[string]ToolGroup { return r.ToolGroups },
		name:   func(v ToolGroup) string { return v.Name },
	}),
	loadInto(loader[Concierge]{
		kind:   fileKind{prefix: "concierge-", ext: "yaml"},
		decode: decodeYAML,
		dest:   func(r *Registry) map[string]Concierge { return r.Concierges },
		name:   func(v Concierge) string { return v.Name },
	}),
	loadInto(loader[Workflow]{
		kind:   fileKind{prefix: "workflow-", ext: "yaml"},
		decode: decodeYAML,
		dest:   func(r *Registry) map[string]Workflow { return r.Workflows },
		name:   func(v Workflow) string { return v.Name },
		extra: func(filename string, v *Workflow) error {
			if len(v.Name) < 10 || strings.ContainsAny(v.Name, " \t") {
				return fmt.Errorf("registry: workflow name %q must be a slug of at least 10 chars with no spaces (file %s)", v.Name, filename)
			}
			return nil
		},
	}),
	loadInto(loader[Job]{
		kind:   fileKind{prefix: "job-", ext: "yaml"},
		decode: decodeYAML,
		dest:   func(r *Registry) map[string]Job { return r.Jobs },
		name:   func(v Job) string { return v.Name },
	}),
}

// Load scans dir (non-recursively) and parses every recognized config file.
// It returns an error on the first malformed file, duplicate name, or
// filename/name mismatch, since these represent broken developer config.
func Load(dir string) (*Registry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("registry: read config dir %q: %w", dir, err)
	}

	reg := &Registry{
		Identities:  map[string]Identity{},
		Impressions: map[string]Impression{},
		ToolGroups:  map[string]ToolGroup{},
		Concierges:  map[string]Concierge{},
		Workflows:   map[string]Workflow{},
		Jobs:        map[string]Job{},
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		filename := entry.Name()
		lower := strings.ToLower(filename)
		for _, kl := range loaders {
			if !strings.HasPrefix(lower, kl.kind.prefix) || !strings.HasSuffix(lower, "."+kl.kind.ext) {
				continue
			}
			expectedName := strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(filename, kl.kind.prefix), "."+kl.kind.ext))
			if err := kl.load(reg, filename, filepath.Join(dir, filename), expectedName); err != nil {
				return nil, err
			}
			break // a filename matches at most one kind
		}
	}

	return reg, nil
}

func checkName(filename, expectedName, actualName string) error {
	if actualName == "" {
		return fmt.Errorf("registry: %s: name field must not be empty", filename)
	}
	if strings.ToLower(actualName) != expectedName {
		return fmt.Errorf("registry: %s: name field %q does not match filename-derived name %q", filename, actualName, expectedName)
	}
	return nil
}

func validateReasoningEffort(identityName, effort string) error {
	switch effort {
	case "", ReasoningNone, ReasoningLow, ReasoningHigh, ReasoningMax:
		return nil
	default:
		return fmt.Errorf("registry: identity %q: invalid reasoning_effort %q", identityName, effort)
	}
}

func decodeTOML(path string, v any) error {
	meta, err := toml.DecodeFile(path, v)
	if err != nil {
		return fmt.Errorf("registry: decode %s: %w", path, err)
	}
	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		return fmt.Errorf("registry: decode %s: unknown field %q", path, undecoded[0])
	}
	return nil
}

func decodeYAML(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("registry: read %s: %w", path, err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(v); err != nil {
		return fmt.Errorf("registry: decode %s: %w", path, err)
	}
	return nil
}
