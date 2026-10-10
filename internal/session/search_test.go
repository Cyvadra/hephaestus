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

func TestParseSearchQuery(t *testing.T) {
	cases := []struct {
		query string
		mode  SearchMode
		terms int
	}{
		{"rocket", SearchModePhrase, 1},
		{"rocket engine", SearchModeKeywords, 2},
		{"a, b,c d", SearchModeKeywords, 4},
		{"a b c d e", SearchModePhrase, 1},      // 4 delimiters
		{"rocket engine.", SearchModePhrase, 1}, // punctuation
		{"火箭 引擎", SearchModeKeywords, 2},
		{"火箭，引擎", SearchModePhrase, 1},                        // full-width comma is punctuation
		{strings.Repeat("x", 48) + " y", SearchModePhrase, 1}, // >= 50 runes
	}
	for _, tc := range cases {
		got := ParseSearchQuery(tc.query)
		if got.Mode != tc.mode || len(got.Terms) != tc.terms {
			t.Errorf("ParseSearchQuery(%q) = %s/%d terms, want %s/%d", tc.query, got.Mode, len(got.Terms), tc.mode, tc.terms)
		}
	}
}

func TestSearchMatchesSimplifiedAndTraditional(t *testing.T) {
	db := newSearchTestDB(t)
	svc := New(db)
	project := newSearchTestProject(t, db, "project-a")
	traditional := newSearchTestSession(t, db, project.ID, "簡體與繁體", "")
	simplified := newSearchTestSession(t, db, project.ID, "简体测试", "")
	for _, query := range []string{"简体", "簡體"} {
		results, err := svc.SearchByTitle(project.ID, query, 10)
		if err != nil {
			t.Fatal(err)
		}
		ids := map[uint]bool{}
		for _, r := range results {
			ids[r.ID] = true
		}
		if len(results) != 2 || !ids[traditional.ID] || !ids[simplified.ID] {
			t.Errorf("query %q: got %+v", query, results)
		}
	}
	if err := db.Create(&store.ChatMessage{SessionID: simplified.ID, Role: "user", Content: "這是繁體訊息", Timestamp: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	results, err := svc.SearchMessages(project.ID, "这是繁体", 10, 0)
	if err != nil || len(results) != 1 || !strings.Contains(results[0].Snippet, "這是繁體") {
		t.Fatalf("results = %+v, err = %v", results, err)
	}
}

func TestSearchKeywordsRequiresAllKeywordsPerSession(t *testing.T) {
	db := newSearchTestDB(t)
	svc := New(db)
	project := newSearchTestProject(t, db, "project-a")
	both := newSearchTestSession(t, db, project.ID, "Rocket notes", "")
	onlyOne := newSearchTestSession(t, db, project.ID, "Rocket only", "")
	titleOnly := newSearchTestSession(t, db, project.ID, "rocket engine summary", "rocket engine")
	now := time.Now()
	for _, msg := range []store.ChatMessage{
		{SessionID: both.ID, Role: "user", Content: "the engine stalled", Timestamp: now},
		{SessionID: onlyOne.ID, Role: "user", Content: "nothing relevant", Timestamp: now},
	} {
		if err := db.Create(&msg).Error; err != nil {
			t.Fatal(err)
		}
	}

	results, err := svc.SearchMessages(project.ID, "rocket, engine", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := map[uint]MessageSearchResult{}
	for _, r := range results {
		got[r.SessionID] = r
	}
	if len(results) != 2 || got[both.ID].MessageID == 0 || !strings.Contains(got[both.ID].Snippet, "engine") {
		t.Fatalf("results = %+v", results)
	}
	if r, ok := got[titleOnly.ID]; !ok || r.MessageID != 0 {
		t.Fatalf("title-only match = %+v", r)
	}

	sessions, err := svc.SearchByTitle(project.ID, "engine rocket", 10)
	if err != nil || len(sessions) != 1 || sessions[0].ID != titleOnly.ID {
		t.Fatalf("sessions = %+v, err = %v", sessions, err)
	}
}
