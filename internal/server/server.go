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
	"github.com/Cyvadra/hephaestus/internal/registry"
	"github.com/Cyvadra/hephaestus/internal/session"
	"github.com/gin-gonic/gin"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
	"gorm.io/gorm"
)

// Server exposes Hephaestus sessions and chat turns over HTTP.
type Server struct {
	engine   *gin.Engine
	db       *gorm.DB
	reg      *registry.Registry
	sessions *session.Service
	pipeline *chat.Pipeline
	commands *command.Service
}

// New builds the Gin engine and registers every route.
func New(db *gorm.DB, reg *registry.Registry, sessions *session.Service, pipeline *chat.Pipeline, commands *command.Service) *Server {
	s := &Server{
		engine:   gin.Default(),
		db:       db,
		reg:      reg,
		sessions: sessions,
		pipeline: pipeline,
		commands: commands,
	}

	api := s.engine.Group("/api/v1")
	api.GET("/sessions", s.listSessions)
	api.POST("/sessions", s.createSession)
	api.GET("/sessions/:id/history", s.getHistory)
	api.POST("/sessions/:id/messages", s.sendMessage)
	api.POST("/sessions/:id/messages/:messageID/edit", s.editAssistantMessage)
	api.POST("/sessions/:id/messages/stream", s.streamMessage)
	api.POST("/sessions/:id/regenerate", s.regenerate)
	api.POST("/sessions/:id/regenerate/stream", s.streamRegenerate)
	api.GET("/concierges", s.listConcierges)

	s.engine.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	return s
}

// Run serves until ctx is canceled, then drains in-flight requests.
func (s *Server) Run(ctx context.Context, addr string) error {
	httpServer := &http.Server{Addr: addr, Handler: s.engine}
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
