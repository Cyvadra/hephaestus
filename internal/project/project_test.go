// Package project_test exercises project.Service against a real Postgres
// instance. It is skipped unless HEPHAESTUS_TEST_POSTGRES_DSN is set, e.g.:
//
//	HEPHAESTUS_TEST_POSTGRES_DSN="host=127.0.0.1 user=hephaestus password=... dbname=hephaestus port=5432 sslmode=disable" go test ./internal/project/...
package project_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Cyvadra/hephaestus/internal/project"
	"github.com/Cyvadra/hephaestus/internal/store"
	"gorm.io/gorm"
)

func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("HEPHAESTUS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("HEPHAESTUS_TEST_POSTGRES_DSN not set; skipping Postgres integration test")
	}
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	return db
}

func TestCreate_WritesDirectoryAgentsMdAndRow(t *testing.T) {
	db := openTestDB(t)
	root := t.TempDir()
	svc, err := project.New(db, root)
	if err != nil {
		t.Fatalf("project.New: %v", err)
	}
	t.Cleanup(func() { db.Where("name = ?", "test-alpha").Delete(&store.Project{}) })

	p, err := svc.Create("test-alpha", "an example project")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if p.ID == 0 {
		t.Fatal("expected a persisted id")
	}

	agentsPath := filepath.Join(root, "test-alpha", "AGENTS.md")
	data, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatalf("expected AGENTS.md to exist: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected AGENTS.md to have content")
	}

	got, err := svc.GetByName("test-alpha")
	if err != nil {
		t.Fatalf("GetByName: %v", err)
	}
	if got.Description != "an example project" {
		t.Fatalf("expected description to round-trip, got %q", got.Description)
	}
}

func TestCreate_RejectsDuplicateName(t *testing.T) {
	db := openTestDB(t)
	root := t.TempDir()
	svc, err := project.New(db, root)
	if err != nil {
		t.Fatalf("project.New: %v", err)
	}
	t.Cleanup(func() { db.Where("name = ?", "test-dup").Delete(&store.Project{}) })

	if _, err := svc.Create("test-dup", ""); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if _, err := svc.Create("test-dup", ""); err == nil {
		t.Fatal("expected duplicate name to be rejected")
	}
}

func TestCreate_RejectsInvalidName(t *testing.T) {
	db := openTestDB(t)
	root := t.TempDir()
	svc, err := project.New(db, root)
	if err != nil {
		t.Fatalf("project.New: %v", err)
	}

	if _, err := svc.Create("Not A Slug!", ""); err == nil {
		t.Fatal("expected invalid name to be rejected")
	}
}

func TestDelete_PreservesDirectoryByDefault(t *testing.T) {
	db := openTestDB(t)
	root := t.TempDir()
	svc, err := project.New(db, root)
	if err != nil {
		t.Fatalf("project.New: %v", err)
	}
	t.Cleanup(func() { db.Where("name = ?", "test-delete").Delete(&store.Project{}) })

	if _, err := svc.Create("test-delete", ""); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := svc.Delete("test-delete", false); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := svc.GetByName("test-delete"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("expected deleted project to be absent, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "test-delete")); err != nil {
		t.Fatalf("expected project directory to be preserved, got %v", err)
	}
}

func TestDelete_RemovesDirectoryWhenRequested(t *testing.T) {
	db := openTestDB(t)
	root := t.TempDir()
	svc, err := project.New(db, root)
	if err != nil {
		t.Fatalf("project.New: %v", err)
	}
	t.Cleanup(func() { db.Where("name = ?", "test-delete-directory").Delete(&store.Project{}) })

	if _, err := svc.Create("test-delete-directory", ""); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := svc.Delete("test-delete-directory", true); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "test-delete-directory")); !os.IsNotExist(err) {
		t.Fatalf("expected project directory to be removed, got %v", err)
	}
}

func TestDelete_RejectsDefaultProject(t *testing.T) {
	db := openTestDB(t)
	root := t.TempDir()
	svc, err := project.New(db, root)
	if err != nil {
		t.Fatalf("project.New: %v", err)
	}
	t.Cleanup(func() { db.Where("name = ?", project.DefaultName).Delete(&store.Project{}) })
	if _, err := svc.EnsureDefault(); err != nil {
		t.Fatalf("EnsureDefault: %v", err)
	}

	if err := svc.Delete(project.DefaultName, false); err == nil {
		t.Fatal("expected default project deletion to be rejected")
	}
}

func TestEnsureDefault_CreatesAndReusesSystemWorkspace(t *testing.T) {
	db := openTestDB(t)
	root := t.TempDir()
	svc, err := project.New(db, root)
	if err != nil {
		t.Fatalf("project.New: %v", err)
	}
	t.Cleanup(func() { db.Where("name = ?", project.DefaultName).Delete(&store.Project{}) })

	first, err := svc.EnsureDefault()
	if err != nil {
		t.Fatalf("EnsureDefault: %v", err)
	}
	second, err := svc.EnsureDefault()
	if err != nil {
		t.Fatalf("EnsureDefault second call: %v", err)
	}
	if first.ID == 0 || first.ID != second.ID || first.Name != project.DefaultName {
		t.Fatalf("unexpected default projects: first=%+v second=%+v", first, second)
	}
	if _, err := os.Stat(filepath.Join(root, project.DefaultName, "AGENTS.md")); err != nil {
		t.Fatalf("expected default AGENTS.md: %v", err)
	}
}

func TestSetConciergeAvailability_UpdatesSelectedProjects(t *testing.T) {
	db := openTestDB(t)
	root := t.TempDir()
	svc, err := project.New(db, root)
	if err != nil {
		t.Fatalf("project.New: %v", err)
	}
	for _, name := range []string{"test-availability-a", "test-availability-b"} {
		if _, err := svc.Create(name, ""); err != nil {
			t.Fatalf("Create %q: %v", name, err)
		}
		t.Cleanup(func() { db.Where("name = ?", name).Delete(&store.Project{}) })
	}

	if err := svc.SetConciergeAvailability("coding", []string{"test-availability-b", " test-availability-b "}); err != nil {
		t.Fatalf("SetConciergeAvailability: %v", err)
	}
	selected, err := svc.GetByName("test-availability-b")
	if err != nil {
		t.Fatalf("GetByName selected: %v", err)
	}
	if !svc.IsConciergeAvailable(*selected, "coding") {
		t.Fatalf("expected coding to be available in selected project: %+v", selected.AvailableConciergeList)
	}
	unselected, err := svc.GetByName("test-availability-a")
	if err != nil {
		t.Fatalf("GetByName unselected: %v", err)
	}
	if svc.IsConciergeAvailable(*unselected, "coding") {
		t.Fatalf("did not expect coding in unselected project: %+v", unselected.AvailableConciergeList)
	}

	if err := svc.RemoveConciergeFromProjects("coding"); err != nil {
		t.Fatalf("RemoveConciergeFromProjects: %v", err)
	}
	selected, err = svc.GetByName("test-availability-b")
	if err != nil {
		t.Fatalf("GetByName after remove: %v", err)
	}
	if svc.IsConciergeAvailable(*selected, "coding") {
		t.Fatalf("expected coding to be removed: %+v", selected.AvailableConciergeList)
	}
}

func TestSetConciergeAvailability_RejectsUnknownProjectWithoutChanges(t *testing.T) {
	db := openTestDB(t)
	root := t.TempDir()
	svc, err := project.New(db, root)
	if err != nil {
		t.Fatalf("project.New: %v", err)
	}
	p, err := svc.Create("test-availability-unchanged", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { db.Where("name = ?", p.Name).Delete(&store.Project{}) })

	if err := svc.SetConciergeAvailability("coding", []string{p.Name, "missing-project"}); err == nil {
		t.Fatal("expected unknown project to be rejected")
	}
	reloaded, err := svc.GetByName(p.Name)
	if err != nil {
		t.Fatalf("GetByName: %v", err)
	}
	if svc.IsConciergeAvailable(*reloaded, "coding") {
		t.Fatalf("unknown project request changed availability: %+v", reloaded.AvailableConciergeList)
	}
}
