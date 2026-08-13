// Package server exposes the platform over HTTP using Gin, with Swagger
// documentation generated from the handler annotations below (run
// `swag init -g internal/server/server.go -o docs/swagger` to regenerate).
package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Cyvadra/hephaestus/internal/chat"
	"github.com/Cyvadra/hephaestus/internal/command"
	"github.com/Cyvadra/hephaestus/internal/project"
	"github.com/Cyvadra/hephaestus/internal/registry"
	"github.com/Cyvadra/hephaestus/internal/session"
	"github.com/Cyvadra/hephaestus/internal/upload"
	"github.com/gin-gonic/gin"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
)

// Server exposes Hephaestus sessions, chat turns, and workflow/job runs over HTTP.
type Server struct {
	engine     *gin.Engine
	registries *registry.Store
	sessions   *session.Service
	pipeline   *chat.Pipeline
	commands   *command.Service
	projects   *project.Service
	uploads    *upload.Processor
	configs    *registry.Service
	workflows  workflowRunner
	jobs       jobRunner
	// streamDoneGrace keeps a workflow-run SSE connection open after the
	// done event so the client can close its EventSource instead of the
	// browser auto-reconnecting. Zero disables the grace (tests).
	streamDoneGrace time.Duration
}

// New builds the Gin engine and registers every route.
func New(registries *registry.Store, sessions *session.Service, pipeline *chat.Pipeline, commands *command.Service, projects *project.Service, uploads *upload.Processor, configs *registry.Service, workflows workflowRunner, jobs jobRunner) *Server {
	s := &Server{
		engine:          gin.Default(),
		registries:      registries,
		sessions:        sessions,
		pipeline:        pipeline,
		commands:        commands,
		projects:        projects,
		uploads:         uploads,
		configs:         configs,
		workflows:       workflows,
		jobs:            jobs,
		streamDoneGrace: 3 * time.Second,
	}

	api := s.engine.Group("/api/v1")
	api.GET("/sessions", s.listSessions)
	api.POST("/sessions", s.createSession)
	api.PATCH("/sessions/:id", s.updateSession)
	api.DELETE("/sessions/:id", s.deleteSession)
	api.GET("/sessions/:id/history", s.getHistory)
	api.GET("/sessions/:id/attachments/:attachmentID/download", s.downloadAttachment)
	api.POST("/sessions/:id/messages", s.sendMessage)
	api.POST("/sessions/:id/messages/:messageID/fork", s.forkSessionAtMessage)
	api.POST("/sessions/:id/messages/:messageID/edit", s.editAssistantMessage)
	api.POST("/sessions/:id/messages/stream", s.streamMessage)
	api.POST("/sessions/:id/regenerate", s.regenerate)
	api.POST("/sessions/:id/regenerate/stream", s.streamRegenerate)
	api.POST("/sessions/:id/messages/:messageID/continue/stream", s.streamContinue)
	api.GET("/concierges", s.listConcierges)
	api.GET("/projects", s.listProjects)
	api.POST("/projects", s.createProject)
	api.DELETE("/projects/:name", s.deleteProject)
	api.GET("/configurations/catalog", s.configurationCatalog)
	api.GET("/configurations/:kind", s.listConfigurations)
	api.POST("/configurations/:kind", s.createConfiguration)
	api.GET("/configurations/:kind/:name", s.getConfiguration)
	api.PUT("/configurations/:kind/:name", s.replaceConfiguration)
	api.DELETE("/configurations/:kind/:name", s.deleteConfiguration)
	api.POST("/workflows/:name/runs", s.startWorkflowRun)
	api.GET("/workflow-runs", s.listWorkflowRuns)
	api.GET("/workflow-runs/:id", s.getWorkflowRun)
	api.GET("/workflow-runs/:id/stream", s.streamWorkflowRun)
	api.POST("/workflow-runs/:id/cancel", s.cancelWorkflowRun)
	api.GET("/job-runs", s.listJobRuns)
	api.GET("/job-runs/:id", s.getJobRun)
	api.POST("/job-runs/:id/cancel", s.cancelJobRun)

	s.engine.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	return s
}

// Run serves until ctx is canceled, then drains in-flight requests.
func (s *Server) Run(ctx context.Context, addr string) error {
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           s.engine,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- httpServer.ListenAndServe() }()

	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return nil
	}
}
