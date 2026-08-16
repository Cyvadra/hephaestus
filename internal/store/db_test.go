package store

import (
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestEnsureDefaultProjectAllowsDefaultConciergeOnCreate(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&Project{}); err != nil {
		t.Fatalf("migrate project: %v", err)
	}

	project, err := ensureDefaultProject(db)
	if err != nil {
		t.Fatalf("ensure default project: %v", err)
	}
	if len(project.AvailableConciergeList) != 1 || project.AvailableConciergeList[0] != defaultConciergeName {
		t.Fatalf("available concierges = %v, want [%q]", project.AvailableConciergeList, defaultConciergeName)
	}
}

func TestEnsureDefaultProjectPreservesExistingAvailability(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&Project{}); err != nil {
		t.Fatalf("migrate project: %v", err)
	}
	existing := Project{Name: DefaultProjectName, AvailableConciergeList: []string{"custom"}}
	if err := db.Create(&existing).Error; err != nil {
		t.Fatalf("create existing project: %v", err)
	}

	project, err := ensureDefaultProject(db)
	if err != nil {
		t.Fatalf("ensure default project: %v", err)
	}
	if len(project.AvailableConciergeList) != 1 || project.AvailableConciergeList[0] != "custom" {
		t.Fatalf("available concierges = %v, want existing value [custom]", project.AvailableConciergeList)
	}
}

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
