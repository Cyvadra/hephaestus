// Hephaestus: a single-user LLM<->human interaction framework.
//
//	@title			Hephaestus API
//	@version		0.1
//	@description	Single-user AI agent framework: sessions, chat turns, slash commands.
//	@BasePath		/api/v1
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	_ "github.com/Cyvadra/hephaestus/docs/swagger"
	"github.com/Cyvadra/hephaestus/internal/bootstrap"
	"github.com/Cyvadra/hephaestus/internal/chat"
	"github.com/Cyvadra/hephaestus/internal/command"
	"github.com/Cyvadra/hephaestus/internal/interaction"
	"github.com/Cyvadra/hephaestus/internal/llm"
	"github.com/Cyvadra/hephaestus/internal/notify"
	"github.com/Cyvadra/hephaestus/internal/plugin"
	"github.com/Cyvadra/hephaestus/internal/plugin/builtin"
	"github.com/Cyvadra/hephaestus/internal/project"
	"github.com/Cyvadra/hephaestus/internal/registry"
	"github.com/Cyvadra/hephaestus/internal/server"
	"github.com/Cyvadra/hephaestus/internal/session"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/Cyvadra/hephaestus/internal/toolkit"
	"github.com/Cyvadra/hephaestus/internal/tools"
	"github.com/Cyvadra/hephaestus/internal/upload"
	"github.com/Cyvadra/hephaestus/pkg/baidu/ocr"
	"github.com/Cyvadra/hephaestus/pkg/weather"
	"github.com/joho/godotenv"
)

func main() {
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		log.Fatalf("dotenv: %v", err)
	}

	cfg, err := bootstrap.Load()
	if err != nil {
		log.Fatalf("bootstrap: %v", err)
	}

	notifier := notify.New(cfg.WeComWebhookURL)

	toolReg := toolkit.NewRegistry()

	db, err := store.Open(cfg.PostgresDSN)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	projects, err := project.New(db, cfg.ProjectsRoot)
	if err != nil {
		log.Fatalf("project: %v", err)
	}
	defaultProject, err := projects.EnsureDefault()
	if err != nil {
		log.Fatalf("project: ensure default: %v", err)
	}
	llmClient := llm.NewWithLocalModel(cfg.DeepSeekAPIKey, cfg.LocalModelURL, cfg.LocalModelAPIKey)
	sessions := session.New(db)
	if err := session.BindUnscopedSessions(db, defaultProject.ID); err != nil {
		log.Fatalf("session: bind default project: %v", err)
	}
	toolReg.Register(tools.NewChatHistorySearchTool(db, sessions))
	toolReg.Register(tools.NewCreateProjectTool(projects))
	toolReg.Register(tools.NewListProjectsTool(projects))
	fileAccess := tools.FileAccessConfig{AllowOutsideProject: cfg.ProjectAccessOverride}
	interactions := interaction.NewManager()
	webFetch, err := tools.NewWebFetchTool(tools.WebFetchConfig{
		Provider:        cfg.WebFetchProvider,
		FirecrawlAPIKey: cfg.FirecrawlAPIKey,
		ChromePath:      cfg.WebFetchChromePath,
		MaxChars:        cfg.WebFetchMaxChars,
		SummaryMaxChars: cfg.WebFetchSummaryMaxChars,
		LLMClient:       llmClient,
	})
	if err != nil {
		log.Fatalf("web fetch: %v", err)
	}
	toolReg.Register(webFetch)
	webSearch := tools.NewWebSearchTool(tools.WebSearchConfig{BraveAPIKeys: cfg.WebSearchBraveAPIKeys, TavilyAPIKeys: cfg.WebSearchTavilyAPIKeys, SerpAPIKeys: cfg.WebSearchSerpAPIKeys, SerpAPIEngine: cfg.WebSearchSerpAPIEngine, SearXNGBaseURL: cfg.WebSearchSearXNGBaseURL, LLMClient: llmClient, SummaryMaxChars: cfg.WebSearchSummaryMaxChars})
	toolReg.Register(webSearch)
	shellTool := tools.NewShellToolWithAccess(cfg.ShellEnabled, 0, fileAccess)
	shellTool.SetInteractionManager(interactions)
	toolReg.Register(shellTool)

	pluginReg := plugin.NewRegistry(notifier)
	weatherClient, err := weather.NewClient(nil, cfg.WeatherProviders)
	if err != nil {
		log.Fatalf("weather: %v", err)
	}
	pluginReg.Register(builtin.NewEnvironmentContextPlugin(builtin.EnvironmentContextConfig{
		Location:    cfg.EnvironmentLocation,
		Coordinates: weather.Location{Latitude: cfg.EnvironmentLatitude, Longitude: cfg.EnvironmentLongitude},
		Timezone:    cfg.EnvironmentTimezone,
		Weather:     weatherClient,
	}))
	pluginReg.Register(builtin.NewSessionSummaryPlugin(db, llmClient, 5*time.Minute))
	pluginReg.Register(builtin.NewStorylineStatusPlugin(db, llmClient))
	pluginReg.Register(builtin.NewOptionsPlugin(llmClient))
	if err := pluginReg.SetFixedPlugins(cfg.FixedPlugins); err != nil {
		log.Fatalf("plugin: configure fixed plugins: %v", err)
	}

	staticRegistry, err := registry.Load(cfg.ConfigDir)
	if err != nil {
		log.Fatalf("registry: %v", err)
	}
	reg, err := registry.LoadDatabase(db, staticRegistry)
	if err != nil {
		log.Fatalf("registry: %v", err)
	}
	if err := reg.Validate(toolReg.KnownNames(), pluginReg.KnownNames()); err != nil {
		log.Fatalf("registry: validation failed: %v", err)
	}
	registryStore := registry.NewStore(reg)
	configs, err := registry.NewService(db, staticRegistry, registryStore, toolReg.KnownNames(), pluginReg.KnownNames())
	if err != nil {
		log.Fatalf("registry: configuration service: %v", err)
	}
	if len(reg.Workflows) > 0 || len(reg.Jobs) > 0 {
		log.Printf("registry: loaded %d workflow(s) and %d job(s); no scheduler is implemented yet, so they will not run", len(reg.Workflows), len(reg.Jobs))
	}

	pipeline := chat.NewPipeline(db, registryStore, toolReg, pluginReg, llmClient, sessions, notifier, projects, interactions)
	commands := command.NewService(registryStore, toolReg, pluginReg, sessions, notifier, db, projects, interactions)
	uploads, err := upload.New(upload.Config{
		TextExtensions:     cfg.UploadTextExtensions,
		ImageExtensions:    cfg.UploadImageExtensions,
		InlineTextMaxBytes: cfg.UploadInlineTextMaxBytes,
		OCRImageMaxBytes:   cfg.UploadOCRImageMaxBytes,
		FileMaxBytes:       cfg.UploadFileMaxBytes,
		TotalMaxBytes:      cfg.UploadTotalMaxBytes,
		MaxFiles:           cfg.UploadMaxFiles,
		Recognizer:         newOCRRecognizer(cfg.BaiduOCRAPIKey, cfg.BaiduOCRSecretKey),
	})
	if err != nil {
		log.Fatalf("upload: %v", err)
	}

	srv := server.New(db, registryStore, sessions, pipeline, commands, projects, uploads, configs)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := srv.Run(ctx, cfg.ListenAddr); err != nil {
		log.Printf("server: %v", err)
		return
	}
}

type ocrRecognizer struct{}

func newOCRRecognizer(apiKey, secretKey string) upload.Recognizer {
	if apiKey == "" {
		return nil
	}
	ocr.Init(apiKey, secretKey)
	return ocrRecognizer{}
}

func (ocrRecognizer) Recognize(ctx context.Context, image []byte) (string, error) {
	result, err := ocr.RecognizeImage(ctx, image, ocr.Options{})
	if err != nil {
		return "", err
	}
	lines := make([]string, 0, len(result.WordsResult))
	for _, word := range result.WordsResult {
		if word.Words != "" {
			lines = append(lines, word.Words)
		}
	}
	return strings.Join(lines, "\n"), nil
}
