// Package store_test exercises the store+session packages against a real
// Postgres instance. It is skipped unless HEPHAESTUS_TEST_POSTGRES_DSN is
// set, e.g.:
//
//	HEPHAESTUS_TEST_POSTGRES_DSN="host=127.0.0.1 user=hephaestus password=... dbname=hephaestus port=5432 sslmode=disable" go test ./internal/store/...
package store_test

import (
	"os"
	"testing"
	"time"

	"github.com/Cyvadra/hephaestus/internal/session"
	"github.com/Cyvadra/hephaestus/internal/store"
	"gorm.io/datatypes"
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

// newTestSession creates a Session tagged with a unique SourceConcierge so
// it (and everything chained under it) can be identified and cleaned up.
func newTestSession(t *testing.T, db *gorm.DB, marker string) *store.Session {
	t.Helper()
	sess := &store.Session{
		SourceConcierge: marker,
		Settings: datatypes.NewJSONType(store.SessionSettings{
			Identity: "test-identity",
		}),
	}
	if err := db.Create(sess).Error; err != nil {
		t.Fatalf("create session: %v", err)
	}
	t.Cleanup(func() {
		db.Where("session_id = ?", sess.ID).Delete(&store.ChatMessage{})
		db.Where("session_id = ?", sess.ID).Delete(&store.Compression{})
		db.Where("session_id = ?", sess.ID).Delete(&store.PluginState{})
		db.Delete(&store.Session{}, sess.ID)
	})
	return sess
}

func TestIntegration_AppendMessagesAndActivePath(t *testing.T) {
	db := openTestDB(t)
	svc := session.New(db)
	sess := newTestSession(t, db, "hephaestus-it-active-path")

	saved, err := svc.AppendMessages(sess.ID, nil, []store.ChatMessage{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi there"},
	})
	if err != nil {
		t.Fatalf("AppendMessages: %v", err)
	}
	if len(saved) != 2 {
		t.Fatalf("expected 2 saved messages, got %d", len(saved))
	}

	var reloaded store.Session
	if err := db.First(&reloaded, sess.ID).Error; err != nil {
		t.Fatalf("reload session: %v", err)
	}
	if reloaded.ActiveLeafMessageID == nil || *reloaded.ActiveLeafMessageID != saved[1].ID {
		t.Fatalf("expected active_leaf_message_id %d, got %v", saved[1].ID, reloaded.ActiveLeafMessageID)
	}

	path, err := svc.ActivePath(reloaded)
	if err != nil {
		t.Fatalf("ActivePath: %v", err)
	}
	if len(path) != 2 || path[0].Content != "hello" || path[1].Content != "hi there" {
		t.Fatalf("unexpected active path: %+v", path)
	}

	// A second AppendMessages call parented at the first message creates
	// a sibling branch; the original path must remain reachable.
	branch, err := svc.AppendMessages(sess.ID, &saved[0].ID, []store.ChatMessage{
		{Role: "assistant", Content: "a different reply"},
	})
	if err != nil {
		t.Fatalf("AppendMessages (branch): %v", err)
	}

	db.First(&reloaded, sess.ID)
	if *reloaded.ActiveLeafMessageID != branch[0].ID {
		t.Fatalf("expected active leaf to move to branch message %d", branch[0].ID)
	}
	branchPath, err := svc.ActivePath(reloaded)
	if err != nil {
		t.Fatalf("ActivePath (branch): %v", err)
	}
	if len(branchPath) != 2 || branchPath[1].Content != "a different reply" {
		t.Fatalf("unexpected branch path: %+v", branchPath)
	}
}

func TestIntegration_ResolveCompression(t *testing.T) {
	db := openTestDB(t)
	svc := session.New(db)
	sess := newTestSession(t, db, "hephaestus-it-compression")

	saved, err := svc.AppendMessages(sess.ID, nil, []store.ChatMessage{
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "second"},
		{Role: "user", Content: "third"},
	})
	if err != nil {
		t.Fatalf("AppendMessages: %v", err)
	}

	var reloaded store.Session
	db.First(&reloaded, sess.ID)

	// Case 3: no compression yet.
	path, _ := svc.ActivePath(reloaded)
	comp, err := svc.ResolveCompression(&reloaded, path)
	if err != nil || comp != nil {
		t.Fatalf("expected no compression, got %+v, err=%v", comp, err)
	}

	// Manually attach a compression covering up to the second message.
	compressionRow := &store.Compression{
		SessionID:      sess.ID,
		FirstMessageID: saved[0].ID,
		LastMessageID:  saved[1].ID,
		Messages:       []byte(`[{"role":"user","content":"first"},{"role":"assistant","content":"second"}]`),
		CreatedAt:      time.Now(),
	}
	if err := db.Create(compressionRow).Error; err != nil {
		t.Fatalf("create compression: %v", err)
	}
	reloaded.CompressionID = &compressionRow.ID
	reloaded.CompressionLastMessageID = &saved[1].ID
	db.Save(&reloaded)

	// Case 1: cache hit (compression's last message is still on the active path).
	comp, err = svc.ResolveCompression(&reloaded, path)
	if err != nil {
		t.Fatalf("ResolveCompression (hit): %v", err)
	}
	if comp == nil || comp.ID != compressionRow.ID {
		t.Fatalf("expected cache hit on compression %d, got %+v", compressionRow.ID, comp)
	}

	// Case 2: switch to a branch that no longer includes the compressed
	// message; with no other compression covering it, pointers must clear.
	branch, err := svc.AppendMessages(sess.ID, nil, []store.ChatMessage{
		{Role: "user", Content: "a whole new branch from the root"},
	})
	if err != nil {
		t.Fatalf("AppendMessages (new root branch): %v", err)
	}
	db.First(&reloaded, sess.ID)
	branchPath, _ := svc.ActivePath(reloaded)

	comp, err = svc.ResolveCompression(&reloaded, branchPath)
	if err != nil {
		t.Fatalf("ResolveCompression (miss): %v", err)
	}
	if comp != nil {
		t.Fatalf("expected compression cache to clear on branch switch, got %+v", comp)
	}
	if reloaded.CompressionID != nil {
		t.Fatalf("expected CompressionID cleared, got %v", reloaded.CompressionID)
	}
	_ = branch
}

func TestIntegration_StoreCompression(t *testing.T) {
	db := openTestDB(t)
	svc := session.New(db)
	sess := newTestSession(t, db, "hephaestus-it-store-compression")

	saved, err := svc.AppendMessages(sess.ID, nil, []store.ChatMessage{
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "second"},
	})
	if err != nil {
		t.Fatalf("AppendMessages: %v", err)
	}

	row, err := svc.StoreCompression(sess.ID, saved[0].ID, saved[1].ID,
		[]byte(`[{"role":"user","content":"first"},{"role":"assistant","content":"second"}]`))
	if err != nil {
		t.Fatalf("StoreCompression: %v", err)
	}

	var reloaded store.Session
	if err := db.First(&reloaded, sess.ID).Error; err != nil {
		t.Fatalf("reload session: %v", err)
	}
	if reloaded.CompressionID == nil || *reloaded.CompressionID != row.ID {
		t.Fatalf("expected CompressionID %d, got %v", row.ID, reloaded.CompressionID)
	}
	if reloaded.CompressionLastMessageID == nil || *reloaded.CompressionLastMessageID != saved[1].ID {
		t.Fatalf("expected CompressionLastMessageID %d, got %v", saved[1].ID, reloaded.CompressionLastMessageID)
	}

	path, err := svc.ActivePath(reloaded)
	if err != nil {
		t.Fatalf("ActivePath: %v", err)
	}
	resolved, err := svc.ResolveCompression(&reloaded, path)
	if err != nil {
		t.Fatalf("ResolveCompression: %v", err)
	}
	if resolved == nil || resolved.ID != row.ID {
		t.Fatalf("expected stored compression %d, got %+v", row.ID, resolved)
	}
}

func TestIntegration_PluginState(t *testing.T) {
	db := openTestDB(t)
	sess := newTestSession(t, db, "hephaestus-it-plugin-state")

	type state struct {
		Count int `json:"count"`
	}

	found, err := store.LoadPluginState(db, sess.ID, "test_plugin", &state{})
	if err != nil {
		t.Fatalf("LoadPluginState (empty): %v", err)
	}
	if found {
		t.Fatal("expected no state before first save")
	}

	if err := store.SavePluginState(db, sess.ID, "test_plugin", state{Count: 1}); err != nil {
		t.Fatalf("SavePluginState: %v", err)
	}
	var loaded state
	found, err = store.LoadPluginState(db, sess.ID, "test_plugin", &loaded)
	if err != nil || !found || loaded.Count != 1 {
		t.Fatalf("expected count=1, got %+v found=%v err=%v", loaded, found, err)
	}

	// Upsert path.
	if err := store.SavePluginState(db, sess.ID, "test_plugin", state{Count: 2}); err != nil {
		t.Fatalf("SavePluginState (update): %v", err)
	}
	found, err = store.LoadPluginState(db, sess.ID, "test_plugin", &loaded)
	if err != nil || !found || loaded.Count != 2 {
		t.Fatalf("expected count=2 after update, got %+v found=%v err=%v", loaded, found, err)
	}
}
