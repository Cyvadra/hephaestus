package builtin

import (
	"context"
	"strings"
	"testing"

	"github.com/Cyvadra/hephaestus/internal/plugin"
)

func TestSessionSummaryPlugin_IgnoresAssistantMessageHook(t *testing.T) {
	p := &SessionSummaryPlugin{}
	turn := plugin.TurnContext{SessionID: 1}

	got, err := p.Handle(context.Background(), plugin.HookAssistantMessageSent2User, plugin.PhaseAfter, turn)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got.SessionID != turn.SessionID {
		t.Fatalf("expected unchanged turn, got %+v", got)
	}
}

func TestParseSessionSummary_ExtractsWrappedJSON(t *testing.T) {
	title, summary, err := parseSessionSummary("prefix\n{\"session\":{\"title\":\"备份方案\",\"summary\":\"讨论恢复目标。\"}}\nsuffix")
	if err != nil {
		t.Fatalf("parseSessionSummary: %v", err)
	}
	if title != "备份方案" || summary != "讨论恢复目标。" {
		t.Fatalf("parseSessionSummary = %q, %q", title, summary)
	}
}

func TestParseSessionSummary_ClampsFields(t *testing.T) {
	title, summary, err := parseSessionSummary(`{"session":{"title":"12345678901234567890","summary":"` + strings.Repeat("a", 210) + `"}}`)
	if err != nil {
		t.Fatalf("parseSessionSummary: %v", err)
	}
	if len([]rune(title)) != 15 || len([]rune(summary)) != 200 {
		t.Fatalf("clamped lengths = %d, %d", len([]rune(title)), len([]rune(summary)))
	}
}

func TestParseSessionSummary_RejectsMissingJSON(t *testing.T) {
	if _, _, err := parseSessionSummary("no json here"); err == nil {
		t.Fatal("parseSessionSummary unexpectedly accepted response without JSON")
	}
}
