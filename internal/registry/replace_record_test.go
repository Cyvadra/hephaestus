package registry

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// TestReplaceRecordAdvancesUpdatedAt guards the timestamp that decides
// whether a static template may overwrite a database edit. syncTemplate
// applies a template only when the file is newer than the record's
// updated_at, so a replace that left the column frozen at creation time
// would let the next startup silently discard the user's edit.
func TestReplaceRecordAdvancesUpdatedAt(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&Identity{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	original := &Identity{Name: "edited", Description: "from template", ContextWindowTokens: 1024}
	if err := db.Create(original).Error; err != nil {
		t.Fatalf("create: %v", err)
	}

	// SQLite stores whole nanoseconds but two writes can still land in the
	// same instant; a short pause keeps the comparison meaningful.
	time.Sleep(2 * time.Millisecond)
	if err := replaceRecord(db, KindIdentity, &Identity{Name: "edited", Description: "user edit", ContextWindowTokens: 2048}); err != nil {
		t.Fatalf("replaceRecord: %v", err)
	}

	var edited Identity
	if err := db.First(&edited, "name = ?", "edited").Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if edited.Description != "user edit" || edited.ContextWindowTokens != 2048 {
		t.Fatalf("record was not replaced: %+v", edited)
	}
	if !edited.CreatedAt.Equal(original.CreatedAt) {
		t.Errorf("created_at changed: %v -> %v", original.CreatedAt, edited.CreatedAt)
	}
	if !edited.UpdatedAt.After(original.UpdatedAt) {
		t.Errorf("updated_at did not advance: %v -> %v", original.UpdatedAt, edited.UpdatedAt)
	}
}
