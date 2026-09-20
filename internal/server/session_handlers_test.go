package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/Cyvadra/hephaestus/internal/chat"
	"github.com/Cyvadra/hephaestus/internal/chatrun"
	"github.com/Cyvadra/hephaestus/internal/command"
	"github.com/Cyvadra/hephaestus/internal/interaction"
	"github.com/Cyvadra/hephaestus/internal/project"
	"github.com/Cyvadra/hephaestus/internal/registry"
	"github.com/Cyvadra/hephaestus/internal/session"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

const (
	authorizationTestProject   = "authorization-test"
	authorizationTestConcierge = "authorization-test-concierge"
)

// newTestStore opens an in-memory database migrated for models, and a project
// service rooted in a temporary directory, for handler tests in this package.
func newTestStore(t *testing.T, models ...any) (*gorm.DB, *project.Service) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(models...); err != nil {
		t.Fatal(err)
	}
	projects, err := project.New(db, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	return db, projects
}

// newSessionTestServer builds a server backed by an in-memory database with one
// project that allows one Concierge.
func newSessionTestServer(t *testing.T) *Server {
	t.Helper()
	db, projects := newTestStore(t,
		&store.Project{}, &store.Session{}, &store.ChatMessage{}, &store.MessageAttachment{},
		&store.ChatRun{}, &store.ChatRunEvent{},
	)
	bound, err := projects.Create(authorizationTestProject, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := projects.SetConciergeAvailability(authorizationTestConcierge, []string{bound.Name}); err != nil {
		t.Fatal(err)
	}
	registries := registry.NewStore(&registry.Registry{
		Identities: map[string]registry.Identity{"identity": {Name: "identity", ReasoningEffort: registry.ReasoningHigh}},
		Concierges: map[string]registry.Concierge{authorizationTestConcierge: {
			Name:              authorizationTestConcierge,
			Identity:          "identity",
			ToolGroups:        []string{"basic", "web"},
			DefaultToolGroups: []string{"basic"},
		}},
	})
	sessions := session.New(db)
	return &Server{
		registries: registries,
		sessions:   sessions,
		projects:   projects,
		commands:   command.NewService(registries, nil, nil, sessions, nil, db, projects, interaction.NewManager()),
		chatRuns:   chatrun.New(db),
	}
}

func (s *Server) createTestSession(t *testing.T, body string) store.Session {
	t.Helper()
	ctx, recorder := newRunContext(http.MethodPost, "/sessions", body, nil)
	s.createSession(ctx)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create session: status %d body %s", recorder.Code, recorder.Body.String())
	}
	var created store.Session
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created session: %v", err)
	}
	return created
}

func sessionParams(id uint) gin.Params {
	return gin.Params{{Key: "id", Value: strconv.FormatUint(uint64(id), 10)}}
}

// TestCreateSessionSeedsAutoApprove covers a client choosing "allow all" before
// the session exists: the policy has to be in place from the very first turn,
// and it has to be reported back when the client reopens the session.
func TestCreateSessionSeedsAutoApprove(t *testing.T) {
	server := newSessionTestServer(t)
	created := server.createTestSession(t, `{"concierge":"`+authorizationTestConcierge+`","project":"`+authorizationTestProject+`","auto_approve":true}`)

	if !server.commands.AutoApprove(created.ID) {
		t.Fatal("expected a session created with auto_approve to start with automatic approval enabled")
	}

	ctx, recorder := newRunContext(http.MethodGet, "/sessions/"+strconv.FormatUint(uint64(created.ID), 10)+"/history", "", sessionParams(created.ID))
	server.getHistory(ctx)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"auto_approve":true`) {
		t.Fatalf("history should report the seeded policy: status %d body %s", recorder.Code, recorder.Body.String())
	}
}

// TestCreateSessionDefaultsToNoAutoApprove guards the other half of the
// contract: omitting the field must leave the session asking for permission.
func TestCreateSessionDefaultsToNoAutoApprove(t *testing.T) {
	server := newSessionTestServer(t)
	created := server.createTestSession(t, `{"concierge":"`+authorizationTestConcierge+`","project":"`+authorizationTestProject+`"}`)

	if server.commands.AutoApprove(created.ID) {
		t.Fatal("expected automatic approval to stay off when the request omits it")
	}
}

// TestAuthorizationChangeIsAcceptedWhileARunIsActive covers the in-flight case:
// the composer may switch authorization while a generation is streaming, so the
// message endpoint must route the change into the interaction manager instead
// of rejecting the session as busy.
func TestAuthorizationChangeIsAcceptedWhileARunIsActive(t *testing.T) {
	server := newSessionTestServer(t)
	created := server.createTestSession(t, `{"concierge":"`+authorizationTestConcierge+`","project":"`+authorizationTestProject+`"}`)

	release := make(chan struct{})
	executing := make(chan struct{})
	if _, err := server.chatRuns.Start(created.ID, created.ProjectID, store.ChatRunMessage, map[string]any{"text": "hello"}, func(ctx context.Context, _ func(chat.StreamEvent)) (*chatrun.Result, error) {
		close(executing)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return &chatrun.Result{}, nil
	}); err != nil {
		t.Fatalf("start chat run: %v", err)
	}
	defer func() {
		close(release)
		server.chatRuns.Shutdown()
	}()
	<-executing

	ctx, recorder := newRunContext(http.MethodPost, "/sessions/"+strconv.FormatUint(uint64(created.ID), 10)+"/messages", `{"text":"/interact auto-approve"}`, sessionParams(created.ID))
	server.sendMessage(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("authorization change during a run: status %d body %s", recorder.Code, recorder.Body.String())
	}
	if !server.commands.AutoApprove(created.ID) {
		t.Fatal("expected the authorization change to be applied while the run is active")
	}
}
