package chat

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Cyvadra/ds4"
	"github.com/Cyvadra/hephaestus/internal/llm"
	"github.com/Cyvadra/hephaestus/internal/plugin"
	"github.com/Cyvadra/hephaestus/internal/registry"
	"github.com/Cyvadra/hephaestus/internal/session"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/Cyvadra/hephaestus/internal/transform"
	"github.com/glebarez/sqlite"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// compressionFixture builds a Pipeline backed by an in-memory database and a
// stub chat-completions server. The body channel delivers the captured
// request body for prompt assertions.
func compressionFixture(t *testing.T, response string) (*Pipeline, *gorm.DB, store.Session, <-chan string) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&store.Project{}, &store.Session{}, &store.ChatMessage{}, &store.Compression{}); err != nil {
		t.Fatal(err)
	}
	project := store.Project{Name: "compression-test"}
	if err := db.Create(&project).Error; err != nil {
		t.Fatal(err)
	}
	sess := store.Session{ProjectID: project.ID, Settings: datatypes.NewJSONType(store.SessionSettings{}), LastMessageTime: time.Now()}
	if err := db.Create(&sess).Error; err != nil {
		t.Fatal(err)
	}

	bodyCh := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"deepseek-flash"}]}`))
			return
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		bodyCh <- string(raw)
		payload, err := json.Marshal(map[string]any{
			"choices": []map[string]any{{
				"message":       map[string]any{"role": "assistant", "content": response},
				"finish_reason": "stop",
			}},
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	}))
	t.Cleanup(server.Close)

	pipeline := &Pipeline{llm: llm.NewWithBaseURL("test-key", server.URL), sessions: session.New(db)}
	return pipeline, db, sess, bodyCh
}

// compressionHistory returns four large messages: user, assistant, user,
// assistant. Each contributes 975 estimated units, so a 4000-token window
// triggers compression with a 640-unit retained tail.
func compressionHistory(sessionID uint) []store.ChatMessage {
	content := strings.Repeat("message body ", 150)
	roles := []string{ds4.RoleUser, ds4.RoleAssistant, ds4.RoleUser, ds4.RoleAssistant}
	messages := make([]store.ChatMessage, len(roles))
	for i, role := range roles {
		messages[i] = store.ChatMessage{ID: uint(i + 1), SessionID: sessionID, Role: role, Content: content}
	}
	return messages
}

func compressionTurn(activePath []store.ChatMessage) plugin.TurnContext {
	messages := append(append([]store.ChatMessage(nil), activePath...), store.ChatMessage{Role: ds4.RoleUser, Content: "pending question"})
	return plugin.TurnContext{SessionID: activePath[0].SessionID, Messages: messages, Metadata: map[string]any{}}
}

func TestCompressionSplitIndex(t *testing.T) {
	path := []store.ChatMessage{{ID: 10}, {ID: 20}, {ID: 30}}
	if got, ok := compressionSplitIndex(path, 20); !ok || got != 2 {
		t.Fatalf("compressionSplitIndex(path, 20) = %d, %v; want 2, true", got, ok)
	}
	if _, ok := compressionSplitIndex(path, 99); ok {
		t.Fatal("compressionSplitIndex accepted an id off the path")
	}
	if _, ok := compressionSplitIndex(nil, 10); ok {
		t.Fatal("compressionSplitIndex accepted an id for an empty path")
	}
}

func TestSelectRetainedTail(t *testing.T) {
	messages := []store.ChatMessage{
		{Content: strings.Repeat("a", 100)},
		{Content: strings.Repeat("b", 100)},
		{Content: strings.Repeat("c", 100)},
	}
	cases := []struct {
		name   string
		budget int
		want   int
	}{
		{"above total", 1000, 0},
		{"whole suffix", 100, 1},
		{"last message", 50, 2},
		{"non-positive", 0, len(messages)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := selectRetainedTail(messages, c.budget); got != c.want {
				t.Fatalf("selectRetainedTail(%d) = %d, want %d", c.budget, got, c.want)
			}
		})
	}
}

func TestAlignTailToToolBoundary(t *testing.T) {
	messages := []store.ChatMessage{
		{Role: ds4.RoleUser}, {Role: ds4.RoleAssistant}, {Role: ds4.RoleTool}, {Role: ds4.RoleTool}, {Role: ds4.RoleAssistant},
	}
	cases := []struct {
		name  string
		start int
		want  int
	}{
		{"safe cut", 4, 4},
		{"start of slice", 0, 0},
		{"end of slice", len(messages), len(messages)},
		{"second tool result", 3, 1},
		{"first tool result", 2, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := alignTailToToolBoundary(messages, c.start); got != c.want {
				t.Fatalf("alignTailToToolBoundary(%d) = %d, want %d", c.start, got, c.want)
			}
		})
	}

	orphan := []store.ChatMessage{{Role: ds4.RoleUser}, {Role: ds4.RoleTool}, {Role: ds4.RoleTool}}
	if got := alignTailToToolBoundary(orphan, 1); got != 0 {
		t.Fatalf("orphan tool chain = %d, want 0 (skip)", got)
	}
}

func TestMaybeCompress_RetainsTailAndStoresCheckpoint(t *testing.T) {
	checkpoint := "## Primary Request and Intent\n- Resume the task."
	pipeline, db, sess, bodyCh := compressionFixture(t, checkpoint)
	activePath := compressionHistory(sess.ID)
	turn := compressionTurn(activePath)

	out, err := pipeline.maybeCompress(context.Background(), sess, registry.Identity{ContextWindowTokens: 4000}, activePath, nil, turn, 0, len(activePath))
	if err != nil {
		t.Fatalf("maybeCompress: %v", err)
	}
	if len(out.Messages) != 3 {
		t.Fatalf("compressed messages = %d, want checkpoint+tail+pending", len(out.Messages))
	}
	if out.Messages[0].Role != ds4.RoleUser || !strings.Contains(out.Messages[0].Content, checkpoint) {
		t.Fatalf("first message is not the checkpoint: %+v", out.Messages[0])
	}
	if out.Messages[1].ID != activePath[3].ID || out.Messages[1].Content != activePath[3].Content {
		t.Fatalf("retained tail = %+v, want the raw last assistant message", out.Messages[1])
	}
	if out.Messages[2].Content != "pending question" {
		t.Fatalf("last message = %+v, want the pending user message", out.Messages[2])
	}

	var row store.Compression
	if err := db.Order("id desc").First(&row).Error; err != nil {
		t.Fatalf("load compression: %v", err)
	}
	if row.FirstMessageID != activePath[0].ID || row.LastMessageID != activePath[2].ID {
		t.Fatalf("coverage = %d..%d, want %d..%d", row.FirstMessageID, row.LastMessageID, activePath[0].ID, activePath[2].ID)
	}
	var stored []transform.Message
	if err := json.Unmarshal(row.Messages, &stored); err != nil {
		t.Fatalf("unpack stored checkpoint: %v", err)
	}
	if len(stored) != 1 || stored[0].Role != ds4.RoleUser {
		t.Fatalf("stored messages = %+v, want one user checkpoint", stored)
	}

	prompt := userPromptFromChatRequest(t, <-bodyCh)
	for _, want := range []string{"Messages to compress:", "message body", activePath[0].Content} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}

	replayed, err := pipeline.buildContext(&registry.Registry{}, store.SessionSettings{}, append(activePath, store.ChatMessage{ID: 5, Role: ds4.RoleUser, Content: "next"}), &row)
	if err != nil {
		t.Fatalf("buildContext: %v", err)
	}
	if len(replayed) != 3 {
		t.Fatalf("replayed messages = %d, want checkpoint+tail+next", len(replayed))
	}
	if replayed[0].Content != stored[0].Content || replayed[1].ID != activePath[3].ID || replayed[2].Content != "next" {
		t.Fatalf("unexpected replay: %+v", replayed)
	}
}

func TestMaybeCompress_MergesPriorCheckpoint(t *testing.T) {
	pipeline, db, sess, bodyCh := compressionFixture(t, "## Critical Context\n- merged")
	activePath := compressionHistory(sess.ID)
	priorJSON, err := json.Marshal([]transform.Message{{Role: ds4.RoleUser, Content: "PRIOR-CHECKPOINT-TEXT"}})
	if err != nil {
		t.Fatal(err)
	}
	priorRow, err := pipeline.sessions.StoreCompression(sess.ID, activePath[0].ID, activePath[1].ID, priorJSON)
	if err != nil {
		t.Fatalf("seed prior compression: %v", err)
	}
	turn := compressionTurn(activePath)

	out, err := pipeline.maybeCompress(context.Background(), sess, registry.Identity{ContextWindowTokens: 4000}, activePath, priorRow, turn, 0, len(activePath))
	if err != nil {
		t.Fatalf("maybeCompress: %v", err)
	}
	if len(out.Messages) != 3 {
		t.Fatalf("compressed messages = %d, want checkpoint+tail+pending", len(out.Messages))
	}

	var row store.Compression
	if err := db.Order("id desc").First(&row).Error; err != nil {
		t.Fatalf("load compression: %v", err)
	}
	if row.FirstMessageID != activePath[0].ID {
		t.Fatalf("first message id = %d, want inherited %d", row.FirstMessageID, activePath[0].ID)
	}
	if row.LastMessageID != activePath[2].ID {
		t.Fatalf("last message id = %d, want %d", row.LastMessageID, activePath[2].ID)
	}
	var stored []transform.Message
	if err := json.Unmarshal(row.Messages, &stored); err != nil {
		t.Fatalf("unpack stored checkpoint: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("stored messages = %d, want one consolidated checkpoint", len(stored))
	}

	prompt := userPromptFromChatRequest(t, <-bodyCh)
	if !strings.Contains(prompt, "PRIOR-CHECKPOINT-TEXT") {
		t.Fatalf("prompt does not include the prior checkpoint: %q", prompt)
	}
}

func TestMaybeCompress_SkipsNonShrinkingCheckpoint(t *testing.T) {
	pipeline, db, sess, _ := compressionFixture(t, strings.Repeat("oversized checkpoint ", 500))
	activePath := compressionHistory(sess.ID)
	turn := compressionTurn(activePath)

	out, err := pipeline.maybeCompress(context.Background(), sess, registry.Identity{ContextWindowTokens: 4000}, activePath, nil, turn, 0, len(activePath))
	if err != nil {
		t.Fatalf("maybeCompress: %v", err)
	}
	if len(out.Messages) != len(turn.Messages) {
		t.Fatalf("messages changed despite a non-shrinking checkpoint: %d -> %d", len(turn.Messages), len(out.Messages))
	}
	var count int64
	if err := db.Model(&store.Compression{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("stored %d compression rows, want none", count)
	}
}

func TestMaybeCompress_IgnoresPriorOffPath(t *testing.T) {
	pipeline, db, sess, bodyCh := compressionFixture(t, "## Current Work\n- consolidated")
	activePath := compressionHistory(sess.ID)
	priorJSON, err := json.Marshal([]transform.Message{{Role: ds4.RoleUser, Content: "PRIOR-CHECKPOINT-TEXT"}})
	if err != nil {
		t.Fatal(err)
	}
	priorRow := &store.Compression{ID: 99, SessionID: sess.ID, FirstMessageID: 1, LastMessageID: 99, Messages: priorJSON}
	turn := compressionTurn(activePath)

	if _, err := pipeline.maybeCompress(context.Background(), sess, registry.Identity{ContextWindowTokens: 4000}, activePath, priorRow, turn, 0, len(activePath)); err != nil {
		t.Fatalf("maybeCompress: %v", err)
	}

	var row store.Compression
	if err := db.Order("id desc").First(&row).Error; err != nil {
		t.Fatalf("load compression: %v", err)
	}
	if row.FirstMessageID != activePath[0].ID {
		t.Fatalf("first message id = %d, want current path start %d", row.FirstMessageID, activePath[0].ID)
	}
	prompt := userPromptFromChatRequest(t, <-bodyCh)
	if strings.Contains(prompt, "PRIOR-CHECKPOINT-TEXT") {
		t.Fatalf("prompt merged a prior checkpoint that is off the path: %q", prompt)
	}
}

func TestMaybeCompress_PreservesPrefixAndSuffixAroundCheckpoint(t *testing.T) {
	checkpoint := "## Primary Request and Intent\n- Resume the task."
	pipeline, _, sess, _ := compressionFixture(t, checkpoint)
	activePath := compressionHistory(sess.ID)
	static := []store.ChatMessage{
		{Role: ds4.RoleSystem, Content: "identity"},
		{Role: ds4.RoleSystem, Content: "environment"},
	}
	injected := store.ChatMessage{Role: ds4.RoleUser, Content: "injected"}
	pending := store.ChatMessage{Role: ds4.RoleUser, Content: "pending question"}
	turn := plugin.TurnContext{
		SessionID: sess.ID,
		Messages:  append(append(append(append([]store.ChatMessage{}, static...), activePath...), injected), pending),
		Metadata:  map[string]any{},
	}

	out, err := pipeline.maybeCompress(context.Background(), sess, registry.Identity{ContextWindowTokens: 4000}, activePath, nil, turn, len(static), len(static)+len(activePath))
	if err != nil {
		t.Fatalf("maybeCompress: %v", err)
	}
	if len(out.Messages) != len(static)+4 {
		t.Fatalf("compressed messages = %d, want static+checkpoint+tail+injected+pending", len(out.Messages))
	}
	for i := range static {
		if out.Messages[i].Role != static[i].Role || out.Messages[i].Content != static[i].Content {
			t.Fatalf("static message %d changed: %+v", i, out.Messages[i])
		}
	}
	if !strings.Contains(out.Messages[len(static)].Content, checkpoint) {
		t.Fatalf("checkpoint not placed after the prefix: %+v", out.Messages[len(static)])
	}
	if out.Messages[len(out.Messages)-2].Content != injected.Content || out.Messages[len(out.Messages)-1].Content != pending.Content {
		t.Fatalf("suffix not preserved: %+v", out.Messages)
	}
}

func TestCompactHistory_ManualCompactsBelowAutoThreshold(t *testing.T) {
	checkpoint := "## Current Work\n- compacted manually"
	pipeline, db, sess, _ := compressionFixture(t, checkpoint)
	activePath := compressionHistory(sess.ID)

	// Four 975-unit messages total 3900 units: below 80% of an 8000-unit
	// window, so only the manual trigger can fire.
	result, err := pipeline.compactHistory(context.Background(), sess, registry.Identity{ContextWindowTokens: 8000}, activePath, nil, 3900, compactionManual)
	if err != nil {
		t.Fatalf("compactHistory: %v", err)
	}
	if !result.Compacted {
		t.Fatalf("manual compaction skipped: %s", result.Reason)
	}
	if result.AfterUnits >= result.BeforeUnits {
		t.Fatalf("after = %d, want less than before = %d", result.AfterUnits, result.BeforeUnits)
	}

	var row store.Compression
	if err := db.Order("id desc").First(&row).Error; err != nil {
		t.Fatalf("load compression: %v", err)
	}
	if row.LastMessageID != activePath[2].ID {
		t.Fatalf("coverage ends at %d, want %d (retained tail starts at the last message)", row.LastMessageID, activePath[2].ID)
	}
}

func TestCompactHistory_ManualSkipsSmallRecentlyCompactedContext(t *testing.T) {
	pipeline, db, sess, _ := compressionFixture(t, "## Critical Context\n- stale")
	activePath := compressionHistory(sess.ID)
	priorJSON, err := json.Marshal([]transform.Message{{Role: ds4.RoleUser, Content: "PRIOR"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pipeline.sessions.StoreCompression(sess.ID, activePath[0].ID, activePath[1].ID, priorJSON); err != nil {
		t.Fatalf("seed prior compression: %v", err)
	}
	if err := db.First(&sess, sess.ID).Error; err != nil {
		t.Fatalf("reload session: %v", err)
	}
	prior, err := pipeline.sessions.ResolveCompression(&sess, activePath)
	if err != nil || prior == nil {
		t.Fatalf("resolve prior compression: %v, %+v", err, prior)
	}

	// 15% of an 8000-unit window is 1200 units; a smaller context with a
	// recent compaction is a no-op.
	result, err := pipeline.compactHistory(context.Background(), sess, registry.Identity{ContextWindowTokens: 8000}, activePath, prior, 1000, compactionManual)
	if err != nil {
		t.Fatalf("compactHistory: %v", err)
	}
	if result.Compacted || result.Reason == "" {
		t.Fatalf("expected a reasoned skip, got %+v", result)
	}
	var count int64
	if err := db.Model(&store.Compression{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("stored %d compression rows, want only the seeded one", count)
	}
}

// userPromptFromChatRequest extracts the user prompt from a captured
// chat-completions request body.
func userPromptFromChatRequest(t *testing.T, body string) string {
	t.Helper()
	var request struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &request); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	for _, m := range request.Messages {
		if m.Role == ds4.RoleUser {
			return m.Content
		}
	}
	t.Fatal("request has no user message")
	return ""
}
