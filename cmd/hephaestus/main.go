// Hephaestus: a single-user LLM<->human interaction framework.
//
//	@title			Hephaestus API
//	@version		0.1
//	@description	Single-user AI agent framework: sessions, chat turns, slash commands.
//	@BasePath		/api/v1
//	@securityDefinitions.apikey	BearerAuth
//	@in			header
//	@name			Authorization
//	@description	JWT bearer token. Browser clients may also authenticate with the HttpOnly session cookie.
package main

import (
	"context"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "github.com/Cyvadra/hephaestus/docs/swagger"
	"github.com/Cyvadra/hephaestus/internal/agent"
	"github.com/Cyvadra/hephaestus/internal/auth"
	"github.com/Cyvadra/hephaestus/internal/bootstrap"
	channelruntime "github.com/Cyvadra/hephaestus/internal/channel"
	"github.com/Cyvadra/hephaestus/internal/chat"
	"github.com/Cyvadra/hephaestus/internal/chatrun"
	"github.com/Cyvadra/hephaestus/internal/command"
	"github.com/Cyvadra/hephaestus/internal/interaction"
	"github.com/Cyvadra/hephaestus/internal/job"
	"github.com/Cyvadra/hephaestus/internal/llm"
	"github.com/Cyvadra/hephaestus/internal/notify"
	"github.com/Cyvadra/hephaestus/internal/plugin"
	"github.com/Cyvadra/hephaestus/internal/plugin/builtin"
	"github.com/Cyvadra/hephaestus/internal/project"
	"github.com/Cyvadra/hephaestus/internal/registry"
	"github.com/Cyvadra/hephaestus/internal/resume"
	"github.com/Cyvadra/hephaestus/internal/server"
	"github.com/Cyvadra/hephaestus/internal/session"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/Cyvadra/hephaestus/internal/subagent"
	"github.com/Cyvadra/hephaestus/internal/subagentexec"
	"github.com/Cyvadra/hephaestus/internal/toolkit"
	"github.com/Cyvadra/hephaestus/internal/tools"
	"github.com/Cyvadra/hephaestus/internal/upload"
	"github.com/Cyvadra/hephaestus/internal/workflow"
	"github.com/Cyvadra/hephaestus/pkg/baidu/ocr"
	"github.com/Cyvadra/hephaestus/pkg/channels"
	channelqq "github.com/Cyvadra/hephaestus/pkg/channels/qq"
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
	warnIfExposed(cfg.ListenAddr)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	notifier := notify.New(cfg.WeComWebhookURL)
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := notifier.Shutdown(shutdownCtx); err != nil {
			log.Printf("notify shutdown: %v", err)
		}
	}()

	toolReg := toolkit.NewRegistry()

	db, err := store.Open(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	projects, err := project.New(db, cfg.ProjectsRoot)
	if err != nil {
		log.Fatalf("project: %v", err)
	}
	if _, err := projects.EnsureDefault(); err != nil {
		log.Fatalf("project: ensure default: %v", err)
	}
	llmClient := llm.NewWithLocalModel(cfg.DeepSeekAPIKey, cfg.LocalModelURL, cfg.LocalModelAPIKey)
	sessions := session.New(db)
	subagentSvc := subagent.New(db, cfg.SubagentMaxDepth)
	toolReg.Register(tools.NewChatHistorySearchTool(db, sessions))
	toolReg.Register(tools.NewChatHistoryReadTool(db, sessions))
	toolReg.Register(tools.NewCreateProjectTool(projects))
	toolReg.Register(tools.NewListProjectsTool(projects))
	toolReg.Register(tools.NewSpawnTool(db, subagentSvc))
	toolReg.Register(tools.NewForkTool(db, subagentSvc))
	toolReg.Register(tools.NewSubagentAwaitTool(subagentSvc))
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
	shellTool, err := tools.NewShellToolWithConfig(tools.ShellConfig{
		Enabled:         cfg.ShellEnabled,
		Access:          fileAccess,
		Backend:         cfg.ShellBackend,
		SSHDestination:  cfg.ShellSSHDestination,
		SSHProjectsRoot: cfg.ShellSSHProjectsRoot,
	})
	if err != nil {
		log.Fatalf("shell: %v", err)
	}
	shellTool.SetInteractionManager(interactions)
	toolReg.Register(shellTool)
	toolReg.Register(tools.NewSendFileTool(cfg.ShellBackend == "local" || cfg.ShellBackend == ""))

	pluginReg := plugin.NewRegistry(notifier)
	weatherClient, err := weather.NewClient(nil, cfg.WeatherProviders)
	if err != nil {
		log.Fatalf("weather: %v", err)
	}
	pluginReg.Register(builtin.NewMetaphysicsPlugin(builtin.MetaphysicsConfig{
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

	_, templates, err := registry.LoadTemplates(cfg.ConfigDir)
	if err != nil {
		log.Fatalf("registry: %v", err)
	}
	reg, syncResult, err := registry.SyncTemplates(db, templates, toolReg.KnownNames(), pluginReg.KnownNames())
	if err != nil {
		log.Fatalf("registry: %v", err)
	}
	registryStore := registry.NewStore(reg)
	configs, err := registry.NewService(db, registryStore, toolReg.KnownNames(), pluginReg.KnownNames(), pluginReg.Descriptions())
	if err != nil {
		log.Fatalf("registry: configuration service: %v", err)
	}
	log.Printf("registry: synchronized templates (created=%d updated=%d preserved=%d)", syncResult.Created, syncResult.Updated, syncResult.Preserved)
	if len(reg.Workflows) > 0 || len(reg.Jobs) > 0 {
		log.Printf("registry: loaded %d workflow(s) and %d job(s)", len(reg.Workflows), len(reg.Jobs))
	}

	agentRunner := agent.NewRunner(llmClient, pluginReg, interactions, db, notifier)
	workflowSvc := workflow.NewService(db, registryStore, toolReg, agentRunner, projects, notifier)
	jobSvc := job.NewService(db, registryStore, workflowSvc, notifier)
	if err := workflowSvc.Reconcile(); err != nil {
		log.Fatalf("workflow: reconcile stale runs: %v", err)
	}
	if err := jobSvc.Reconcile(); err != nil {
		log.Fatalf("job: reconcile stale runs: %v", err)
	}

	pipeline := chat.NewPipeline(db, registryStore, toolReg, pluginReg, llmClient, agentRunner, sessions, notifier, projects, interactions)
	subagentSvc.SetExecutor(subagentexec.NewPipelineExecutor(db, sessions, pipeline))
	pipeline.SetNotificationSource(subagentSvc)
	if err := subagentSvc.Reconcile(); err != nil {
		log.Fatalf("subagents: reconcile stale runs: %v", err)
	}
	chatRunSvc := chatrun.New(db)
	if err := chatRunSvc.Reconcile(); err != nil {
		log.Fatalf("chat runs: reconcile stale runs: %v", err)
	}
	// Deliver any completions that finished while their parent session was
	// idle (or that were rebuilt by subagent Reconcile above).
	dispatcher := resume.New(db, sessions, subagentSvc, chatRunSvc, pipeline)
	subagentSvc.SetOnCompletion(dispatcher.Deliver)
	chatRunSvc.SetOnRunEnded(func(sessionID uint, _ store.ChatRunStatus) {
		dispatcher.Deliver(sessionID)
	})
	if err := dispatcher.Sweep(); err != nil {
		log.Fatalf("subagents: sweep pending completions: %v", err)
	}
	commands := command.NewService(registryStore, toolReg, pluginReg, sessions, notifier, db, projects, interactions)
	commands.SetRunCanceler(chatRunSvc.CancelSession)
	var configuredChannels []channels.Channel
	if cfg.QQAppID != "" {
		qqChannel, err := channels.New("qq", channelqq.Config{
			AppID: cfg.QQAppID, AppSecret: cfg.QQAppSecret, UserOpenID: cfg.QQUserOpenID,
		})
		if err != nil {
			log.Fatalf("channel: configure qq: %v", err)
		}
		configuredChannels = append(configuredChannels, qqChannel)
	}
	channelService := channelruntime.New(db, registryStore, sessions, pipeline, commands, projects, interactions, configuredChannels...)
	if err := channelService.Start(ctx); err != nil {
		log.Fatalf("channel: %v", err)
	}
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

	authService, err := auth.New(auth.Config{Username: cfg.AuthUsername, Password: cfg.AuthPassword, Secret: cfg.JWTSecret})
	if err != nil {
		log.Fatalf("auth: %v", err)
	}
	srv := server.New(authService, registryStore, sessions, pipeline, commands, projects, uploads, configs, workflowSvc, jobSvc, chatRunSvc, subagentSvc)
	scheduler := job.NewScheduler(jobSvc, registryStore, db, notifier)
	var schedulerWG sync.WaitGroup
	schedulerWG.Add(1)
	go func() {
		defer schedulerWG.Done()
		scheduler.Run(ctx)
	}()

	if err := srv.Run(ctx, cfg.ListenAddr); err != nil {
		log.Printf("server: %v", err)
	}
	// The server returned after ctx was canceled: stop the scheduler, cancel
	// any active runs, and wait for workers to finalize their statuses.
	workflowSvc.Shutdown()
	chatRunSvc.Shutdown()
	subagentSvc.Shutdown()
	jobSvc.Shutdown()
	stop()
	if err := channelService.Stop(context.Background()); err != nil {
		log.Printf("channel shutdown: %v", err)
	}
	schedulerWG.Wait()
}

type ocrRecognizer struct{}

// warnIfExposed logs a warning when the API binds to a non-loopback
// address, because exposing a process that can execute local shell commands
// requires TLS and trusted network boundaries even when login is enabled.
func warnIfExposed(addr string) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return
	}
	switch strings.Trim(host, "[]") {
	case "", "127.0.0.1", "::1", "localhost":
		return
	}
	log.Printf("warning: HEPHAESTUS_LISTEN_ADDR %q is not loopback; serve the authenticated API through TLS", addr)
}

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
