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
	ExecEnabled             bool
	WebSearchProvider       string
	WebSearchBraveAPIKeys   []string
	WebSearchTavilyAPIKeys  []string
	WebSearchSerpAPIKeys    []string
	WebSearchSerpAPIEngine  string
	WebSearchSearXNGBaseURL string
	WebSearchSogouEnabled   bool
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
		ConfigDir:               getenvDefault("HEPHAESTUS_CONFIG_DIR", "./config"),
		PostgresDSN:             os.Getenv("HEPHAESTUS_POSTGRES_DSN"),
		DeepSeekAPIKey:          os.Getenv("HEPHAESTUS_DEEPSEEK_API_KEY"),
		WeComWebhookURL:         os.Getenv("HEPHAESTUS_WECOM_WEBHOOK_URL"),
		ListenAddr:              getenvDefault("HEPHAESTUS_LISTEN_ADDR", ":9016"),
		ProjectsRoot:            projectsRoot,
		ProjectAccessOverride:   getenvBool("HEPHAESTUS_PROJECT_ACCESS_OVERRIDE"),
		ExecEnabled:             getenvBool("HEPHAESTUS_EXEC_ENABLED"),
		WebSearchProvider:       getenvDefault("HEPHAESTUS_WEB_SEARCH_PROVIDER", "auto"),
		WebSearchBraveAPIKeys:   splitCommaSeparated(os.Getenv("HEPHAESTUS_WEB_SEARCH_BRAVE_API_KEYS")),
		WebSearchTavilyAPIKeys:  splitCommaSeparated(os.Getenv("HEPHAESTUS_WEB_SEARCH_TAVILY_API_KEYS")),
		WebSearchSerpAPIKeys:    splitCommaSeparated(os.Getenv("HEPHAESTUS_WEB_SEARCH_SERPAPI_API_KEYS")),
		WebSearchSerpAPIEngine:  getenvDefault("HEPHAESTUS_WEB_SEARCH_SERPAPI_ENGINE", "google_light"),
		WebSearchSearXNGBaseURL: os.Getenv("HEPHAESTUS_WEB_SEARCH_SEARXNG_BASE_URL"),
		WebSearchSogouEnabled:   getenvBool("HEPHAESTUS_WEB_SEARCH_SOGOU_ENABLED"),
		FixedPlugins:            splitCommaSeparated(getenvDefault("HEPHAESTUS_FIXED_PLUGINS", "session_summary")),
	}

	if cfg.PostgresDSN == "" {
		return nil, fmt.Errorf("bootstrap: HEPHAESTUS_POSTGRES_DSN is required")
	}
	if cfg.DeepSeekAPIKey == "" {
		return nil, fmt.Errorf("bootstrap: HEPHAESTUS_DEEPSEEK_API_KEY is required")
	}

	return cfg, nil
}

func getenvBool(key string) bool {
	value, err := strconv.ParseBool(os.Getenv(key))
	return err == nil && value
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
