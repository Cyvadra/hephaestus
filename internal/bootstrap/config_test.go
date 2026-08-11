package bootstrap

import (
	"os"
	"reflect"
	"testing"
)

func TestMain(m *testing.M) {
	os.Setenv("HEPHAESTUS_ENV_LOCATION", "深圳")
	os.Setenv("HEPHAESTUS_ENV_LATITUDE", "22.5431")
	os.Setenv("HEPHAESTUS_ENV_LONGITUDE", "114.0579")
	os.Setenv("HEPHAESTUS_ENV_TIMEZONE", "Asia/Shanghai")
	os.Exit(m.Run())
}

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
	t.Setenv("HEPHAESTUS_UPLOAD_OCR_IMAGE_MAX_BYTES", "50")
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
	if cfg.UploadFileMaxBytes != 100 || cfg.UploadTotalMaxBytes != 200 || cfg.UploadOCRImageMaxBytes != 50 || cfg.UploadInlineTextMaxBytes != 10 || cfg.UploadMaxFiles != 2 {
		t.Fatalf("unexpected upload config: %+v", cfg)
	}
}

func TestLoadRejectsPartialOCRCredentialsAndInvalidUploadLimits(t *testing.T) {
	t.Setenv("HEPHAESTUS_POSTGRES_DSN", "test-dsn")
	t.Setenv("HEPHAESTUS_DEEPSEEK_API_KEY", "test-key")
	t.Setenv("HEPHAESTUS_FIRECRAWL_API_KEY", "firecrawl-key")
	t.Setenv("HEPHAESTUS_BAIDU_OCR_API_KEY", "ocr-key")
	if _, err := Load(); err == nil {
		t.Fatal("expected partial OCR credentials to fail")
	}

	t.Setenv("HEPHAESTUS_BAIDU_OCR_SECRET_KEY", "ocr-secret")
	t.Setenv("HEPHAESTUS_UPLOAD_MAX_FILES", "0")
	if _, err := Load(); err == nil {
		t.Fatal("expected invalid upload limit to fail")
	}
}

func TestLoadRejectsInvalidEnvironmentContext(t *testing.T) {
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
