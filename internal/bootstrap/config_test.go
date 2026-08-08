package bootstrap

import (
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

func TestLoadSerpAPIConfig(t *testing.T) {
	t.Setenv("HEPHAESTUS_POSTGRES_DSN", "test-dsn")
	t.Setenv("HEPHAESTUS_DEEPSEEK_API_KEY", "test-key")
	t.Setenv("HEPHAESTUS_FIRECRAWL_API_KEY", "firecrawl-key")
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

func TestLoadWebFetchConfig(t *testing.T) {
	t.Setenv("HEPHAESTUS_POSTGRES_DSN", "test-dsn")
	t.Setenv("HEPHAESTUS_DEEPSEEK_API_KEY", "test-key")
	t.Setenv("HEPHAESTUS_FIRECRAWL_API_KEY", "firecrawl-key")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WebFetchProvider != "firecrawl" || cfg.FirecrawlAPIKey != "firecrawl-key" {
		t.Fatalf("unexpected web fetch config: %+v", cfg)
	}

	t.Setenv("HEPHAESTUS_WEB_FETCH_PROVIDER", "local")
	t.Setenv("HEPHAESTUS_FIRECRAWL_API_KEY", "")
	if _, err := Load(); err != nil {
		t.Fatalf("local provider should not require Firecrawl key: %v", err)
	}

	t.Setenv("HEPHAESTUS_WEB_FETCH_PROVIDER", "invalid")
	if _, err := Load(); err == nil {
		t.Fatal("expected invalid web fetch provider error")
	}
}

func TestLoadRequiresFirecrawlKeyByDefault(t *testing.T) {
	t.Setenv("HEPHAESTUS_POSTGRES_DSN", "test-dsn")
	t.Setenv("HEPHAESTUS_DEEPSEEK_API_KEY", "test-key")
	t.Setenv("HEPHAESTUS_FIRECRAWL_API_KEY", "")
	if _, err := Load(); err == nil {
		t.Fatal("expected missing Firecrawl key error")
	}
}
