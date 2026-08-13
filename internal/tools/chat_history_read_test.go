package tools

import (
	"strings"
	"testing"

	"github.com/Cyvadra/hephaestus/internal/store"
)

func TestPreserveSnippetKeepsFormattingAndReportsRemainder(t *testing.T) {
	got := preserveSnippet("line one\nline two", 10)
	if !strings.HasPrefix(got, "line one\nl") {
		t.Fatalf("preserveSnippet flattened content: %q", got)
	}
	if !strings.Contains(got, "increase max_chars") {
		t.Fatalf("preserveSnippet lacks continuation hint: %q", got)
	}
}

func TestSessionTitleFallback(t *testing.T) {
	if got := sessionTitle(store.Session{}); got != "(untitled)" {
		t.Fatalf("sessionTitle = %q", got)
	}
}
