package registry

import (
	"fmt"
	"sync/atomic"

	"gorm.io/gorm"
)

// Store provides lock-free access to the immutable Registry snapshot used by
// runtime requests. Published registries must not be mutated.
type Store struct {
	current atomic.Pointer[Registry]
}

func NewStore(initial *Registry) *Store {
	store := &Store{}
	store.Publish(initial)
	return store
}

// Current returns the currently active Registry snapshot.
func (s *Store) Current() *Registry {
	return s.current.Load()
}

// Publish atomically makes reg active for subsequent requests.
func (s *Store) Publish(reg *Registry) {
	if reg == nil {
		panic("registry: cannot publish nil registry")
	}
	s.current.Store(reg)
}

// Clone returns an independent registry map set. Config records are treated
// as immutable after loading, so their nested values do not need deep copies.
func (r *Registry) Clone() *Registry {
	return &Registry{
		Identities:  cloneMap(r.Identities),
		Impressions: cloneMap(r.Impressions),
		ToolGroups:  cloneMap(r.ToolGroups),
		Concierges:  cloneMap(r.Concierges),
		Workflows:   cloneMap(r.Workflows),
		Jobs:        cloneMap(r.Jobs),
	}
}

func cloneMap[T any](values map[string]T) map[string]T {
	copy := make(map[string]T, len(values))
	for name, value := range values {
		copy[name] = value
	}
	return copy
}

// LoadDatabase overlays persisted configuration onto static. A database row
// with the same name replaces the complete static record of that kind.
func LoadDatabase(db *gorm.DB, static *Registry) (*Registry, error) {
	if static == nil {
		return nil, fmt.Errorf("registry: static registry is required")
	}
	reg := static.Clone()

	if err := loadDatabaseInto(db, reg); err != nil {
		return nil, err
	}
	return reg, nil
}

func loadDatabaseInto(db *gorm.DB, reg *Registry) error {
	var identities []Identity
	if err := db.Find(&identities).Error; err != nil {
		return fmt.Errorf("registry: load database identities: %w", err)
	}
	for index := range identities {
		if err := validatePersistedName(KindIdentity, identities[index].Name); err != nil {
			return err
		}
		if err := normalizeIdentity(&identities[index]); err != nil {
			return err
		}
		reg.Identities[identities[index].Name] = identities[index]
	}

	var impressions []Impression
	if err := db.Find(&impressions).Error; err != nil {
		return fmt.Errorf("registry: load database impressions: %w", err)
	}
	for _, value := range impressions {
		if err := validatePersistedName(KindImpression, value.Name); err != nil {
			return err
		}
		reg.Impressions[value.Name] = value
	}

	var toolGroups []ToolGroup
	if err := db.Find(&toolGroups).Error; err != nil {
		return fmt.Errorf("registry: load database tool groups: %w", err)
	}
	for _, value := range toolGroups {
		if err := validatePersistedName(KindToolGroup, value.Name); err != nil {
			return err
		}
		reg.ToolGroups[value.Name] = value
	}

	var concierges []Concierge
	if err := db.Find(&concierges).Error; err != nil {
		return fmt.Errorf("registry: load database concierges: %w", err)
	}
	for index := range concierges {
		if err := validatePersistedName(KindConcierge, concierges[index].Name); err != nil {
			return err
		}
		if err := normalizeConcierge(&concierges[index]); err != nil {
			return err
		}
		reg.Concierges[concierges[index].Name] = concierges[index]
	}

	var workflows []Workflow
	if err := db.Find(&workflows).Error; err != nil {
		return fmt.Errorf("registry: load database workflows: %w", err)
	}
	for index := range workflows {
		if err := validatePersistedName(KindWorkflow, workflows[index].Name); err != nil {
			return err
		}
		if err := normalizeWorkflow(&workflows[index]); err != nil {
			return err
		}
		reg.Workflows[workflows[index].Name] = workflows[index]
	}

	var jobs []Job
	if err := db.Find(&jobs).Error; err != nil {
		return fmt.Errorf("registry: load database jobs: %w", err)
	}
	for _, value := range jobs {
		if err := validatePersistedName(KindJob, value.Name); err != nil {
			return err
		}
		reg.Jobs[value.Name] = value
	}

	return nil
}

func validatePersistedName(kind Kind, name string) error {
	if name == "" {
		return fmt.Errorf("registry: database %s name must not be empty", kind)
	}
	return nil
}
