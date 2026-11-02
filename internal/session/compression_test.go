package session

import (
	"testing"
	"time"

	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/glebarez/sqlite"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

func compressionFixture(t *testing.T) (*gorm.DB, store.Session, []store.ChatMessage) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&store.Project{}, &store.Session{}, &store.ChatMessage{}, &store.Compression{}); err != nil {
		t.Fatal(err)
	}
	project := store.Project{Name: "compression-readonly-test"}
	if err := db.Create(&project).Error; err != nil {
		t.Fatal(err)
	}
	sess := store.Session{
		ProjectID:       project.ID,
		Settings:        datatypes.NewJSONType(store.SessionSettings{}),
		LastMessageTime: time.Now(),
	}
	if err := db.Create(&sess).Error; err != nil {
		t.Fatal(err)
	}
	messages := []store.ChatMessage{
		{SessionID: sess.ID, Role: "user", Content: "one"},
		{SessionID: sess.ID, Role: "assistant", Content: "two"},
		{SessionID: sess.ID, Role: "user", Content: "three"},
		{SessionID: sess.ID, Role: "assistant", Content: "four"},
	}
	for i := range messages {
		if err := db.Create(&messages[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	return db, sess, messages
}

func compressionRow(t *testing.T, db *gorm.DB, sessionID, firstID, lastID uint, createdAt time.Time) store.Compression {
	t.Helper()
	row := store.Compression{
		SessionID:      sessionID,
		FirstMessageID: firstID,
		LastMessageID:  lastID,
		Messages:       datatypes.JSON(`[]`),
		CreatedAt:      createdAt,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row
}

func reloadSession(t *testing.T, db *gorm.DB, id uint) store.Session {
	t.Helper()
	var sess store.Session
	if err := db.First(&sess, id).Error; err != nil {
		t.Fatal(err)
	}
	return sess
}

func TestResolveCompressionReadOnlyReturnsCacheHitWithoutWriting(t *testing.T) {
	db, sess, messages := compressionFixture(t)
	row := compressionRow(t, db, sess.ID, messages[0].ID, messages[1].ID, time.Now())
	if err := db.Model(&sess).Updates(map[string]any{
		"compression_id":              row.ID,
		"compression_last_message_id": row.LastMessageID,
	}).Error; err != nil {
		t.Fatal(err)
	}

	var before store.Session
	if err := db.First(&before, sess.ID).Error; err != nil {
		t.Fatal(err)
	}
	got, err := New(db).ResolveCompressionReadOnly(before, messages[:2])
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != row.ID {
		t.Fatalf("ResolveCompressionReadOnly() = %+v, want compression %d", got, row.ID)
	}

	var after store.Session
	if err := db.First(&after, sess.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.CompressionID == nil || *after.CompressionID != row.ID {
		t.Fatalf("read-only resolution changed compression id: %+v", after.CompressionID)
	}
	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("read-only resolution wrote the session: updated_at %v -> %v", before.UpdatedAt, after.UpdatedAt)
	}
}

func TestResolveCompressionReadOnlyFindsCandidateWithoutAdopting(t *testing.T) {
	db, sess, messages := compressionFixture(t)
	stale := compressionRow(t, db, sess.ID, messages[0].ID, messages[1].ID, time.Now().Add(-time.Minute))
	candidate := compressionRow(t, db, sess.ID, messages[2].ID, messages[2].ID, time.Now())
	if err := db.Model(&sess).Updates(map[string]any{
		"compression_id":              stale.ID,
		"compression_last_message_id": stale.LastMessageID,
	}).Error; err != nil {
		t.Fatal(err)
	}

	got, err := New(db).ResolveCompressionReadOnly(reloadSession(t, db, sess.ID), messages[2:])
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != candidate.ID {
		t.Fatalf("ResolveCompressionReadOnly() = %+v, want on-path compression %d", got, candidate.ID)
	}

	var after store.Session
	if err := db.First(&after, sess.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.CompressionID == nil || *after.CompressionID != stale.ID {
		t.Fatalf("read-only resolution adopted a candidate: %+v", after.CompressionID)
	}
}

func TestResolveCompressionReadOnlyDoesNotClearStalePointers(t *testing.T) {
	db, sess, messages := compressionFixture(t)
	stale := compressionRow(t, db, sess.ID, messages[0].ID, messages[1].ID, time.Now())
	if err := db.Model(&sess).Updates(map[string]any{
		"compression_id":              stale.ID,
		"compression_last_message_id": stale.LastMessageID,
	}).Error; err != nil {
		t.Fatal(err)
	}

	got, err := New(db).ResolveCompressionReadOnly(reloadSession(t, db, sess.ID), messages[2:])
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("ResolveCompressionReadOnly() = %+v, want nil", got)
	}

	var after store.Session
	if err := db.First(&after, sess.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.CompressionID == nil || *after.CompressionID != stale.ID {
		t.Fatalf("read-only resolution cleared pointers: %+v", after.CompressionID)
	}
}

func TestResolveCompressionStillAdoptsAndClears(t *testing.T) {
	db, sess, messages := compressionFixture(t)
	stale := compressionRow(t, db, sess.ID, messages[0].ID, messages[1].ID, time.Now().Add(-time.Minute))
	candidate := compressionRow(t, db, sess.ID, messages[2].ID, messages[2].ID, time.Now())
	if err := db.Model(&sess).Updates(map[string]any{
		"compression_id":              stale.ID,
		"compression_last_message_id": stale.LastMessageID,
	}).Error; err != nil {
		t.Fatal(err)
	}

	current := reloadSession(t, db, sess.ID)
	got, err := New(db).ResolveCompression(&current, messages[2:])
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != candidate.ID {
		t.Fatalf("ResolveCompression() = %+v, want %d", got, candidate.ID)
	}
	if current.CompressionID == nil || *current.CompressionID != candidate.ID {
		t.Fatalf("ResolveCompression() did not update the session pointer: %+v", current.CompressionID)
	}

	var after store.Session
	if err := db.First(&after, sess.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.CompressionID == nil || *after.CompressionID != candidate.ID {
		t.Fatalf("ResolveCompression() did not persist the adopted pointer: %+v", after.CompressionID)
	}

	// A path with no covering compression must clear the cache, as before.
	got, err = New(db).ResolveCompression(&current, messages[3:])
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("ResolveCompression() = %+v, want nil", got)
	}
	if current.CompressionID != nil || current.CompressionLastMessageID != nil {
		t.Fatalf("ResolveCompression() did not clear in-memory pointers: %+v %+v", current.CompressionID, current.CompressionLastMessageID)
	}
	if err := db.First(&after, sess.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.CompressionID != nil || after.CompressionLastMessageID != nil {
		t.Fatalf("ResolveCompression() did not persist cleared pointers: %+v %+v", after.CompressionID, after.CompressionLastMessageID)
	}
}

// Switching away from a compacted branch clears the session's cached
// pointers. Switching back must re-adopt the branch's compression row even
// though CompressionID is now nil.
func TestResolveCompressionReAdoptsAfterSwitchBack(t *testing.T) {
	db, sess, messages := compressionFixture(t)
	row := compressionRow(t, db, sess.ID, messages[0].ID, messages[1].ID, time.Now())
	if err := db.Model(&sess).Updates(map[string]any{
		"compression_id":              row.ID,
		"compression_last_message_id": row.LastMessageID,
	}).Error; err != nil {
		t.Fatal(err)
	}

	service := New(db)
	// Branch switch: messages[2:] does not contain the cached boundary and
	// has no compression of its own, so the pointers are cleared.
	current := reloadSession(t, db, sess.ID)
	got, err := service.ResolveCompression(&current, messages[2:])
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("ResolveCompression() on the sibling branch = %+v, want nil", got)
	}
	current = reloadSession(t, db, sess.ID)
	if current.CompressionID != nil || current.CompressionLastMessageID != nil {
		t.Fatalf("pointers not cleared: %+v %+v", current.CompressionID, current.CompressionLastMessageID)
	}

	// Switch back: the row still covers messages[:2] and must be re-adopted.
	got, err = service.ResolveCompression(&current, messages[:2])
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != row.ID {
		t.Fatalf("ResolveCompression() after switching back = %+v, want compression %d", got, row.ID)
	}
	if current.CompressionID == nil || *current.CompressionID != row.ID {
		t.Fatalf("re-adopted in-memory pointer = %+v, want %d", current.CompressionID, row.ID)
	}
}
