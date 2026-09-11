package session

import (
	"strings"
	"testing"
	"time"

	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/glebarez/sqlite"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

func newSearchTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&store.Project{}, &store.Session{}, &store.ChatMessage{}, &store.MessageAttachment{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func newSearchTestProject(t *testing.T, db *gorm.DB, name string) store.Project {
	t.Helper()
	p := store.Project{Name: name}
	if err := db.Create(&p).Error; err != nil {
		t.Fatal(err)
	}
	return p
}

func newSearchTestSession(t *testing.T, db *gorm.DB, projectID uint, title, summary string) store.Session {
	t.Helper()
	sess := store.Session{
		ProjectID:       projectID,
		Settings:        datatypes.NewJSONType(store.SessionSettings{}),
		Title:           title,
		Summary:         summary,
		LastMessageTime: time.Now(),
	}
	if err := db.Create(&sess).Error; err != nil {
		t.Fatal(err)
	}
	return sess
}

func TestSearchByTitleMatchesTitleAndSummary(t *testing.T) {
	db := newSearchTestDB(t)
	svc := New(db)
	projectA := newSearchTestProject(t, db, "project-a")
	projectB := newSearchTestProject(t, db, "project-b")

	byTitle := newSearchTestSession(t, db, projectA.ID, "Debugging the Rocket Engine", "")
	bySummary := newSearchTestSession(t, db, projectA.ID, "Unrelated Chat", "Discussed rocket fuel mixtures")
	newSearchTestSession(t, db, projectA.ID, "Cooking Pasta", "How to boil water")
	// Same title, different project: must not leak across project scope.
	newSearchTestSession(t, db, projectB.ID, "Rocket Engine Notes", "")

	results, err := svc.SearchByTitle(projectA.ID, "rocket", 10)
	if err != nil {
		t.Fatalf("SearchByTitle: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d: %+v", len(results), results)
	}
	gotIDs := map[uint]bool{results[0].ID: true, results[1].ID: true}
	if !gotIDs[byTitle.ID] || !gotIDs[bySummary.ID] {
		t.Errorf("expected sessions %d and %d, got %v", byTitle.ID, bySummary.ID, gotIDs)
	}
}

func TestSearchByTitleEmptyQuery(t *testing.T) {
	db := newSearchTestDB(t)
	svc := New(db)
	project := newSearchTestProject(t, db, "project-a")
	newSearchTestSession(t, db, project.ID, "Some title", "")

	results, err := svc.SearchByTitle(project.ID, "   ", 10)
	if err != nil {
		t.Fatalf("SearchByTitle: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected no results for blank query, got %d", len(results))
	}
}

func TestSearchMessagesScopesToProjectAndPaginates(t *testing.T) {
	db := newSearchTestDB(t)
	svc := New(db)
	projectA := newSearchTestProject(t, db, "project-a")
	projectB := newSearchTestProject(t, db, "project-b")

	sessA := newSearchTestSession(t, db, projectA.ID, "Session A", "")
	sessB := newSearchTestSession(t, db, projectB.ID, "Session B", "")

	base := time.Now()
	for i := range 3 {
		msg := store.ChatMessage{
			SessionID: sessA.ID,
			Role:      "user",
			Content:   "please review the widget factory design",
			Timestamp: base.Add(time.Duration(i) * time.Minute),
		}
		if err := db.Create(&msg).Error; err != nil {
			t.Fatal(err)
		}
	}
	// Different project: must not leak into results.
	if err := db.Create(&store.ChatMessage{SessionID: sessB.ID, Role: "user", Content: "widget factory", Timestamp: base}).Error; err != nil {
		t.Fatal(err)
	}
	// Same project, no match.
	if err := db.Create(&store.ChatMessage{SessionID: sessA.ID, Role: "user", Content: "totally unrelated", Timestamp: base}).Error; err != nil {
		t.Fatal(err)
	}

	all, err := svc.SearchMessages(projectA.ID, "widget", 200, 0)
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 matches scoped to project A, got %d: %+v", len(all), all)
	}
	for _, result := range all {
		if result.SessionID != sessA.ID {
			t.Errorf("result leaked session from another project: %+v", result)
		}
		if result.SessionTitle != "Session A" {
			t.Errorf("expected session title %q, got %q", "Session A", result.SessionTitle)
		}
	}

	page, err := svc.SearchMessages(projectA.ID, "widget", 2, 0)
	if err != nil {
		t.Fatalf("SearchMessages page 1: %v", err)
	}
	if len(page) != 2 {
		t.Fatalf("expected 2 results with limit=2, got %d", len(page))
	}
	rest, err := svc.SearchMessages(projectA.ID, "widget", 2, 2)
	if err != nil {
		t.Fatalf("SearchMessages page 2: %v", err)
	}
	if len(rest) != 1 {
		t.Fatalf("expected 1 remaining result with offset=2, got %d", len(rest))
	}
}

func TestSnippetAroundCentersOnMatch(t *testing.T) {
	content := "start " + strings.Repeat("padding ", 40) + "NEEDLE " + strings.Repeat("padding ", 40) + "end"
	snippet := snippetAround(content, "NEEDLE", 40)
	if !strings.Contains(snippet, "NEEDLE") {
		t.Fatalf("expected snippet to contain the match, got %q", snippet)
	}
	if len([]rune(snippet)) > 40+len(" … … ") {
		t.Fatalf("snippet exceeds expected bound: %q", snippet)
	}
}
