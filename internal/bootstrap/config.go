// Package bootstrap loads process-level configuration from the environment.
package bootstrap

import (
	"fmt"
	"os"
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
}

// Load reads configuration from environment variables, applying defaults
// where safe to do so, and validates required fields.
func Load() (*Config, error) {
	cfg := &Config{
		ConfigDir:       getenvDefault("HEPHAESTUS_CONFIG_DIR", "./config"),
		PostgresDSN:     os.Getenv("HEPHAESTUS_POSTGRES_DSN"),
		DeepSeekAPIKey:  os.Getenv("HEPHAESTUS_DEEPSEEK_API_KEY"),
		WeComWebhookURL: os.Getenv("HEPHAESTUS_WECOM_WEBHOOK_URL"),
		ListenAddr:      getenvDefault("HEPHAESTUS_LISTEN_ADDR", ":8080"),
	}

	if cfg.PostgresDSN == "" {
		return nil, fmt.Errorf("bootstrap: HEPHAESTUS_POSTGRES_DSN is required")
	}
	if cfg.DeepSeekAPIKey == "" {
		return nil, fmt.Errorf("bootstrap: HEPHAESTUS_DEEPSEEK_API_KEY is required")
	}

	return cfg, nil
}

func getenvDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
