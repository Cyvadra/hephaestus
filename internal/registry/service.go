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

func NewService(db *gorm.DB, static *Registry, knownTools, knownPlugins map[string]bool) (*Service, error) {
	if db == nil {
		return nil, fmt.Errorf("registry: database is required")
	}
	if static == nil {
		return nil, fmt.Errorf("registry: static registry is required")
	}
	return &Service{
		db:           db,
		static:       static.Clone(),
		knownTools:   cloneMap(knownTools),
		knownPlugins: cloneMap(knownPlugins),
	}, nil
}

// Catalog returns names from the registry that would become active after a
// restart, plus names registered directly by the host application.
func (s *Service) Catalog() (Catalog, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	reg, err := LoadDatabase(s.db, s.static)
	if err != nil {
		return Catalog{}, err
	}
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
	value, err := newValue(kind)
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
	if _, err := newValue(kind); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.db.Transaction(func(tx *gorm.DB) error {
		result := tx.Where("name = ?", name).Delete(modelForKind(kind))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrNotFound
		}
		return s.validateFuture(tx)
	})
}

func (s *Service) write(kind Kind, value any, replace bool) error {
	if err := normalizeValue(kind, value); err != nil {
		return err
	}
	name, err := valueName(kind, value)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.db.Transaction(func(tx *gorm.DB) error {
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
		return s.validateFuture(tx)
	})
}

func (s *Service) validateFuture(tx *gorm.DB) error {
	reg, err := LoadDatabase(tx, s.static)
	if err != nil {
		return err
	}
	if err := reg.Validate(s.knownTools, s.knownPlugins); err != nil {
		return fmt.Errorf("%w: %v", ErrConflict, err)
	}
	return nil
}

func newValue(kind Kind) (any, error) {
	switch kind {
	case KindIdentity:
		return &Identity{}, nil
	case KindImpression:
		return &Impression{}, nil
	case KindToolGroup:
		return &ToolGroup{}, nil
	case KindConcierge:
		return &Concierge{}, nil
	case KindWorkflow:
		return &Workflow{}, nil
	case KindJob:
		return &Job{}, nil
	default:
		return nil, ErrInvalidKind
	}
}

func modelForKind(kind Kind) any {
	value, _ := newValue(kind)
	return value
}

func normalizeValue(kind Kind, value any) error {
	switch typed := value.(type) {
	case *Identity:
		if kind != KindIdentity {
			break
		}
		return normalizeIdentity(typed)
	case *Impression:
		if kind == KindImpression {
			return nil
		}
	case *ToolGroup:
		if kind == KindToolGroup {
			return nil
		}
	case *Concierge:
		if kind == KindConcierge {
			return normalizeConcierge(typed)
		}
	case *Workflow:
		if kind == KindWorkflow {
			return normalizeWorkflow(typed)
		}
	case *Job:
		if kind == KindJob {
			return nil
		}
	}
	return fmt.Errorf("registry: %w %q payload", ErrInvalidKind, kind)
}

func valueName(kind Kind, value any) (string, error) {
	var name string
	switch typed := value.(type) {
	case *Identity:
		name = typed.Name
	case *Impression:
		name = typed.Name
	case *ToolGroup:
		name = typed.Name
	case *Concierge:
		name = typed.Name
	case *Workflow:
		name = typed.Name
	case *Job:
		name = typed.Name
	default:
		return "", fmt.Errorf("registry: %w %q payload", ErrInvalidKind, kind)
	}
	if name == "" {
		return "", fmt.Errorf("registry: %s name must not be empty", kind)
	}
	return name, nil
}
