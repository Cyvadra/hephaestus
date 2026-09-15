// Package server exposes the platform over HTTP using Gin, with Swagger
// documentation generated from the handler annotations below (run
// `make swagger` to regenerate).
package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Cyvadra/hephaestus/internal/auth"
	"github.com/Cyvadra/hephaestus/internal/chat"
	"github.com/Cyvadra/hephaestus/internal/chatrun"
	"github.com/Cyvadra/hephaestus/internal/command"
	"github.com/Cyvadra/hephaestus/internal/project"
	"github.com/Cyvadra/hephaestus/internal/registry"
	"github.com/Cyvadra/hephaestus/internal/session"
	"github.com/Cyvadra/hephaestus/internal/subagent"
	"github.com/Cyvadra/hephaestus/internal/upload"
	"github.com/gin-gonic/gin"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
)

// Server exposes Hephaestus sessions, chat turns, and workflow/job runs over HTTP.
type Server struct {
	engine     *gin.Engine
	auth       *auth.Service
	registries *registry.Store
	sessions   *session.Service
	pipeline   *chat.Pipeline
	commands   *command.Service
	projects   *project.Service
	uploads    *upload.Processor
	configs    *registry.Service
	workflows  workflowRunner
	jobs       jobRunner
	chatRuns   *chatrun.Service
	subagents  subagentRunner
	// streamDoneGrace keeps a workflow-run SSE connection open after the
	// done event so the client can close its EventSource instead of the
	// browser auto-reconnecting. Zero disables the grace (tests).
	streamDoneGrace time.Duration
	// secureCookies issues session cookies with the Secure attribute even
	// when this process serves plain HTTP, for deployments that terminate
	// HTTPS in a reverse proxy.
	secureCookies bool
	// version is reported by the health endpoint.
	version string
}

// SetSecureCookies forces the Secure attribute on session cookies. Use it
// when HTTPS is terminated in front of Hephaestus, where the request this
// process sees is plain HTTP and cannot be detected as secure.
func (s *Server) SetSecureCookies(secure bool) { s.secureCookies = secure }

// SetVersion records the build version reported by GET /healthz.
func (s *Server) SetVersion(version string) { s.version = version }

// cookieSecure reports whether session cookies should carry the Secure
// attribute: either this connection is TLS, or a proxy terminated it.
func (s *Server) cookieSecure(c *gin.Context) bool {
	return s.secureCookies || c.Request.TLS != nil
}

// New builds the Gin engine and registers every route.
func New(authService *auth.Service, registries *registry.Store, sessions *session.Service, pipeline *chat.Pipeline, commands *command.Service, projects *project.Service, uploads *upload.Processor, configs *registry.Service, workflows workflowRunner, jobs jobRunner, chatRuns *chatrun.Service, subagents subagentRunner) *Server {
	s := &Server{
		engine:          gin.Default(),
		auth:            authService,
		registries:      registries,
		sessions:        sessions,
		pipeline:        pipeline,
		commands:        commands,
		projects:        projects,
		uploads:         uploads,
		configs:         configs,
		workflows:       workflows,
		jobs:            jobs,
		chatRuns:        chatRuns,
		subagents:       subagents,
		streamDoneGrace: 3 * time.Second,
	}

	s.engine.GET("/healthz", s.health)

	public := s.engine.Group("/api/v1")
	public.POST("/auth/login", s.login)

	api := s.engine.Group("/api/v1")
	api.Use(s.requireAuthentication)
	api.GET("/auth/session", s.authSession)
	api.POST("/auth/logout", s.logout)
	api.GET("/sessions", s.listSessions)
	api.GET("/search/sessions", s.searchSessions)
	api.GET("/search/messages", s.searchMessages)
	api.POST("/sessions", s.createSession)
	api.PATCH("/sessions/:id", s.updateSession)
	api.DELETE("/sessions/:id", s.deleteSession)
	api.GET("/sessions/:id/history", s.getHistory)
	api.GET("/sessions/:id/chat-run", s.getActiveChatRun)
	api.GET("/sessions/:id/chat-run/steering", s.getSteering)
	api.PUT("/sessions/:id/chat-run/steering", s.putSteering)
	api.DELETE("/sessions/:id/chat-run/steering", s.cancelSteering)
	api.POST("/sessions/:id/chat-runs", s.startChatRun)
	api.POST("/sessions/:id/chat-runs/cancel", s.cancelActiveChatRun)
	api.GET("/sessions/:id/attachments/:attachmentID/download", s.downloadAttachment)
	api.POST("/sessions/:id/messages", s.sendMessage)
	api.POST("/sessions/:id/interactions/:requestID/responses", s.respondToQuestions)
	api.POST("/sessions/:id/messages/:messageID/fork", s.forkSessionAtMessage)
	api.POST("/sessions/:id/messages/:messageID/edit", s.editAssistantMessage)
	api.POST("/sessions/:id/regenerate", s.regenerate)
	api.GET("/chat-runs/:id", s.getChatRun)
	api.GET("/chat-runs/:id/stream", s.streamChatRun)
	api.POST("/chat-runs/:id/cancel", s.cancelChatRun)
	api.GET("/sessions/:id/subagent-runs", s.listSubagentRuns)
	api.GET("/subagent-runs/:id", s.getSubagentRun)
	api.POST("/subagent-runs/:id/cancel", s.cancelSubagentRun)
	api.GET("/concierges", s.listConcierges)
	api.GET("/projects", s.listProjects)
	api.POST("/projects", s.createProject)
	api.DELETE("/projects/:name", s.deleteProject)
	api.GET("/configurations/catalog", s.configurationCatalog)
	api.POST("/configurations/complete", s.completeConfiguration)
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

	s.engine.GET("/swagger/*any", s.requireAuthentication, ginSwagger.WrapHandler(swaggerFiles.Handler))

	return s
}

// health reports process liveness and build version.
//
//	@Summary		Health check
//	@Description	Reports process liveness and build version. This endpoint is public and touches no database.
//	@Tags			meta
//	@Produce		json
//	@Success		200	{object}	map[string]string
//	@Router			/healthz [get]
func (s *Server) health(c *gin.Context) {
	version := s.version
	if version == "" {
		version = "dev"
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "version": version})
}

var _ subagentRunner = (*subagent.Service)(nil)

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
