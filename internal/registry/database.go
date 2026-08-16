package registry

import (
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"gorm.io/gorm"
)

// TemplateState records the last static template observed for one database
// configuration. It is internal synchronization metadata, not user config.
type TemplateState struct {
	Kind           Kind   `gorm:"primaryKey;size:32"`
	Name           string `gorm:"primaryKey;size:255"`
	SourcePath     string `gorm:"size:1024"`
	ContentHash    string `gorm:"size:64"`
	FileModifiedAt time.Time
	RecordTimestamps
}

// SyncResult summarizes startup template synchronization decisions.
type SyncResult struct {
	Created   int
	Updated   int
	Preserved int
}

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
		Constants:   cloneMap(r.Constants),
	}
}

func cloneMap[T any](values map[string]T) map[string]T {
	copy := make(map[string]T, len(values))
	for name, value := range values {
		copy[name] = value
	}
	return copy
}

// LoadDatabase builds the complete runtime registry from persisted records.
func LoadDatabase(db *gorm.DB) (*Registry, error) {
	reg := emptyRegistry()

	if err := loadDatabaseInto(db, reg); err != nil {
		return nil, err
	}
	return reg, nil
}

func emptyRegistry() *Registry {
	return &Registry{
		Identities:  map[string]Identity{},
		Impressions: map[string]Impression{},
		ToolGroups:  map[string]ToolGroup{},
		Concierges:  map[string]Concierge{},
		Workflows:   map[string]Workflow{},
		Jobs:        map[string]Job{},
		Constants:   map[string]Constant{},
	}
}

// SyncTemplates migrates static defaults into the database and validates the
// complete candidate registry before committing any change.
func SyncTemplates(db *gorm.DB, templates []Template, knownTools, knownPlugins map[string]bool) (*Registry, SyncResult, error) {
	if db == nil {
		return nil, SyncResult{}, fmt.Errorf("registry: database is required")
	}
	var candidate *Registry
	var result SyncResult
	err := db.Transaction(func(tx *gorm.DB) error {
		if tx.Dialector.Name() == "postgres" {
			if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", "hephaestus:registry-template-sync").Error; err != nil {
				return fmt.Errorf("registry: lock template synchronization: %w", err)
			}
		}
		for _, template := range templates {
			decision, err := syncTemplate(tx, template)
			if err != nil {
				return err
			}
			switch decision {
			case syncCreated:
				result.Created++
			case syncUpdated:
				result.Updated++
			case syncPreserved:
				result.Preserved++
			}
		}
		var err error
		candidate, err = LoadDatabase(tx)
		if err != nil {
			return err
		}
		if err := candidate.Validate(knownTools, knownPlugins); err != nil {
			return fmt.Errorf("registry: template synchronization validation failed: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, SyncResult{}, err
	}
	return candidate, result, nil
}

type syncDecision uint8

const (
	syncCreated syncDecision = iota
	syncUpdated
	syncPreserved
)

func syncTemplate(tx *gorm.DB, template Template) (syncDecision, error) {
	if _, err := ValueName(template.Kind, template.Value); err != nil {
		return syncPreserved, err
	}
	model := modelForKind(template.Kind)
	err := tx.First(model, "name = ?", template.Name).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if err := tx.Create(template.Value).Error; err != nil {
			return syncPreserved, fmt.Errorf("registry: create template %s %q: %w", template.Kind, template.Name, err)
		}
		if err := saveTemplateState(tx, template); err != nil {
			return syncPreserved, err
		}
		return syncCreated, nil
	}
	if err != nil {
		return syncPreserved, fmt.Errorf("registry: load template target %s %q: %w", template.Kind, template.Name, err)
	}

	var state TemplateState
	err = tx.First(&state, "kind = ? AND name = ?", template.Kind, template.Name).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if err := saveTemplateState(tx, template); err != nil {
			return syncPreserved, err
		}
		return syncPreserved, nil
	}
	if err != nil {
		return syncPreserved, fmt.Errorf("registry: load template state %s %q: %w", template.Kind, template.Name, err)
	}
	if state.ContentHash == template.Hash {
		if state.SourcePath != template.Path || !state.FileModifiedAt.Equal(template.ModifiedAt) {
			if err := saveTemplateState(tx, template); err != nil {
				return syncPreserved, err
			}
		}
		return syncPreserved, nil
	}

	updatedAt, err := recordUpdatedAt(template.Kind, model)
	if err != nil {
		return syncPreserved, err
	}
	decision := syncPreserved
	if template.ModifiedAt.After(updatedAt) {
		if err := replaceRecord(tx, template.Kind, template.Value); err != nil {
			return syncPreserved, fmt.Errorf("registry: update template %s %q: %w", template.Kind, template.Name, err)
		}
		decision = syncUpdated
	}
	if err := saveTemplateState(tx, template); err != nil {
		return syncPreserved, err
	}
	return decision, nil
}

func saveTemplateState(tx *gorm.DB, template Template) error {
	state := TemplateState{
		Kind:           template.Kind,
		Name:           template.Name,
		SourcePath:     template.Path,
		ContentHash:    template.Hash,
		FileModifiedAt: template.ModifiedAt,
	}
	if err := tx.Save(&state).Error; err != nil {
		return fmt.Errorf("registry: save template state %s %q: %w", template.Kind, template.Name, err)
	}
	return nil
}

func recordUpdatedAt(kind Kind, value any) (time.Time, error) {
	switch typed := value.(type) {
	case *Identity:
		return typed.UpdatedAt, nil
	case *Impression:
		return typed.UpdatedAt, nil
	case *ToolGroup:
		return typed.UpdatedAt, nil
	case *Concierge:
		return typed.UpdatedAt, nil
	case *Workflow:
		return typed.UpdatedAt, nil
	case *Job:
		return typed.UpdatedAt, nil
	case *Constant:
		return typed.UpdatedAt, nil
	default:
		return time.Time{}, wrongPayload(kind)
	}
}

func replaceRecord(tx *gorm.DB, kind Kind, value any) error {
	name, err := ValueName(kind, value)
	if err != nil {
		return err
	}
	return tx.Model(modelForKind(kind)).Where("name = ?", name).Select("*").Omit("created_at", "updated_at").Updates(value).Error
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
		normalizeImpression(&value)
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
	for index := range jobs {
		if err := validatePersistedName(KindJob, jobs[index].Name); err != nil {
			return err
		}
		if err := normalizeJob(&jobs[index]); err != nil {
			return err
		}
		reg.Jobs[jobs[index].Name] = jobs[index]
	}

	var constants []Constant
	if err := db.Find(&constants).Error; err != nil {
		return fmt.Errorf("registry: load database constants: %w", err)
	}
	for _, value := range constants {
		if err := validatePersistedName(KindConstant, value.Name); err != nil {
			return err
		}
		reg.Constants[value.Name] = value
	}

	return nil
}

func validatePersistedName(kind Kind, name string) error {
	if name == "" {
		return fmt.Errorf("registry: database %s name must not be empty", kind)
	}
	if kind == KindConstant && !validPromptVariable(name) {
		return fmt.Errorf("registry: database constant name %q must match [A-Za-z_][A-Za-z0-9_]*", name)
	}
	return nil
}
