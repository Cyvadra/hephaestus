// Package bootstrap loads process-level configuration from the environment.
package bootstrap

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/Cyvadra/hephaestus/internal/fsutil"
)

// Config holds environment-derived settings needed to start the process.
// Static domain configuration (identity, concierge, etc.) lives under
// ConfigDir and is loaded separately by internal/registry.
type Config struct {
	// ConfigDir holds the flat directory of identity/impression/toolgroup/
	// concierge/workflow/job static config files.
	ConfigDir string
	// PostgresDSN connects to the single Postgres database used for
	// runtime data (session, chat history, compression).
	PostgresDSN string
	// DeepSeekAPIKey authenticates outbound calls via github.com/Cyvadra/ds4.
	DeepSeekAPIKey string
	// WeComWebhookURL receives Warn/Error notifications; empty disables delivery.
	WeComWebhookURL string
	// ListenAddr is the address the Gin HTTP server binds to.
	ListenAddr string
	// ProjectsRoot is the directory under which each Project gets its own
	// named subdirectory; created on startup if missing.
	ProjectsRoot string
	// ProjectAccessOverride allows filesystem tools to access paths outside
	// the bound Project and the system temporary directory.
	ProjectAccessOverride bool
	// ExecEnabled defaults to false.
	ExecEnabled              bool
	WebFetchProvider         string
	FirecrawlAPIKey          string
	WebFetchChromePath       string
	WebFetchMaxChars         int
	WebFetchSummaryMaxChars  int
	WebSearchBraveAPIKeys    []string
	WebSearchTavilyAPIKeys   []string
	WebSearchSerpAPIKeys     []string
	WebSearchSerpAPIEngine   string
	WebSearchSearXNGBaseURL  string
	WebSearchSummaryMaxChars int
	// FixedPlugins run for every session and cannot be disabled through
	// mutable session settings.
	FixedPlugins []string
}

// Load reads configuration from environment variables, applying defaults
// where safe to do so, and validates required fields.
func Load() (*Config, error) {
	projectsRoot, err := fsutil.ExpandHome(getenvDefault("HEPHAESTUS_PROJECTS_ROOT", "./data/projects"))
	if err != nil {
		return nil, fmt.Errorf("bootstrap: projects root: %w", err)
	}
	cfg := &Config{
		ConfigDir:                getenvDefault("HEPHAESTUS_CONFIG_DIR", "./config"),
		PostgresDSN:              os.Getenv("HEPHAESTUS_POSTGRES_DSN"),
		DeepSeekAPIKey:           os.Getenv("HEPHAESTUS_DEEPSEEK_API_KEY"),
		WeComWebhookURL:          os.Getenv("HEPHAESTUS_WECOM_WEBHOOK_URL"),
		ListenAddr:               getenvDefault("HEPHAESTUS_LISTEN_ADDR", "127.0.0.1:9016"),
		ProjectsRoot:             projectsRoot,
		ProjectAccessOverride:    getenvBool("HEPHAESTUS_PROJECT_ACCESS_OVERRIDE"),
		ExecEnabled:              getenvBool("HEPHAESTUS_EXEC_ENABLED"),
		WebFetchProvider:         strings.ToLower(strings.TrimSpace(getenvDefault("HEPHAESTUS_WEB_FETCH_PROVIDER", "firecrawl"))),
		FirecrawlAPIKey:          strings.TrimSpace(os.Getenv("HEPHAESTUS_FIRECRAWL_API_KEY")),
		WebFetchChromePath:       strings.TrimSpace(os.Getenv("HEPHAESTUS_WEB_FETCH_CHROME_PATH")),
		WebFetchMaxChars:         getenvInt("HEPHAESTUS_WEB_FETCH_MAX_CHARS", 16_000),
		WebFetchSummaryMaxChars:  getenvInt("HEPHAESTUS_WEB_FETCH_SUMMARY_MAX_CHARS", 4_000),
		WebSearchBraveAPIKeys:    splitCommaSeparated(os.Getenv("HEPHAESTUS_WEB_SEARCH_BRAVE_API_KEYS")),
		WebSearchTavilyAPIKeys:   splitCommaSeparated(os.Getenv("HEPHAESTUS_WEB_SEARCH_TAVILY_API_KEYS")),
		WebSearchSerpAPIKeys:     splitCommaSeparated(os.Getenv("HEPHAESTUS_WEB_SEARCH_SERPAPI_API_KEYS")),
		WebSearchSerpAPIEngine:   getenvDefault("HEPHAESTUS_WEB_SEARCH_SERPAPI_ENGINE", "google_light"),
		WebSearchSearXNGBaseURL:  os.Getenv("HEPHAESTUS_WEB_SEARCH_SEARXNG_BASE_URL"),
		WebSearchSummaryMaxChars: getenvInt("HEPHAESTUS_WEB_SEARCH_SUMMARY_MAX_CHARS", 4_000),
		FixedPlugins:             splitCommaSeparated(getenvDefault("HEPHAESTUS_FIXED_PLUGINS", "session_summary")),
	}

	if cfg.PostgresDSN == "" {
		return nil, fmt.Errorf("bootstrap: HEPHAESTUS_POSTGRES_DSN is required")
	}
	if cfg.DeepSeekAPIKey == "" {
		return nil, fmt.Errorf("bootstrap: HEPHAESTUS_DEEPSEEK_API_KEY is required")
	}
	if cfg.WebFetchProvider != "firecrawl" && cfg.WebFetchProvider != "local" {
		return nil, fmt.Errorf("bootstrap: HEPHAESTUS_WEB_FETCH_PROVIDER must be firecrawl or local")
	}
	if cfg.WebFetchProvider == "firecrawl" && cfg.FirecrawlAPIKey == "" {
		return nil, fmt.Errorf("bootstrap: HEPHAESTUS_FIRECRAWL_API_KEY is required when web fetch provider is firecrawl")
	}

	return cfg, nil
}

func getenvBool(key string) bool {
	value, err := strconv.ParseBool(os.Getenv(key))
	return err == nil && value
}

// getenvInt returns the integer value of key, or fallback when unset or
// unparsable.
func getenvInt(key string, fallback int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil {
			return parsed
		}
	}
	return fallback
}

func getenvDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func splitCommaSeparated(value string) []string {
	var values []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			values = append(values, item)
		}
	}
	return values
}
