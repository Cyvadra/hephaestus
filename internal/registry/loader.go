package registry

import (
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

var kinds = []fileKind{
	{prefix: "identity-", ext: "toml"},
	{prefix: "impression-", ext: "toml"},
	{prefix: "toolgroup-", ext: "yaml"},
	{prefix: "concierge-", ext: "yaml"},
	{prefix: "workflow-", ext: "yaml"},
	{prefix: "job-", ext: "yaml"},
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
		kind, ok := matchKind(filename)
		if !ok {
			continue // ignore files that don't match any known prefix
		}

		expectedName := strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(filename, kind.prefix), "."+kind.ext))
		path := filepath.Join(dir, filename)

		switch kind.prefix {
		case "identity-":
			var v Identity
			if err := decodeTOML(path, &v); err != nil {
				return nil, err
			}
			if err := checkName(filename, expectedName, v.Name); err != nil {
				return nil, err
			}
			if v.SystemPrompt == "" {
				v.SystemPrompt = DefaultSystemPrompt
			}
			if err := validateReasoningEffort(v.Name, v.ReasoningEffort); err != nil {
				return nil, err
			}
			if _, dup := reg.Identities[v.Name]; dup {
				return nil, fmt.Errorf("registry: duplicate identity name %q (file %s)", v.Name, filename)
			}
			reg.Identities[v.Name] = v

		case "impression-":
			var v Impression
			if err := decodeTOML(path, &v); err != nil {
				return nil, err
			}
			if err := checkName(filename, expectedName, v.Name); err != nil {
				return nil, err
			}
			if _, dup := reg.Impressions[v.Name]; dup {
				return nil, fmt.Errorf("registry: duplicate impression name %q (file %s)", v.Name, filename)
			}
			reg.Impressions[v.Name] = v

		case "toolgroup-":
			var v ToolGroup
			if err := decodeYAML(path, &v); err != nil {
				return nil, err
			}
			if err := checkName(filename, expectedName, v.Name); err != nil {
				return nil, err
			}
			if _, dup := reg.ToolGroups[v.Name]; dup {
				return nil, fmt.Errorf("registry: duplicate tool group name %q (file %s)", v.Name, filename)
			}
			reg.ToolGroups[v.Name] = v

		case "concierge-":
			var v Concierge
			if err := decodeYAML(path, &v); err != nil {
				return nil, err
			}
			if err := checkName(filename, expectedName, v.Name); err != nil {
				return nil, err
			}
			if _, dup := reg.Concierges[v.Name]; dup {
				return nil, fmt.Errorf("registry: duplicate concierge name %q (file %s)", v.Name, filename)
			}
			reg.Concierges[v.Name] = v

		case "workflow-":
			var v Workflow
			if err := decodeYAML(path, &v); err != nil {
				return nil, err
			}
			if err := checkName(filename, expectedName, v.Name); err != nil {
				return nil, err
			}
			if len(v.Name) < 10 || strings.ContainsAny(v.Name, " \t") {
				return nil, fmt.Errorf("registry: workflow name %q must be a slug of at least 10 chars with no spaces (file %s)", v.Name, filename)
			}
			if _, dup := reg.Workflows[v.Name]; dup {
				return nil, fmt.Errorf("registry: duplicate workflow name %q (file %s)", v.Name, filename)
			}
			reg.Workflows[v.Name] = v

		case "job-":
			var v Job
			if err := decodeYAML(path, &v); err != nil {
				return nil, err
			}
			if err := checkName(filename, expectedName, v.Name); err != nil {
				return nil, err
			}
			if _, dup := reg.Jobs[v.Name]; dup {
				return nil, fmt.Errorf("registry: duplicate job name %q (file %s)", v.Name, filename)
			}
			reg.Jobs[v.Name] = v
		}
	}

	return reg, nil
}

func matchKind(filename string) (fileKind, bool) {
	lower := strings.ToLower(filename)
	for _, k := range kinds {
		if strings.HasPrefix(lower, k.prefix) && strings.HasSuffix(lower, "."+k.ext) {
			return k, true
		}
	}
	return fileKind{}, false
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
	if _, err := toml.DecodeFile(path, v); err != nil {
		return fmt.Errorf("registry: decode %s: %w", path, err)
	}
	return nil
}

func decodeYAML(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("registry: read %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, v); err != nil {
		return fmt.Errorf("registry: decode %s: %w", path, err)
	}
	return nil
}
