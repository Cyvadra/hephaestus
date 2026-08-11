// Package bootstrap loads process-level configuration from the environment.
package bootstrap

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

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
	// LocalModelURL and LocalModelAPIKey configure an optional OpenAI-compatible
	// endpoint. When set, ds4 routes identities by their preferred model name.
	LocalModelURL    string
	LocalModelAPIKey string
	// BaiduOCRAPIKey and BaiduOCRSecretKey authenticate optional image OCR.
	BaiduOCRAPIKey    string
	BaiduOCRSecretKey string
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
	// ShellEnabled defaults to false.
	ShellEnabled             bool
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
	UploadTextExtensions     []string
	UploadImageExtensions    []string
	UploadInlineTextMaxBytes int64
	UploadOCRImageMaxBytes   int64
	UploadFileMaxBytes       int64
	UploadTotalMaxBytes      int64
	UploadMaxFiles           int
	EnvironmentLocation      string
	EnvironmentLatitude      float64
	EnvironmentLongitude     float64
	EnvironmentTimezone      string
	WeatherProviders         []string
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
		LocalModelURL:            strings.TrimRight(strings.TrimSpace(os.Getenv("HEPHAESTUS_LOCAL_MODEL_URL")), "/"),
		LocalModelAPIKey:         strings.TrimSpace(os.Getenv("HEPHAESTUS_LOCAL_MODEL_API_KEY")),
		BaiduOCRAPIKey:           strings.TrimSpace(os.Getenv("HEPHAESTUS_BAIDU_OCR_API_KEY")),
		BaiduOCRSecretKey:        strings.TrimSpace(os.Getenv("HEPHAESTUS_BAIDU_OCR_SECRET_KEY")),
		WeComWebhookURL:          os.Getenv("HEPHAESTUS_WECOM_WEBHOOK_URL"),
		ListenAddr:               getenvDefault("HEPHAESTUS_LISTEN_ADDR", "127.0.0.1:9016"),
		ProjectsRoot:             projectsRoot,
		ProjectAccessOverride:    getenvBool("HEPHAESTUS_PROJECT_ACCESS_OVERRIDE"),
		ShellEnabled:             getenvBool("HEPHAESTUS_SHELL_ENABLED"),
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
		UploadTextExtensions:     splitExtensions(getenvDefault("HEPHAESTUS_UPLOAD_TEXT_EXTENSIONS", "md,markdown,txt,csv,json,yaml,yml,toml,xml")),
		UploadImageExtensions:    splitExtensions(getenvDefault("HEPHAESTUS_UPLOAD_IMAGE_EXTENSIONS", "jpg,jpeg,png,bmp")),
		UploadInlineTextMaxBytes: getenvInt64("HEPHAESTUS_UPLOAD_INLINE_TEXT_MAX_BYTES", 10<<10),
		UploadOCRImageMaxBytes:   getenvInt64("HEPHAESTUS_UPLOAD_OCR_IMAGE_MAX_BYTES", 4<<20),
		UploadFileMaxBytes:       getenvInt64("HEPHAESTUS_UPLOAD_FILE_MAX_BYTES", 50<<20),
		UploadTotalMaxBytes:      getenvInt64("HEPHAESTUS_UPLOAD_TOTAL_MAX_BYTES", 250<<20),
		UploadMaxFiles:           getenvInt("HEPHAESTUS_UPLOAD_MAX_FILES", 5),
		EnvironmentLocation:      strings.TrimSpace(os.Getenv("HEPHAESTUS_ENV_LOCATION")),
		EnvironmentTimezone:      strings.TrimSpace(os.Getenv("HEPHAESTUS_ENV_TIMEZONE")),
		WeatherProviders:         splitCommaSeparated(getenvDefault("HEPHAESTUS_WEATHER_PROVIDERS", "open_meteo,wttr,met_no")),
		FixedPlugins:             splitCommaSeparated(getenvDefault("HEPHAESTUS_FIXED_PLUGINS", "environment_context,session_summary")),
	}
	var errLatitude, errLongitude error
	cfg.EnvironmentLatitude, errLatitude = requiredFloat("HEPHAESTUS_ENV_LATITUDE")
	cfg.EnvironmentLongitude, errLongitude = requiredFloat("HEPHAESTUS_ENV_LONGITUDE")

	if cfg.PostgresDSN == "" {
		return nil, fmt.Errorf("bootstrap: HEPHAESTUS_POSTGRES_DSN is required")
	}
	if cfg.DeepSeekAPIKey == "" && cfg.LocalModelURL == "" {
		return nil, fmt.Errorf("bootstrap: HEPHAESTUS_DEEPSEEK_API_KEY or HEPHAESTUS_LOCAL_MODEL_URL is required")
	}
	if cfg.EnvironmentLocation == "" {
		return nil, fmt.Errorf("bootstrap: HEPHAESTUS_ENV_LOCATION is required")
	}
	if errLatitude != nil {
		return nil, errLatitude
	}
	if errLongitude != nil {
		return nil, errLongitude
	}
	if cfg.EnvironmentLatitude < -90 || cfg.EnvironmentLatitude > 90 {
		return nil, fmt.Errorf("bootstrap: HEPHAESTUS_ENV_LATITUDE must be between -90 and 90")
	}
	if cfg.EnvironmentLongitude < -180 || cfg.EnvironmentLongitude > 180 {
		return nil, fmt.Errorf("bootstrap: HEPHAESTUS_ENV_LONGITUDE must be between -180 and 180")
	}
	if cfg.EnvironmentTimezone == "" {
		return nil, fmt.Errorf("bootstrap: HEPHAESTUS_ENV_TIMEZONE is required")
	}
	if _, err := time.LoadLocation(cfg.EnvironmentTimezone); err != nil {
		return nil, fmt.Errorf("bootstrap: HEPHAESTUS_ENV_TIMEZONE: %w", err)
	}
	if len(cfg.WeatherProviders) == 0 {
		return nil, fmt.Errorf("bootstrap: HEPHAESTUS_WEATHER_PROVIDERS cannot be empty")
	}
	for _, provider := range cfg.WeatherProviders {
		if provider != "open_meteo" && provider != "wttr" && provider != "met_no" {
			return nil, fmt.Errorf("bootstrap: unsupported weather provider %q", provider)
		}
	}
	if (cfg.BaiduOCRAPIKey == "") != (cfg.BaiduOCRSecretKey == "") {
		return nil, fmt.Errorf("bootstrap: HEPHAESTUS_BAIDU_OCR_API_KEY and HEPHAESTUS_BAIDU_OCR_SECRET_KEY must be set together")
	}
	if cfg.WebFetchProvider != "firecrawl" && cfg.WebFetchProvider != "local" {
		return nil, fmt.Errorf("bootstrap: HEPHAESTUS_WEB_FETCH_PROVIDER must be firecrawl or local")
	}
	if cfg.WebFetchProvider == "firecrawl" && cfg.FirecrawlAPIKey == "" {
		return nil, fmt.Errorf("bootstrap: HEPHAESTUS_FIRECRAWL_API_KEY is required when web fetch provider is firecrawl")
	}
	if cfg.UploadInlineTextMaxBytes <= 0 || cfg.UploadOCRImageMaxBytes <= 0 || cfg.UploadFileMaxBytes <= 0 || cfg.UploadTotalMaxBytes <= 0 || cfg.UploadMaxFiles <= 0 {
		return nil, fmt.Errorf("bootstrap: upload limits must be positive")
	}
	if cfg.UploadOCRImageMaxBytes > cfg.UploadFileMaxBytes {
		return nil, fmt.Errorf("bootstrap: HEPHAESTUS_UPLOAD_OCR_IMAGE_MAX_BYTES cannot exceed HEPHAESTUS_UPLOAD_FILE_MAX_BYTES")
	}
	if cfg.UploadFileMaxBytes > cfg.UploadTotalMaxBytes {
		return nil, fmt.Errorf("bootstrap: HEPHAESTUS_UPLOAD_FILE_MAX_BYTES cannot exceed HEPHAESTUS_UPLOAD_TOTAL_MAX_BYTES")
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

func getenvInt64(key string, fallback int64) int64 {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if parsed, err := strconv.ParseInt(v, 10, 64); err == nil {
			return parsed
		}
	}
	return fallback
}

func requiredFloat(key string) (float64, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return 0, fmt.Errorf("bootstrap: %s is required", key)
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("bootstrap: %s must be a number", key)
	}
	return parsed, nil
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

func splitExtensions(value string) []string {
	seen := make(map[string]struct{})
	var extensions []string
	for _, item := range strings.Split(value, ",") {
		item = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(item, ".")))
		if item == "" {
			continue
		}
		if _, exists := seen[item]; exists {
			continue
		}
		seen[item] = struct{}{}
		extensions = append(extensions, item)
	}
	return extensions
}
