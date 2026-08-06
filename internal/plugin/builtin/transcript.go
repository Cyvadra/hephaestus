package builtin

import (
	"strings"

	"github.com/Cyvadra/hephaestus/internal/store"
)

// renderTranscript renders messages as "role: content" lines, truncated to
// at most maxChars characters (keeping the most recent content, since
// that's most relevant for summarization/status updates).
func renderTranscript(messages []store.ChatMessage, maxChars int) string {
	var b strings.Builder
	for _, m := range messages {
		if m.Content == "" {
			continue
		}
		b.WriteString(m.Role)
		b.WriteString(": ")
		b.WriteString(m.Content)
		b.WriteString("\n")
	}
	out := b.String()
	if len(out) > maxChars {
		out = out[len(out)-maxChars:]
	}
	return out
}

// splitTitleSummary parses the model's two-line title+summary response,
// clamping each to the design doc's length limits (title <=20 runes,
// summary <=300 runes).
func splitTitleSummary(result string) (title, summary string) {
	lines := strings.SplitN(strings.TrimSpace(result), "\n", 2)
	title = clampRunes(strings.TrimSpace(lines[0]), 20)
	if len(lines) > 1 {
		summary = clampRunes(strings.TrimSpace(lines[1]), 300)
	}
	return title, summary
}

func clampRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}
