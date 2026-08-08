// Package project implements Project: a named, on-disk folder the Agent
// may create (gated behind an opt-in ToolGroup, so creation only happens
// with the user's explicit authorization) to scope file/exec tools and
// take memory-retrieval priority over raw chat history.
package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

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
