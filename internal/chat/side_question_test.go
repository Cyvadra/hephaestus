package chat

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Cyvadra/ds4"
	"github.com/Cyvadra/hephaestus/internal/llm"
	"github.com/Cyvadra/hephaestus/internal/notify"
	"github.com/Cyvadra/hephaestus/internal/plugin"
	"github.com/Cyvadra/hephaestus/internal/project"
	"github.com/Cyvadra/hephaestus/internal/registry"
	"github.com/Cyvadra/hephaestus/internal/session"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/glebarez/sqlite"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type sideQuestionFixture struct {
	pipeline *Pipeline
	db       *gorm.DB
	sessions *session.Service
	sess     store.Session

	// Branch B is the active leaf.
	branchBLeaf store.ChatMessage
	// branchALeaf is a sibling branch that the session does not point at.
	branchALeaf store.ChatMessage
	// incompleteLeaf sits on a branch whose newest assistant was cut off.
	incompleteLeaf store.ChatMessage

	mu       sync.Mutex
	requests []ds4.ChatRequest
}

func newSideQuestionFixture(t *testing.T) *sideQuestionFixture {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&store.Project{},
		&store.Session{},
		&store.ChatMessage{},
		&store.MessageAttachment{},
		&store.Compression{},
		&store.ChatRun{},
		&store.ChatRunEvent{},
		&store.PluginState{},
		&store.ToolAudit{},
	); err != nil {
		t.Fatal(err)
	}

	proj := store.Project{Name: "btw-test"}
	if err := db.Create(&proj).Error; err != nil {
		t.Fatal(err)
	}
	projects, err := project.New(db, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	sess := store.Session{
		ProjectID:       proj.ID,
		Settings:        datatypes.NewJSONType(store.SessionSettings{Identity: "default"}),
		LastMessageTime: time.Now(),
	}
	if err := db.Create(&sess).Error; err != nil {
		t.Fatal(err)
	}

	create := func(parent *uint, role, content, status string) store.ChatMessage {
		t.Helper()
		message := store.ChatMessage{
			SessionID:       sess.ID,
			ParentMessageID: parent,
			Timestamp:       time.Now(),
			Role:            role,
			Content:         content,
			Status:          status,
		}
		if err := db.Create(&message).Error; err != nil {
			t.Fatal(err)
		}
		return message
	}

	firstUser := create(nil, ds4.RoleUser, "first question", "")
	firstAssistant := create(&firstUser.ID, ds4.RoleAssistant, "first answer", store.MessageStatusComplete)

	// Branch A: a complete sibling branch.
	branchAUser := create(&firstAssistant.ID, ds4.RoleUser, "branch A question", "")
	branchALeaf := create(&branchAUser.ID, ds4.RoleAssistant, "branch A answer", store.MessageStatusComplete)

	// Branch B: the session's active leaf.
	branchBUser := create(&firstAssistant.ID, ds4.RoleUser, "branch B question", "")
	branchBLeaf := create(&branchBUser.ID, ds4.RoleAssistant, "branch B answer", store.MessageStatusComplete)

	// Interrupted branch: the newest assistant never completed.
	incompleteUser := create(&firstAssistant.ID, ds4.RoleUser, "second question", "")
	incompleteLeaf := create(&incompleteUser.ID, ds4.RoleAssistant, "incomplete answer", store.MessageStatusIncomplete)

	if err := db.Model(&sess).Update("active_leaf_message_id", branchBLeaf.ID).Error; err != nil {
		t.Fatal(err)
	}
	sess.ActiveLeafMessageID = &branchBLeaf.ID

	fx := &sideQuestionFixture{
		db:             db,
		sessions:       session.New(db),
		sess:           sess,
		branchALeaf:    branchALeaf,
		branchBLeaf:    branchBLeaf,
		incompleteLeaf: incompleteLeaf,
	}

	registries := registry.NewStore(&registry.Registry{
		Identities: map[string]registry.Identity{
			"default": {Name: "default", SystemPrompt: "You are a test identity."},
		},
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"deepseek-flash"}]}`))
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			return
		}
		var request ds4.ChatRequest
		if err := json.Unmarshal(body, &request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		fx.mu.Lock()
		fx.requests = append(fx.requests, request)
		fx.mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"side \"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\n"))
		w.(http.Flusher).Flush()
	}))
	t.Cleanup(server.Close)

	fx.pipeline = &Pipeline{
		db:         db,
		registries: registries,
		plugins:    plugin.NewRegistry(notify.New("")),
		llm:        llm.NewWithBaseURL("test-key", server.URL),
		sessions:   fx.sessions,
		projects:   projects,
	}
	return fx
}

func (fx *sideQuestionFixture) lastRequest(t *testing.T) ds4.ChatRequest {
	t.Helper()
	fx.mu.Lock()
	defer fx.mu.Unlock()
	if len(fx.requests) == 0 {
		t.Fatal("no request captured")
	}
	return fx.requests[len(fx.requests)-1]
}

// conversationContents returns the contents of every non-system, non-tool
// message in request order.
func conversationContents(request ds4.ChatRequest) []string {
	var out []string
	for _, message := range request.Messages {
		if message.Role == ds4.RoleSystem || message.Role == ds4.RoleTool {
			continue
		}
		out = append(out, message.Content)
	}
	return out
}

func assertRequestContents(t *testing.T, request ds4.ChatRequest, want []string, question string) {
	t.Helper()
	if len(request.Tools) != 0 {
		t.Fatalf("side question request included %d tools", len(request.Tools))
	}
	got := conversationContents(request)
	wantWithQuestion := append(append([]string(nil), want...), sideQuestionPrompt(question))
	if !reflect.DeepEqual(got, wantWithQuestion) {
		t.Fatalf("request contents = %q, want %q", got, wantWithQuestion)
	}
}

func TestSideQuestionUsesContextUpToLastCompleteAssistant(t *testing.T) {
	fx := newSideQuestionFixture(t)

	before := reloadSessionForBtw(t, fx.db, fx.sess.ID)
	countsBefore := btwRowCounts(t, fx.db)

	var deltas []string
	answer, err := fx.pipeline.SideQuestion(context.Background(), fx.sess.ID, "why?", nil, false, func(delta string) {
		deltas = append(deltas, delta)
	})
	if err != nil {
		t.Fatalf("SideQuestion() error = %v", err)
	}
	if answer != "side answer" {
		t.Fatalf("SideQuestion() = %q, want %q", answer, "side answer")
	}
	if strings.Join(deltas, "") != "side answer" {
		t.Fatalf("streamed deltas = %q", strings.Join(deltas, ""))
	}

	request := fx.lastRequest(t)
	assertRequestContents(t, request, []string{
		"first question",
		"first answer",
		"branch B question",
		"branch B answer",
	}, "why?")
	for _, forbidden := range []string{"branch A question", "branch A answer", "second question", "incomplete answer"} {
		if strings.Contains(strings.Join(conversationContents(request), "\n"), forbidden) {
			t.Fatalf("request included off-context message %q", forbidden)
		}
	}
	if len(request.Messages) > 0 && request.Messages[0].Role != ds4.RoleSystem {
		t.Fatalf("first request message role = %q, want system", request.Messages[0].Role)
	}

	if after := btwRowCounts(t, fx.db); !reflect.DeepEqual(countsBefore, after) {
		t.Fatalf("side question wrote rows: %+v -> %+v", countsBefore, after)
	}
	if after := reloadSessionForBtw(t, fx.db, fx.sess.ID); !reflect.DeepEqual(before, after) {
		t.Fatalf("side question changed the session:\nbefore %+v\nafter  %+v", before, after)
	}
}

func TestSideQuestionSelectsSuppliedLeaf(t *testing.T) {
	fx := newSideQuestionFixture(t)

	if _, err := fx.pipeline.SideQuestion(context.Background(), fx.sess.ID, "why?", &fx.branchALeaf.ID, false, nil); err != nil {
		t.Fatalf("SideQuestion() error = %v", err)
	}
	assertRequestContents(t, fx.lastRequest(t), []string{
		"first question",
		"first answer",
		"branch A question",
		"branch A answer",
	}, "why?")
}

func TestSideQuestionExcludesIncompleteTrailingTurn(t *testing.T) {
	fx := newSideQuestionFixture(t)

	if _, err := fx.pipeline.SideQuestion(context.Background(), fx.sess.ID, "what happened?", &fx.incompleteLeaf.ID, false, nil); err != nil {
		t.Fatalf("SideQuestion() error = %v", err)
	}
	assertRequestContents(t, fx.lastRequest(t), []string{
		"first question",
		"first answer",
	}, "what happened?")
}

func TestSideQuestionSelectRootUsesOnlyStaticContext(t *testing.T) {
	fx := newSideQuestionFixture(t)

	if _, err := fx.pipeline.SideQuestion(context.Background(), fx.sess.ID, "what is this?", nil, true, nil); err != nil {
		t.Fatalf("SideQuestion() error = %v", err)
	}
	assertRequestContents(t, fx.lastRequest(t), nil, "what is this?")
}

func TestSideQuestionRejectsEmptyQuestion(t *testing.T) {
	fx := newSideQuestionFixture(t)

	if _, err := fx.pipeline.SideQuestion(context.Background(), fx.sess.ID, "   ", nil, false, nil); err == nil {
		t.Fatal("expected an error for a blank question")
	}
	fx.mu.Lock()
	defer fx.mu.Unlock()
	if len(fx.requests) != 0 {
		t.Fatalf("blank question issued %d LLM requests", len(fx.requests))
	}
}

func reloadSessionForBtw(t *testing.T, db *gorm.DB, id uint) store.Session {
	t.Helper()
	var sess store.Session
	if err := db.First(&sess, id).Error; err != nil {
		t.Fatal(err)
	}
	return sess
}

type btwCounts struct {
	messages     int64
	runs         int64
	runEvents    int64
	compressions int64
	pluginStates int64
	toolAudits   int64
}

func btwRowCounts(t *testing.T, db *gorm.DB) btwCounts {
	t.Helper()
	count := func(model any) int64 {
		var n int64
		if err := db.Model(model).Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		return n
	}
	return btwCounts{
		messages:     count(&store.ChatMessage{}),
		runs:         count(&store.ChatRun{}),
		runEvents:    count(&store.ChatRunEvent{}),
		compressions: count(&store.Compression{}),
		pluginStates: count(&store.PluginState{}),
		toolAudits:   count(&store.ToolAudit{}),
	}
}
