package tools

import (
	"strings"
	"testing"

	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestChatHistoryKeywordPredicate(t *testing.T) {
	tests := []struct {
		dialect   string
		predicate string
	}{
		{dialect: "sqlite", predicate: "instr(lower(content), lower(?)) > 0"},
		{dialect: "postgres", predicate: "strpos(lower(content), lower(?)) > 0"},
	}
	for _, test := range tests {
		if got := chatHistoryKeywordPredicate(test.dialect); got != test.predicate {
			t.Errorf("chatHistoryKeywordPredicate(%q) = %q, want %q", test.dialect, got, test.predicate)
		}
	}
}

func TestSessionsContainingKeywordsWorksWithSQLite(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&store.ChatMessage{}); err != nil {
		t.Fatal(err)
	}
	sessions := []store.Session{{ID: 1}, {ID: 2}}
	if err := db.Create(&store.ChatMessage{SessionID: 1, Content: "股市行情"}).Error; err != nil {
		t.Fatal(err)
	}

	tool := ChatHistorySearchTool{db: db}
	got, err := tool.sessionsContainingKeywords(sessions, []string{"行情"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != 1 {
		t.Fatalf("sessionsContainingKeywords = %+v, want session 1 only", got)
	}
}

func TestMatchedSnippetCentersLongMessageOnMatch(t *testing.T) {
	content := strings.Repeat("before ", 200) + "NEEDLE" + strings.Repeat(" after", 200)
	got := matchedSnippet(content, []string{"needle"}, nil, 120)
	if !strings.Contains(got, "NEEDLE") {
		t.Fatalf("matchedSnippet omitted match: %q", got)
	}
	if !strings.HasPrefix(got, "… ") || !strings.Contains(got, "…(+") {
		t.Fatalf("matchedSnippet = %q, want both truncation markers", got)
	}
}

func TestParseChatHistorySearchArgsPreservesZeroNeighbours(t *testing.T) {
	args, err := parseChatHistorySearchArgs(map[string]any{"keywords": []string{"needle"}, "num_neighbour_messages": 0})
	if err != nil {
		t.Fatal(err)
	}
	if args.NumNeighbourMessages == nil || *args.NumNeighbourMessages != 0 {
		t.Fatalf("num_neighbour_messages = %v, want 0", args.NumNeighbourMessages)
	}
}

func TestParseChatHistorySearchArgsDefaultsAndCapsNeighbours(t *testing.T) {
	defaults, err := parseChatHistorySearchArgs(map[string]any{"keywords": []string{"needle"}})
	if err != nil || defaults.NumNeighbourMessages == nil || *defaults.NumNeighbourMessages != 2 {
		t.Fatalf("defaults = %+v, err = %v", defaults, err)
	}
	capped, err := parseChatHistorySearchArgs(map[string]any{"keywords": []string{"needle"}, "num_neighbour_messages": 99})
	if err != nil || *capped.NumNeighbourMessages != maxNeighbourMessages {
		t.Fatalf("capped = %+v, err = %v", capped, err)
	}
}
