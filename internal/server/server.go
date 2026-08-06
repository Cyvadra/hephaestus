// Package server exposes the platform over HTTP using Gin, with Swagger
// documentation generated from the handler annotations below (run
// `swag init -g internal/server/server.go -o docs/swagger` to regenerate).
package server

import (
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
	api.POST("/sessions/:id/messages/stream", s.streamMessage)
	api.POST("/sessions/:id/regenerate", s.regenerate)
	api.POST("/sessions/:id/regenerate/stream", s.streamRegenerate)
	api.GET("/concierges", s.listConcierges)

	s.engine.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	return s
}

// Run starts the HTTP server on addr, blocking until it exits.
func (s *Server) Run(addr string) error {
	return s.engine.Run(addr)
}
