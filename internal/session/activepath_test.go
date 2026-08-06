package session

import (
	"testing"

	"github.com/Cyvadra/hephaestus/internal/store"
)

func idPtr(id uint) *uint { return &id }

func TestWalkActivePath(t *testing.T) {
	all := []store.ChatMessage{
		{ID: 1, ParentMessageID: nil, Role: "user", Content: "hi"},
		{ID: 2, ParentMessageID: idPtr(1), Role: "assistant", Content: "hello"},
		{ID: 3, ParentMessageID: idPtr(2), Role: "user", Content: "how are you"},
		// A sibling branch off message 2, not on the active path.
		{ID: 4, ParentMessageID: idPtr(2), Role: "user", Content: "alternate question"},
	}

	path, err := walkActivePath(all, idPtr(3))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(path) != 3 {
		t.Fatalf("expected 3 messages on path, got %d", len(path))
	}
	for i, wantID := range []uint{1, 2, 3} {
		if path[i].ID != wantID {
			t.Errorf("path[%d].ID = %d, want %d", i, path[i].ID, wantID)
		}
	}
}

func TestWalkActivePath_NilLeaf(t *testing.T) {
	path, err := walkActivePath(nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != nil {
		t.Errorf("expected nil path, got %v", path)
	}
}

func TestWalkActivePath_MissingMessage(t *testing.T) {
	_, err := walkActivePath([]store.ChatMessage{{ID: 1}}, idPtr(99))
	if err == nil {
		t.Fatal("expected error for missing message id")
	}
}
