package bootstrap

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestSplitCommaSeparated(t *testing.T) {
	got := splitCommaSeparated(" session_summary,options, ,storyline_status ")
	want := []string{"session_summary", "options", "storyline_status"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("splitCommaSeparated() = %v, want %v", got, want)
	}
}

func TestGetenvBoolAcceptsStandardTrueValues(t *testing.T) {
	t.Setenv("HEPHAESTUS_TEST_BOOL", "1")
	if !getenvBool("HEPHAESTUS_TEST_BOOL") {
		t.Fatal("expected ParseBool-compatible true value")
	}
}

func TestExpandHomePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := expandHomePath("~/Documents/hephaestus-projects")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "Documents", "hephaestus-projects")
	if got != want {
		t.Fatalf("expandHomePath() = %q, want %q", got, want)
	}
}

func TestLoadSerpAPIConfig(t *testing.T) {
	t.Setenv("HEPHAESTUS_POSTGRES_DSN", "test-dsn")
	t.Setenv("HEPHAESTUS_DEEPSEEK_API_KEY", "test-key")
	t.Setenv("HEPHAESTUS_WEB_SEARCH_SERPAPI_API_KEYS", " first, second , ")
	t.Setenv("HEPHAESTUS_WEB_SEARCH_SERPAPI_ENGINE", "")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"first", "second"}; !reflect.DeepEqual(cfg.WebSearchSerpAPIKeys, want) {
		t.Fatalf("SerpApi keys = %v, want %v", cfg.WebSearchSerpAPIKeys, want)
	}
	if cfg.WebSearchSerpAPIEngine != "google_light" {
		t.Fatalf("SerpApi engine = %q, want google_light", cfg.WebSearchSerpAPIEngine)
	}

	t.Setenv("HEPHAESTUS_WEB_SEARCH_SERPAPI_ENGINE", "bing")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WebSearchSerpAPIEngine != "bing" {
		t.Fatalf("SerpApi engine = %q, want bing", cfg.WebSearchSerpAPIEngine)
	}
}
