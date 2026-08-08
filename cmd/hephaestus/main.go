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
	"syscall"
	"time"

	_ "github.com/Cyvadra/hephaestus/docs/swagger"
	"github.com/Cyvadra/hephaestus/internal/bootstrap"
	"github.com/Cyvadra/hephaestus/internal/chat"
	"github.com/Cyvadra/hephaestus/internal/command"
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
	tools.RegisterPlaceholderTools(toolReg)

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
	llmClient := llm.New(cfg.DeepSeekAPIKey)
	sessions := session.New(db)
	if err := session.BindUnscopedSessions(db, defaultProject.ID); err != nil {
		log.Fatalf("session: bind default project: %v", err)
	}
	toolReg.Register(tools.NewChatHistorySearchTool(db, sessions))
	toolReg.Register(tools.NewCreateProjectTool(projects))
	toolReg.Register(tools.NewListProjectsTool(projects))
	fileAccess := tools.FileAccessConfig{AllowOutsideProject: cfg.ProjectAccessOverride}
	toolReg.Register(tools.NewReadFileTool(fileAccess))
	toolReg.Register(tools.NewReadFileLinesTool(fileAccess))
	toolReg.Register(tools.NewWriteFileTool(fileAccess))
	toolReg.Register(tools.NewEditFileTool(fileAccess))
	toolReg.Register(tools.NewAppendFileTool(fileAccess))
	toolReg.Register(tools.NewListDirTool(fileAccess))
	webFetch, err := tools.NewWebFetchTool(tools.WebFetchConfig{
		Provider:        cfg.WebFetchProvider,
		FirecrawlAPIKey: cfg.FirecrawlAPIKey,
		ChromePath:      cfg.WebFetchChromePath,
	})
	if err != nil {
		log.Fatalf("web fetch: %v", err)
	}
	toolReg.Register(webFetch)
	webSearch := tools.NewWebSearchTool(tools.WebSearchConfig{BraveAPIKeys: cfg.WebSearchBraveAPIKeys, TavilyAPIKeys: cfg.WebSearchTavilyAPIKeys, SerpAPIKeys: cfg.WebSearchSerpAPIKeys, SerpAPIEngine: cfg.WebSearchSerpAPIEngine, SearXNGBaseURL: cfg.WebSearchSearXNGBaseURL})
	toolReg.Register(webSearch)
	execTool := tools.NewExecToolWithAccess(cfg.ExecEnabled, 0, fileAccess)
	toolReg.Register(execTool)

	pluginReg := plugin.NewRegistry(notifier)
	pluginReg.Register(builtin.NewSessionSummaryPlugin(db, llmClient, 5*time.Minute))
	pluginReg.Register(builtin.NewStorylineStatusPlugin(db, llmClient))
	pluginReg.Register(builtin.NewOptionsPlugin(llmClient))
	if err := pluginReg.SetFixedPlugins(cfg.FixedPlugins); err != nil {
		log.Fatalf("plugin: configure fixed plugins: %v", err)
	}

	reg, err := registry.Load(cfg.ConfigDir)
	if err != nil {
		log.Fatalf("registry: %v", err)
	}
	if err := reg.Validate(toolReg.KnownNames(), pluginReg.KnownNames()); err != nil {
		log.Fatalf("registry: validation failed: %v", err)
	}
	if len(reg.Workflows) > 0 || len(reg.Jobs) > 0 {
		log.Printf("registry: loaded %d workflow(s) and %d job(s); no scheduler is implemented yet, so they will not run", len(reg.Workflows), len(reg.Jobs))
	}

	pipeline := chat.NewPipeline(db, reg, toolReg, pluginReg, llmClient, sessions, notifier, projects)
	commands := command.NewService(reg, toolReg, pluginReg, sessions, notifier, db, projects)

	srv := server.New(db, reg, sessions, pipeline, commands, projects)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := srv.Run(ctx, cfg.ListenAddr); err != nil {
		execTool.Shutdown()
		log.Printf("server: %v", err)
		return
	}
	execTool.Shutdown()
}
