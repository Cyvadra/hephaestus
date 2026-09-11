package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Cyvadra/hephaestus/internal/project"
	"github.com/Cyvadra/hephaestus/internal/session"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

func newSearchTestServer(t *testing.T) (*Server, store.Project) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&store.Project{}, &store.Session{}, &store.ChatMessage{}, &store.MessageAttachment{}); err != nil {
		t.Fatal(err)
	}
	projects, err := project.New(db, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, err := projects.Create("search-test", "")
	if err != nil {
		t.Fatal(err)
	}

	sess := store.Session{
		ProjectID:       p.ID,
		Settings:        datatypes.NewJSONType(store.SessionSettings{}),
		Title:           "Rocket Launch Planning",
		LastMessageTime: time.Now(),
	}
	if err := db.Create(&sess).Error; err != nil {
		t.Fatal(err)
	}
	msg := store.ChatMessage{SessionID: sess.ID, Role: "user", Content: "when is the next rocket launch window", Timestamp: time.Now()}
	if err := db.Create(&msg).Error; err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	return &Server{projects: projects, sessions: session.New(db)}, *p
}

func TestSearchSessionsRequiresQuery(t *testing.T) {
	server, _ := newSearchTestServer(t)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/search/sessions?project=search-test", nil)

	server.searchSessions(c)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var results []store.Session
	if err := json.Unmarshal(recorder.Body.Bytes(), &results); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected no results for empty query, got %d", len(results))
	}
}

func TestSearchSessionsMatchesTitle(t *testing.T) {
	server, p := newSearchTestServer(t)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/search/sessions?project="+p.Name+"&q=rocket", nil)

	server.searchSessions(c)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var results []store.Session
	if err := json.Unmarshal(recorder.Body.Bytes(), &results); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(results) != 1 || results[0].Title != "Rocket Launch Planning" {
		t.Fatalf("unexpected results: %+v", results)
	}
}

func TestSearchSessionsUnknownProject(t *testing.T) {
	server, _ := newSearchTestServer(t)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/search/sessions?project=does-not-exist&q=rocket", nil)

	server.searchSessions(c)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestSearchMessagesMatchesContent(t *testing.T) {
	server, p := newSearchTestServer(t)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/search/messages?project="+p.Name+"&q=launch+window", nil)

	server.searchMessages(c)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var results []session.MessageSearchResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &results); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(results) != 1 || results[0].SessionTitle != "Rocket Launch Planning" {
		t.Fatalf("unexpected results: %+v", results)
	}
}
