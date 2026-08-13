// Package project implements Project: a named, on-disk folder the Agent
// may create (gated behind an opt-in ToolGroup, so creation only happens
// with the user's explicit authorization) to scope file/shell tools and
// take memory-retrieval priority over raw chat history.
package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/Cyvadra/hephaestus/internal/store"
	"gorm.io/gorm"
)

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

const (
	// DefaultName is the system-created Project bound to sessions unless
	// the user explicitly switches to another Project.
	DefaultName = store.DefaultProjectName

	defaultDescription = "System default workspace for agent file operations."
)

// Service creates and looks up Projects, each backed by a directory named
// after it under root.
type Service struct {
	db   *gorm.DB
	root string
}

// New creates a Service rooted at root, creating the root directory if
// it doesn't already exist.
func New(db *gorm.DB, root string) (*Service, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("project: create projects root %q: %w", root, err)
	}
	return &Service{db: db, root: root}, nil
}

// Path returns p's on-disk directory.
func (s *Service) Path(p store.Project) string {
	return filepath.Join(s.root, p.Name)
}

// Create makes a new Project: its directory and skeleton AGENTS.md are
// written before the database row is inserted, so a failed insert (e.g. a
// duplicate name) never leaves an orphaned, undiscoverable directory.
func (s *Service) Create(name, description string) (*store.Project, error) {
	if !nameRe.MatchString(name) {
		return nil, fmt.Errorf("project: invalid name %q: must match %s", name, nameRe.String())
	}

	var existing store.Project
	switch err := s.db.Where("name = ?", name).First(&existing).Error; {
	case err == nil:
		return nil, fmt.Errorf("project: %q already exists", name)
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return nil, fmt.Errorf("project: check existing name %q: %w", name, err)
	}

	dir := filepath.Join(s.root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("project: create directory %q: %w", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(agentsSkeleton(name, description)), 0o644); err != nil {
		return nil, fmt.Errorf("project: write AGENTS.md: %w", err)
	}

	p := &store.Project{Name: name, Description: description}
	if err := s.db.Create(p).Error; err != nil {
		return nil, fmt.Errorf("project: persist %q: %w", name, err)
	}
	return p, nil
}

// EnsureDefault creates the system default Project when needed and returns
// it. The operation is idempotent across restarts.
func (s *Service) EnsureDefault() (*store.Project, error) {
	p, err := s.GetByName(DefaultName)
	if err == nil {
		if err := s.ensureDirectory(*p); err != nil {
			return nil, err
		}
		return p, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	return s.Create(DefaultName, defaultDescription)
}

// Get loads a Project by id.
func (s *Service) Get(id uint) (*store.Project, error) {
	var p store.Project
	if err := s.db.First(&p, id).Error; err != nil {
		return nil, fmt.Errorf("project: load %d: %w", id, err)
	}
	return &p, nil
}

// GetByName loads a Project by its unique name.
func (s *Service) GetByName(name string) (*store.Project, error) {
	var p store.Project
	if err := s.db.Where("name = ?", name).First(&p).Error; err != nil {
		return nil, fmt.Errorf("project: load %q: %w", name, err)
	}
	return &p, nil
}

// List returns every Project, ordered by id.
func (s *Service) List() ([]store.Project, error) {
	var projects []store.Project
	if err := s.db.Order("id").Find(&projects).Error; err != nil {
		return nil, fmt.Errorf("project: list: %w", err)
	}
	return projects, nil
}

// SetConciergeAvailability makes conciergeName available only to the named
// Projects. The complete update is transactional so a Concierge form save
// cannot leave Projects partially updated.
func (s *Service) SetConciergeAvailability(conciergeName string, projectNames []string) error {
	conciergeName = strings.TrimSpace(conciergeName)
	if conciergeName == "" {
		return errors.New("project: concierge name must not be empty")
	}
	desired := normalizedNames(projectNames)

	return s.db.Transaction(func(tx *gorm.DB) error {
		var projects []store.Project
		if err := tx.Order("id").Find(&projects).Error; err != nil {
			return fmt.Errorf("project: list availability targets: %w", err)
		}
		known := make(map[string]struct{}, len(projects))
		for _, p := range projects {
			known[p.Name] = struct{}{}
		}
		for name := range desired {
			if _, ok := known[name]; !ok {
				return fmt.Errorf("project: %q not found", name)
			}
		}
		for index := range projects {
			p := &projects[index]
			available := removeName(p.AvailableConciergeList, conciergeName)
			if _, ok := desired[p.Name]; ok {
				available = append(available, conciergeName)
			}
			available = sortedNames(available)
			if sameNames(p.AvailableConciergeList, available) {
				continue
			}
			// Update the struct field and Save so GORM's serializer:json
			// writes a real jsonb value. A field-level Update with a
			// []string would emit a Postgres array literal instead.
			p.AvailableConciergeList = available
			if err := tx.Save(p).Error; err != nil {
				return fmt.Errorf("project: update availability for %q: %w", p.Name, err)
			}
		}
		return nil
	})
}

// ValidateNames confirms every supplied Project name exists without changing
// availability state.
func (s *Service) ValidateNames(projectNames []string) error {
	desired := normalizedNames(projectNames)
	if len(desired) == 0 {
		return nil
	}
	var count int64
	if err := s.db.Model(&store.Project{}).Where("name IN ?", mapKeys(desired)).Count(&count).Error; err != nil {
		return fmt.Errorf("project: validate names: %w", err)
	}
	if count != int64(len(desired)) {
		var projects []store.Project
		if err := s.db.Select("name").Where("name IN ?", mapKeys(desired)).Find(&projects).Error; err != nil {
			return fmt.Errorf("project: list validated names: %w", err)
		}
		found := make(map[string]struct{}, len(projects))
		for _, p := range projects {
			found[p.Name] = struct{}{}
		}
		for name := range desired {
			if _, ok := found[name]; !ok {
				return fmt.Errorf("project: %q not found", name)
			}
		}
	}
	return nil
}

// RemoveConciergeFromProjects removes a deleted Concierge from every Project.
func (s *Service) RemoveConciergeFromProjects(conciergeName string) error {
	return s.SetConciergeAvailability(conciergeName, nil)
}

// IsConciergeAvailable reports whether a Project explicitly allows the
// Concierge. An empty list intentionally allows no Concierges.
func (s *Service) IsConciergeAvailable(p store.Project, conciergeName string) bool {
	for _, available := range p.AvailableConciergeList {
		if available == conciergeName {
			return true
		}
	}
	return false
}

// Delete removes an empty non-default Project. Its on-disk directory is
// preserved unless deleteDirectory is true.
func (s *Service) Delete(name string, deleteDirectory bool) error {
	if name == DefaultName {
		return errors.New("project: the default project cannot be deleted")
	}
	p, err := s.GetByName(name)
	if err != nil {
		return err
	}

	var sessionCount int64
	if err := s.db.Model(&store.Session{}).Where("project_id = ?", p.ID).Count(&sessionCount).Error; err != nil {
		return fmt.Errorf("project: count sessions for %q: %w", name, err)
	}
	if sessionCount > 0 {
		return fmt.Errorf("project: %q has sessions and cannot be deleted", name)
	}
	if err := s.db.Delete(p).Error; err != nil {
		return fmt.Errorf("project: delete %q: %w", name, err)
	}
	if deleteDirectory {
		if err := os.RemoveAll(s.Path(*p)); err != nil {
			return fmt.Errorf("project: remove directory for %q: %w", name, err)
		}
	}
	return nil
}

func (s *Service) ensureDirectory(p store.Project) error {
	dir := s.Path(p)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("project: create directory %q: %w", dir, err)
	}
	agentsPath := filepath.Join(dir, "AGENTS.md")
	if _, err := os.Stat(agentsPath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("project: check AGENTS.md: %w", err)
	}
	if err := os.WriteFile(agentsPath, []byte(agentsSkeleton(p.Name, p.Description)), 0o644); err != nil {
		return fmt.Errorf("project: write AGENTS.md: %w", err)
	}
	return nil
}

func agentsSkeleton(name, description string) string {
	if description == "" {
		description = "(no description provided)"
	}
	return fmt.Sprintf("# %s\n\n%s\n\n## Index\n\n(nothing here yet)\n", name, description)
}

func normalizedNames(values []string) map[string]struct{} {
	names := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			names[value] = struct{}{}
		}
	}
	return names
}

func removeName(values []string, name string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" && value != name {
			result = append(result, value)
		}
	}
	return result
}

func sortedNames(values []string) []string {
	seen := normalizedNames(values)
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func sameNames(left, right []string) bool {
	return strings.Join(sortedNames(left), "\x00") == strings.Join(sortedNames(right), "\x00")
}

func mapKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for value := range values {
		keys = append(keys, value)
	}
	return keys
}
