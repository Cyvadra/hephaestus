// Package project_test exercises project.Service against a real Postgres
// instance. It is skipped unless HEPHAESTUS_TEST_POSTGRES_DSN is set, e.g.:
//
//	HEPHAESTUS_TEST_POSTGRES_DSN="host=127.0.0.1 user=hephaestus password=... dbname=hephaestus port=5432 sslmode=disable" go test ./internal/project/...
package project_test

import (
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
