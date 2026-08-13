package tools

import (
	"strings"
	"testing"
)

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
