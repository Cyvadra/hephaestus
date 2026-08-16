package store

import (
	"path/filepath"
	"testing"
)

func TestOpenSQLiteCreatesParentDirectory(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "nested", "hephaestus.db")
	db, err := Open("sqlite://" + databasePath)
	if err != nil {
		t.Fatalf("Open SQLite: %v", err)
	}

	if err := db.Exec("SELECT 1").Error; err != nil {
		t.Fatalf("query SQLite database: %v", err)
	}
}
