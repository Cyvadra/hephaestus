package bootstrap

import (
	"os"
	"reflect"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	os.Setenv("HEPHAESTUS_AUTH_USERNAME", "test-user")
	os.Setenv("HEPHAESTUS_AUTH_PASSWORD", "test-password")
	os.Setenv("HEPHAESTUS_JWT_SECRET", "test-jwt-secret-with-at-least-32-bytes")
	os.Setenv("HEPHAESTUS_ENV_LOCATION", "深圳")
	os.Setenv("HEPHAESTUS_ENV_LATITUDE", "22.5431")
	os.Setenv("HEPHAESTUS_ENV_LONGITUDE", "114.0579")
	os.Setenv("HEPHAESTUS_ENV_TIMEZONE", "Asia/Shanghai")
	os.Exit(m.Run())
}

func TestLoadAuthConfig(t *testing.T) {
	t.Setenv("HEPHAESTUS_POSTGRES_DSN", "test-dsn")
	t.Setenv("HEPHAESTUS_DEEPSEEK_API_KEY", "test-key")
	t.Setenv("HEPHAESTUS_FIRECRAWL_API_KEY", "firecrawl-key")
	t.Setenv("HEPHAESTUS_AUTH_USERNAME", " user ")
	t.Setenv("HEPHAESTUS_AUTH_PASSWORD", " password ")
	t.Setenv("HEPHAESTUS_JWT_SECRET", "12345678901234567890123456789012")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuthUsername != "user" || cfg.AuthPassword != " password " || cfg.JWTSecret != "12345678901234567890123456789012" {
		t.Fatalf("unexpected auth config: %+v", cfg)
	}
}

func TestLoadRejectsMissingOrShortAuthConfig(t *testing.T) {
	t.Setenv("HEPHAESTUS_POSTGRES_DSN", "test-dsn")
	t.Setenv("HEPHAESTUS_DEEPSEEK_API_KEY", "test-key")
	t.Setenv("HEPHAESTUS_FIRECRAWL_API_KEY", "firecrawl-key")
	t.Setenv("HEPHAESTUS_AUTH_USERNAME", "")
	if _, err := Load(); err == nil {
		t.Fatal("expected missing auth username to fail")
	}

	t.Setenv("HEPHAESTUS_AUTH_USERNAME", "user")
	t.Setenv("HEPHAESTUS_JWT_SECRET", "too-short")
	if _, err := Load(); err == nil {
		t.Fatal("expected short JWT secret to fail")
	}
}

func TestSplitCommaSeparated(t *testing.T) {
	got := splitCommaSeparated(" session_summary,options, ,storyline_status ")
	want := []string{"session_summary", "options", "storyline_status"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("splitCommaSeparated() = %v, want %v", got, want)
	}
}

func TestEnvValuesBoolAcceptsStandardTrueValues(t *testing.T) {
	t.Setenv("HEPHAESTUS_TEST_BOOL", "1")
	env := &envValues{}
	if !env.bool("HEPHAESTUS_TEST_BOOL") {
		t.Fatal("expected ParseBool-compatible true value")
	}
	if len(env.problems) != 0 {
		t.Fatalf("expected no problems, got %v", env.problems)
	}
}

func TestEnvValuesRecordsUnparsableValues(t *testing.T) {
	t.Setenv("HEPHAESTUS_TEST_BOOL", "not-a-bool")
	t.Setenv("HEPHAESTUS_TEST_INT", "50MB")
	t.Setenv("HEPHAESTUS_TEST_INT64", "12.5")
	env := &envValues{}
	env.bool("HEPHAESTUS_TEST_BOOL")
	env.int("HEPHAESTUS_TEST_INT", 7)
	env.int64("HEPHAESTUS_TEST_INT64", 9)
	if len(env.problems) != 3 {
		t.Fatalf("expected 3 problems for unparsable values, got %v", env.problems)
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

func TestLoadLocalModelConfig(t *testing.T) {
	t.Setenv("HEPHAESTUS_POSTGRES_DSN", "test-dsn")
	t.Setenv("HEPHAESTUS_FIRECRAWL_API_KEY", "firecrawl-key")
	t.Setenv("HEPHAESTUS_LOCAL_MODEL_URL", " http://localhost:8080/v1/ ")
	t.Setenv("HEPHAESTUS_LOCAL_MODEL_API_KEY", " local-key ")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LocalModelURL != "http://localhost:8080/v1" {
		t.Fatalf("LocalModelURL = %q", cfg.LocalModelURL)
	}
	if cfg.LocalModelAPIKey != "local-key" {
		t.Fatalf("LocalModelAPIKey = %q", cfg.LocalModelAPIKey)
	}
}

func TestLoadRequiresLLMProvider(t *testing.T) {
	t.Setenv("HEPHAESTUS_POSTGRES_DSN", "test-dsn")
	t.Setenv("HEPHAESTUS_FIRECRAWL_API_KEY", "firecrawl-key")
	if _, err := Load(); err == nil {
		t.Fatal("expected missing LLM provider error")
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

func TestLoadShellBackendConfig(t *testing.T) {
	t.Setenv("HEPHAESTUS_POSTGRES_DSN", "test-dsn")
	t.Setenv("HEPHAESTUS_DEEPSEEK_API_KEY", "test-key")
	t.Setenv("HEPHAESTUS_FIRECRAWL_API_KEY", "firecrawl-key")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ShellBackend != "local" {
		t.Fatalf("ShellBackend = %q, want local", cfg.ShellBackend)
	}

	t.Setenv("HEPHAESTUS_SHELL_ENABLED", "true")
	t.Setenv("HEPHAESTUS_SHELL_BACKEND", "ssh")
	t.Setenv("HEPHAESTUS_SHELL_SSH_DESTINATION", "build-host")
	t.Setenv("HEPHAESTUS_SHELL_SSH_PROJECTS_ROOT", "/srv/hephaestus/projects")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ShellBackend != "ssh" || cfg.ShellSSHDestination != "build-host" || cfg.ShellSSHProjectsRoot != "/srv/hephaestus/projects" {
		t.Fatalf("unexpected SSH shell configuration: %+v", cfg)
	}
}

func TestLoadRejectsInvalidShellBackendConfig(t *testing.T) {
	t.Setenv("HEPHAESTUS_POSTGRES_DSN", "test-dsn")
	t.Setenv("HEPHAESTUS_DEEPSEEK_API_KEY", "test-key")
	t.Setenv("HEPHAESTUS_FIRECRAWL_API_KEY", "firecrawl-key")
	t.Setenv("HEPHAESTUS_SHELL_BACKEND", "container")
	if _, err := Load(); err == nil {
		t.Fatal("expected invalid shell backend error")
	}

	t.Setenv("HEPHAESTUS_SHELL_BACKEND", "ssh")
	t.Setenv("HEPHAESTUS_SHELL_ENABLED", "true")
	if _, err := Load(); err == nil {
		t.Fatal("expected missing SSH configuration error")
	}
	t.Setenv("HEPHAESTUS_SHELL_SSH_DESTINATION", "-unsafe")
	t.Setenv("HEPHAESTUS_SHELL_SSH_PROJECTS_ROOT", "relative")
	if _, err := Load(); err == nil {
		t.Fatal("expected invalid SSH destination and root error")
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

func TestLoadUploadConfig(t *testing.T) {
	t.Setenv("HEPHAESTUS_POSTGRES_DSN", "test-dsn")
	t.Setenv("HEPHAESTUS_DEEPSEEK_API_KEY", "test-key")
	t.Setenv("HEPHAESTUS_FIRECRAWL_API_KEY", "firecrawl-key")
	t.Setenv("HEPHAESTUS_BAIDU_OCR_API_KEY", "ocr-key")
	t.Setenv("HEPHAESTUS_BAIDU_OCR_SECRET_KEY", "ocr-secret")
	t.Setenv("HEPHAESTUS_UPLOAD_TEXT_EXTENSIONS", ".TXT, md, txt")
	t.Setenv("HEPHAESTUS_UPLOAD_FILE_MAX_BYTES", "100")
	t.Setenv("HEPHAESTUS_UPLOAD_TOTAL_MAX_BYTES", "200")
	t.Setenv("HEPHAESTUS_UPLOAD_INLINE_TEXT_MAX_BYTES", "10")
	t.Setenv("HEPHAESTUS_UPLOAD_MAX_FILES", "2")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaiduOCRAPIKey != "ocr-key" || cfg.BaiduOCRSecretKey != "ocr-secret" {
		t.Fatalf("unexpected OCR credentials: %+v", cfg)
	}
	if want := []string{"txt", "md"}; !reflect.DeepEqual(cfg.UploadTextExtensions, want) {
		t.Fatalf("text extensions = %v, want %v", cfg.UploadTextExtensions, want)
	}
	if cfg.UploadFileMaxBytes != 100 || cfg.UploadTotalMaxBytes != 200 || cfg.UploadInlineTextMaxBytes != 10 || cfg.UploadMaxFiles != 2 {
		t.Fatalf("unexpected upload config: %+v", cfg)
	}
}

func TestLoadAcceptsDeprecatedOCRCredentialsAndRejectsInvalidUploadLimits(t *testing.T) {
	t.Setenv("HEPHAESTUS_POSTGRES_DSN", "test-dsn")
	t.Setenv("HEPHAESTUS_DEEPSEEK_API_KEY", "test-key")
	t.Setenv("HEPHAESTUS_FIRECRAWL_API_KEY", "firecrawl-key")
	t.Setenv("HEPHAESTUS_BAIDU_OCR_API_KEY", "ocr-key")
	if _, err := Load(); err != nil {
		t.Fatalf("deprecated OCR credentials must not block startup: %v", err)
	}

	t.Setenv("HEPHAESTUS_UPLOAD_MAX_FILES", "0")
	if _, err := Load(); err == nil {
		t.Fatal("expected invalid upload limit to fail")
	}
}

func TestLoadQQNotificationConfig(t *testing.T) {
	t.Setenv("HEPHAESTUS_POSTGRES_DSN", "test-dsn")
	t.Setenv("HEPHAESTUS_DEEPSEEK_API_KEY", "test-key")
	t.Setenv("HEPHAESTUS_FIRECRAWL_API_KEY", "firecrawl-key")
	t.Setenv("HEPHAESTUS_QQ_APP_ID", " app ")
	t.Setenv("HEPHAESTUS_QQ_APP_SECRET", " secret ")
	t.Setenv("HEPHAESTUS_QQ_USER_OPENID", " user-openid ")
	t.Setenv("HEPHAESTUS_CHANNEL_IMAGE_TEXT_WAIT_SECONDS", "45")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.QQAppID != "app" || cfg.QQAppSecret != "secret" || cfg.QQUserOpenID != "user-openid" || cfg.ChannelImageTextWait != 45*time.Second {
		t.Fatalf("unexpected QQ configuration: %+v", cfg)
	}
}

func TestLoadQQDiscoveryConfigWithoutUserOpenID(t *testing.T) {
	t.Setenv("HEPHAESTUS_POSTGRES_DSN", "test-dsn")
	t.Setenv("HEPHAESTUS_DEEPSEEK_API_KEY", "test-key")
	t.Setenv("HEPHAESTUS_FIRECRAWL_API_KEY", "firecrawl-key")
	t.Setenv("HEPHAESTUS_QQ_APP_ID", "app")
	t.Setenv("HEPHAESTUS_QQ_APP_SECRET", "secret")
	t.Setenv("HEPHAESTUS_QQ_USER_OPENID", "")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.QQUserOpenID != "" {
		t.Fatalf("QQUserOpenID = %q, want empty discovery configuration", cfg.QQUserOpenID)
	}
}

func TestLoadRejectsPartialQQNotificationConfig(t *testing.T) {
	t.Setenv("HEPHAESTUS_POSTGRES_DSN", "test-dsn")
	t.Setenv("HEPHAESTUS_DEEPSEEK_API_KEY", "test-key")
	t.Setenv("HEPHAESTUS_FIRECRAWL_API_KEY", "firecrawl-key")
	t.Setenv("HEPHAESTUS_QQ_APP_ID", "app")
	if _, err := Load(); err == nil {
		t.Fatal("expected partial QQ configuration to fail")
	}
}

func TestLoadRejectsInvalidMetaphysics(t *testing.T) {
	t.Setenv("HEPHAESTUS_POSTGRES_DSN", "test-dsn")
	t.Setenv("HEPHAESTUS_DEEPSEEK_API_KEY", "test-key")
	t.Setenv("HEPHAESTUS_FIRECRAWL_API_KEY", "firecrawl-key")
	t.Setenv("HEPHAESTUS_ENV_LOCATION", "")
	if _, err := Load(); err == nil {
		t.Fatal("expected missing location error")
	}
	t.Setenv("HEPHAESTUS_ENV_LOCATION", "深圳")
	t.Setenv("HEPHAESTUS_ENV_LATITUDE", "91")
	if _, err := Load(); err == nil {
		t.Fatal("expected invalid latitude error")
	}
	t.Setenv("HEPHAESTUS_ENV_LATITUDE", "22.5431")
	t.Setenv("HEPHAESTUS_ENV_TIMEZONE", "not/a-timezone")
	if _, err := Load(); err == nil {
		t.Fatal("expected invalid timezone error")
	}
	t.Setenv("HEPHAESTUS_ENV_TIMEZONE", "Asia/Shanghai")
	t.Setenv("HEPHAESTUS_WEATHER_PROVIDERS", "unknown")
	if _, err := Load(); err == nil {
		t.Fatal("expected invalid provider error")
	}
}
