package registry

import (
	"errors"
	"fmt"
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
			return nil
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
