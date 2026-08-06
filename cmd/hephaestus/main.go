// Hephaestus: a single-user LLM<->human interaction framework.
//
//	@title			Hephaestus API
//	@version		0.1
//	@description	Single-user AI agent framework: sessions, chat turns, slash commands.
//	@BasePath		/api/v1
package main

import (
	"log"
	"time"

	_ "github.com/Cyvadra/hephaestus/docs/swagger"
	"github.com/Cyvadra/hephaestus/internal/bootstrap"
	"github.com/Cyvadra/hephaestus/internal/chat"
	"github.com/Cyvadra/hephaestus/internal/command"
	"github.com/Cyvadra/hephaestus/internal/llm"
	"github.com/Cyvadra/hephaestus/internal/notify"
	"github.com/Cyvadra/hephaestus/internal/plugin"
	"github.com/Cyvadra/hephaestus/internal/plugin/builtin"
	"github.com/Cyvadra/hephaestus/internal/registry"
	"github.com/Cyvadra/hephaestus/internal/server"
	"github.com/Cyvadra/hephaestus/internal/session"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/Cyvadra/hephaestus/internal/tools"
)

func main() {
	cfg, err := bootstrap.Load()
	if err != nil {
		log.Fatalf("bootstrap: %v", err)
	}

	notifier := notify.New(cfg.WeComWebhookURL)

	toolReg := tools.NewRegistry()
	tools.RegisterBuiltins(toolReg)

	db, err := store.Open(cfg.PostgresDSN)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	llmClient := llm.New(cfg.DeepSeekAPIKey)
	sessions := session.New(db)
	toolReg.Register(tools.NewChatHistorySearchTool(db, sessions))

	pluginReg := plugin.NewRegistry(notifier)
	pluginReg.Register(builtin.NewSessionSummaryPlugin(db, llmClient, 5*time.Minute))
	pluginReg.Register(builtin.NewStorylineStatusPlugin(db, llmClient))
	pluginReg.Register(builtin.NewOptionsPlugin(llmClient))

	reg, err := registry.Load(cfg.ConfigDir)
	if err != nil {
		log.Fatalf("registry: %v", err)
	}
	if err := reg.Validate(toolReg.KnownNames(), pluginReg.KnownNames()); err != nil {
		log.Fatalf("registry: validation failed: %v", err)
	}

	pipeline := chat.NewPipeline(db, reg, toolReg, pluginReg, llmClient, sessions, notifier)
	commands := command.NewService(reg, toolReg, pluginReg, sessions, notifier, db)

	srv := server.New(db, reg, sessions, pipeline, commands)
	if err := srv.Run(cfg.ListenAddr); err != nil {
		log.Fatalf("server: %v", err)
	}
}
