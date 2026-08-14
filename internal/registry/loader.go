package registry

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

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
	Constants   map[string]Constant
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
	kind         fileKind
	registryKind Kind
	load         func(reg *Registry, filename, path, expectedName string) (any, error)
}

// loadInto builds the load step for a kind: decode, check the name matches
// the filename, run kind-specific validation, then register (rejecting
// duplicates).
func loadInto[T any](l loader[T]) kindLoader {
	return kindLoader{
		kind:         l.kind,
		registryKind: kindForPrefix(l.kind.prefix),
		load: func(reg *Registry, filename, path, expectedName string) (any, error) {
			var v T
			if err := l.decode(path, &v); err != nil {
				return nil, err
			}
			if err := checkName(filename, expectedName, l.name(v)); err != nil {
				return nil, err
			}
			if l.extra != nil {
				if err := l.extra(filename, &v); err != nil {
					return nil, err
				}
			}
			dest := l.dest(reg)
			name := l.name(v)
			if _, dup := dest[name]; dup {
				return nil, fmt.Errorf("registry: duplicate %s name %q (file %s)", strings.TrimSuffix(l.kind.prefix, "-"), name, filename)
			}
			dest[name] = v
			return &v, nil
		},
	}
}

func kindForPrefix(prefix string) Kind {
	switch prefix {
	case "identity-":
		return KindIdentity
	case "impression-":
		return KindImpression
	case "toolgroup-":
		return KindToolGroup
	case "concierge-":
		return KindConcierge
	case "workflow-":
		return KindWorkflow
	case "job-":
		return KindJob
	default:
		panic("registry: unknown loader prefix " + prefix)
	}
}

// Template is one normalized static configuration document and the metadata
// used to synchronize it into the database at startup.
type Template struct {
	Kind       Kind
	Name       string
	Path       string
	ModifiedAt time.Time
	Hash       string
	Value      any
}

// loaders is the ordered set of config kinds Load recognizes.
var loaders = []kindLoader{
	loadInto(loader[Identity]{
		kind:   fileKind{prefix: "identity-", ext: "toml"},
		decode: decodeTOML,
		dest:   func(r *Registry) map[string]Identity { return r.Identities },
		name:   func(v Identity) string { return v.Name },
		extra:  func(_ string, v *Identity) error { return normalizeIdentity(v) },
	}),
	loadInto(loader[Impression]{
		kind:   fileKind{prefix: "impression-", ext: "toml"},
		decode: decodeTOML,
		dest:   func(r *Registry) map[string]Impression { return r.Impressions },
		name:   func(v Impression) string { return v.Name },
		extra:  func(_ string, v *Impression) error { normalizeImpression(v); return nil },
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
		extra:  func(_ string, v *Concierge) error { return normalizeConcierge(v) },
	}),
	loadInto(loader[Workflow]{
		kind:   fileKind{prefix: "workflow-", ext: "yaml"},
		decode: decodeYAML,
		dest:   func(r *Registry) map[string]Workflow { return r.Workflows },
		name:   func(v Workflow) string { return v.Name },
		extra:  func(_ string, v *Workflow) error { return normalizeWorkflow(v) },
	}),
	loadInto(loader[Job]{
		kind:   fileKind{prefix: "job-", ext: "yaml"},
		decode: decodeYAML,
		dest:   func(r *Registry) map[string]Job { return r.Jobs },
		name:   func(v Job) string { return v.Name },
		extra:  func(_ string, v *Job) error { return normalizeJob(v) },
	}),
}

// Load scans dir (non-recursively) and parses every recognized config file.
// It returns an error on the first malformed file, duplicate name, or
// filename/name mismatch, since these represent broken developer config.
func Load(dir string) (*Registry, error) {
	reg, _, err := LoadTemplates(dir)
	return reg, err
}

// LoadTemplates loads the static defaults and records stable metadata for
// startup synchronization. Hashes describe normalized business fields only.
func LoadTemplates(dir string) (*Registry, []Template, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("registry: read config dir %q: %w", dir, err)
	}

	reg := &Registry{
		Identities:  map[string]Identity{},
		Impressions: map[string]Impression{},
		ToolGroups:  map[string]ToolGroup{},
		Concierges:  map[string]Concierge{},
		Workflows:   map[string]Workflow{},
		Jobs:        map[string]Job{},
		Constants:   map[string]Constant{},
	}
	templates := make([]Template, 0)

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
			path := filepath.Join(dir, filename)
			value, err := kl.load(reg, filename, path, expectedName)
			if err != nil {
				return nil, nil, err
			}
			info, err := entry.Info()
			if err != nil {
				return nil, nil, fmt.Errorf("registry: stat %s: %w", path, err)
			}
			hash, err := templateHash(value)
			if err != nil {
				return nil, nil, fmt.Errorf("registry: hash %s: %w", path, err)
			}
			templates = append(templates, Template{
				Kind:       kl.registryKind,
				Name:       expectedName,
				Path:       filename,
				ModifiedAt: info.ModTime(),
				Hash:       hash,
				Value:      value,
			})
			break // a filename matches at most one kind
		}
	}

	return reg, templates, nil
}

func templateHash(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
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

func normalizeIdentity(v *Identity) error {
	if v.SystemPrompt == "" {
		v.SystemPrompt = DefaultSystemPrompt
	}
	if v.InjectedMessages == nil {
		v.InjectedMessages = []Message{}
	}
	if err := validateReasoningEffort(v.Name, v.ReasoningEffort); err != nil {
		return err
	}
	if v.ContextWindowTokens <= 0 {
		return fmt.Errorf("registry: identity %q: context_window_tokens must be positive", v.Name)
	}
	return nil
}

func normalizeImpression(v *Impression) {
	if v.Messages == nil {
		v.Messages = []Message{}
	}
}

func normalizeConcierge(v *Concierge) error {
	if v.Nickname == "" {
		v.Nickname = v.Name
	}
	if len([]rune(v.Nickname)) > 20 {
		return fmt.Errorf("registry: concierge %q: nickname must not exceed 20 characters", v.Name)
	}
	return nil
}

func normalizeWorkflow(v *Workflow) error {
	if len(v.Name) < 10 || strings.ContainsAny(v.Name, " \t") {
		return fmt.Errorf("registry: workflow name %q must be a slug of at least 10 chars with no spaces", v.Name)
	}
	if len(v.Steps) == 0 {
		return fmt.Errorf("registry: workflow %q must have at least one step", v.Name)
	}
	for i, step := range v.Steps {
		if strings.TrimSpace(step) == "" {
			return fmt.Errorf("registry: workflow %q step %d is blank", v.Name, i)
		}
	}
	if _, err := CompileSchema(v.InputSchema); err != nil {
		return fmt.Errorf("registry: workflow %q input schema: %v", v.Name, err)
	}
	if _, err := CompileSchema(v.OutputSchema); err != nil {
		return fmt.Errorf("registry: workflow %q output schema: %v", v.Name, err)
	}
	return nil
}

func normalizeJob(v *Job) error {
	if strings.TrimSpace(v.Name) == "" {
		return fmt.Errorf("registry: job name must not be empty")
	}
	if v.MaxExecutionsPerDay <= 0 {
		return fmt.Errorf("registry: job %q: max_executions_per_day must be positive", v.Name)
	}
	if strings.TrimSpace(v.Trigger) == "" {
		return fmt.Errorf("registry: job %q: trigger must not be empty", v.Name)
	}
	if _, err := CompileTrigger(v.Trigger); err != nil {
		return fmt.Errorf("registry: job %q: %v", v.Name, err)
	}
	if len(v.Workflows) == 0 {
		return fmt.Errorf("registry: job %q: must bind at least one workflow", v.Name)
	}
	for i, binding := range v.Workflows {
		if strings.TrimSpace(binding.Workflow) == "" {
			return fmt.Errorf("registry: job %q: binding %d workflow must not be empty", v.Name, i)
		}
		if strings.TrimSpace(binding.Project) == "" {
			return fmt.Errorf("registry: job %q: binding %d (workflow %q) project must not be empty", v.Name, i, binding.Workflow)
		}
		if binding.MaxAttempts < 1 {
			return fmt.Errorf("registry: job %q: binding %d (workflow %q) max_attempts must be >= 1", v.Name, i, binding.Workflow)
		}
		if binding.RetryDelaySeconds < 0 {
			return fmt.Errorf("registry: job %q: binding %d (workflow %q) retry_delay_seconds must not be negative", v.Name, i, binding.Workflow)
		}
		if err := validatePlaceholders(binding.Input); err != nil {
			return fmt.Errorf("registry: job %q: binding %d (workflow %q): %v", v.Name, i, binding.Workflow, err)
		}
	}
	return nil
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
