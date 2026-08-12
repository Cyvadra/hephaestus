package registry

import (
	"errors"
	"fmt"
	"sort"
	"sync"

	"gorm.io/gorm"
)

// Kind identifies a database-persisted configuration type.
type Kind string

const (
	KindIdentity   Kind = "identities"
	KindImpression Kind = "impressions"
	KindToolGroup  Kind = "tool-groups"
	KindConcierge  Kind = "concierges"
	KindWorkflow   Kind = "workflows"
	KindJob        Kind = "jobs"
)

var (
	ErrInvalidKind = errors.New("registry: invalid configuration kind")
	ErrNotFound    = errors.New("registry: configuration not found")
	ErrExists      = errors.New("registry: configuration already exists")
	ErrConflict    = errors.New("registry: configuration conflict")
)

// Service manages database-backed configuration. Static is never modified;
// every write validates the registry that will be active after a restart.
type Service struct {
	db           *gorm.DB
	static       *Registry
	store        *Store
	knownTools   map[string]bool
	knownPlugins map[string]bool
	mu           sync.Mutex
}

// Catalog is the complete set of names available for configuration references.
// It includes the static baseline, database overlays, and registered tools/plugins.
type Catalog struct {
	Identities  []string `json:"identities"`
	Impressions []string `json:"impressions"`
	ToolGroups  []string `json:"tool_groups"`
	Concierges  []string `json:"concierges"`
	Workflows   []string `json:"workflows"`
	Jobs        []string `json:"jobs"`
	Tools       []string `json:"tools"`
	Plugins     []string `json:"plugins"`
}

func NewService(db *gorm.DB, static *Registry, store *Store, knownTools, knownPlugins map[string]bool) (*Service, error) {
	if db == nil {
		return nil, fmt.Errorf("registry: database is required")
	}
	if static == nil {
		return nil, fmt.Errorf("registry: static registry is required")
	}
	if store == nil {
		return nil, fmt.Errorf("registry: runtime store is required")
	}
	return &Service{
		db:           db,
		static:       static.Clone(),
		store:        store,
		knownTools:   cloneMap(knownTools),
		knownPlugins: cloneMap(knownPlugins),
	}, nil
}

// Catalog returns names from the active registry snapshot, plus names
// registered directly by the host application.
func (s *Service) Catalog() (Catalog, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	reg := s.store.Current()
	return Catalog{
		Identities:  sortedMapKeys(reg.Identities),
		Impressions: sortedMapKeys(reg.Impressions),
		ToolGroups:  sortedMapKeys(reg.ToolGroups),
		Concierges:  sortedMapKeys(reg.Concierges),
		Workflows:   sortedMapKeys(reg.Workflows),
		Jobs:        sortedMapKeys(reg.Jobs),
		Tools:       sortedBoolKeys(s.knownTools),
		Plugins:     sortedBoolKeys(s.knownPlugins),
	}, nil
}

func sortedMapKeys[T any](values map[string]T) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func sortedBoolKeys(values map[string]bool) []string {
	names := make([]string, 0, len(values))
	for name, known := range values {
		if known {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func (s *Service) List(kind Kind) (any, error) {
	switch kind {
	case KindIdentity:
		var values []Identity
		return values, s.db.Order("name").Find(&values).Error
	case KindImpression:
		var values []Impression
		return values, s.db.Order("name").Find(&values).Error
	case KindToolGroup:
		var values []ToolGroup
		return values, s.db.Order("name").Find(&values).Error
	case KindConcierge:
		var values []Concierge
		return values, s.db.Order("name").Find(&values).Error
	case KindWorkflow:
		var values []Workflow
		return values, s.db.Order("name").Find(&values).Error
	case KindJob:
		var values []Job
		return values, s.db.Order("name").Find(&values).Error
	default:
		return nil, ErrInvalidKind
	}
}

func (s *Service) Get(kind Kind, name string) (any, error) {
	value, err := NewValue(kind)
	if err != nil {
		return nil, err
	}
	if err := s.db.First(value, "name = ?", name).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return value, nil
}

func (s *Service) Create(kind Kind, value any) error {
	return s.write(kind, value, false)
}

func (s *Service) Replace(kind Kind, value any) error {
	return s.write(kind, value, true)
}

func (s *Service) Delete(kind Kind, name string) error {
	if _, err := NewValue(kind); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var candidate *Registry
	err := s.db.Transaction(func(tx *gorm.DB) error {
		result := tx.Where("name = ?", name).Delete(modelForKind(kind))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrNotFound
		}
		var err error
		candidate, err = s.validatedRegistry(tx)
		return err
	})
	if err != nil {
		return err
	}
	s.store.Publish(candidate)
	return nil
}

func (s *Service) write(kind Kind, value any, replace bool) error {
	if err := normalizeValue(kind, value); err != nil {
		return err
	}
	name, err := ValueName(kind, value)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var candidate *Registry
	err = s.db.Transaction(func(tx *gorm.DB) error {
		model := modelForKind(kind)
		var count int64
		if err := tx.Model(model).Where("name = ?", name).Count(&count).Error; err != nil {
			return err
		}
		if !replace && count > 0 {
			return ErrExists
		}
		if replace && count == 0 {
			return ErrNotFound
		}
		if replace {
			if err := tx.Save(value).Error; err != nil {
				return err
			}
		} else if err := tx.Create(value).Error; err != nil {
			return err
		}
		candidate, err = s.validatedRegistry(tx)
		return err
	})
	if err != nil {
		return err
	}
	s.store.Publish(candidate)
	return nil
}

// DefaultIdentityName returns the alphabetically first identity name, used
// as the fallback when a session references an identity that no longer
// exists. Returns "" when the registry has no identities at all.
func (r *Registry) DefaultIdentityName() string {
	if len(r.Identities) == 0 {
		return ""
	}
	return sortedMapKeys(r.Identities)[0]
}

func (s *Service) validatedRegistry(tx *gorm.DB) (*Registry, error) {
	reg, err := LoadDatabase(tx, s.static)
	if err != nil {
		return nil, err
	}
	if err := reg.Validate(s.knownTools, s.knownPlugins); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrConflict, err)
	}
	return reg, nil
}

// kindDescriptor centralizes the per-kind behaviors (blank value, name
// extraction, normalization) that the HTTP handlers and the write/delete
// paths all need. Keeping them as data instead of parallel switches makes
// adding a configuration kind a single table entry.
type kindDescriptor struct {
	blank     func() any
	name      func(any) (string, bool)
	normalize func(any) error // nil means the kind needs no normalization
}

var kindDescriptors = map[Kind]kindDescriptor{
	KindIdentity: {
		blank: func() any { return &Identity{} },
		name: func(v any) (string, bool) {
			typed, ok := v.(*Identity)
			if !ok {
				return "", false
			}
			return typed.Name, true
		},
		normalize: func(v any) error {
			typed, ok := v.(*Identity)
			if !ok {
				return wrongPayload(KindIdentity)
			}
			return normalizeIdentity(typed)
		},
	},
	KindImpression: {
		blank: func() any { return &Impression{} },
		name: func(v any) (string, bool) {
			typed, ok := v.(*Impression)
			if !ok {
				return "", false
			}
			return typed.Name, true
		},
	},
	KindToolGroup: {
		blank: func() any { return &ToolGroup{} },
		name: func(v any) (string, bool) {
			typed, ok := v.(*ToolGroup)
			if !ok {
				return "", false
			}
			return typed.Name, true
		},
	},
	KindConcierge: {
		blank: func() any { return &Concierge{} },
		name: func(v any) (string, bool) {
			typed, ok := v.(*Concierge)
			if !ok {
				return "", false
			}
			return typed.Name, true
		},
		normalize: func(v any) error {
			typed, ok := v.(*Concierge)
			if !ok {
				return wrongPayload(KindConcierge)
			}
			return normalizeConcierge(typed)
		},
	},
	KindWorkflow: {
		blank: func() any { return &Workflow{} },
		name: func(v any) (string, bool) {
			typed, ok := v.(*Workflow)
			if !ok {
				return "", false
			}
			return typed.Name, true
		},
		normalize: func(v any) error {
			typed, ok := v.(*Workflow)
			if !ok {
				return wrongPayload(KindWorkflow)
			}
			return normalizeWorkflow(typed)
		},
	},
	KindJob: {
		blank: func() any { return &Job{} },
		name: func(v any) (string, bool) {
			typed, ok := v.(*Job)
			if !ok {
				return "", false
			}
			return typed.Name, true
		},
		normalize: func(v any) error {
			typed, ok := v.(*Job)
			if !ok {
				return wrongPayload(KindJob)
			}
			return normalizeJob(typed)
		},
	},
}

func wrongPayload(kind Kind) error {
	return fmt.Errorf("registry: %w %q payload", ErrInvalidKind, kind)
}

// NewValue returns a blank configuration value for kind, for decoding a
// request payload, or ErrInvalidKind.
func NewValue(kind Kind) (any, error) {
	desc, ok := kindDescriptors[kind]
	if !ok {
		return nil, ErrInvalidKind
	}
	return desc.blank(), nil
}

// ValueName extracts the persisted name from a configuration value of kind.
func ValueName(kind Kind, value any) (string, error) {
	desc, ok := kindDescriptors[kind]
	if !ok {
		return "", ErrInvalidKind
	}
	name, ok := desc.name(value)
	if !ok {
		return "", wrongPayload(kind)
	}
	if name == "" {
		return "", fmt.Errorf("registry: %s name must not be empty", kind)
	}
	return name, nil
}

func normalizeValue(kind Kind, value any) error {
	desc, ok := kindDescriptors[kind]
	if !ok {
		return ErrInvalidKind
	}
	if desc.normalize == nil {
		return nil
	}
	return desc.normalize(value)
}

func modelForKind(kind Kind) any {
	value, _ := NewValue(kind)
	return value
}
