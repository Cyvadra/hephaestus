package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Cyvadra/hephaestus/internal/project"
	"github.com/Cyvadra/hephaestus/internal/session"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// searchSessions godoc
//
//	@Summary		Search session titles and summaries
//	@Description	Fast tier of chat history search: case-insensitive substring match against Title/Summary, scoped to one Project.
//	@Tags			sessions
//	@Produce		json
//	@Param			project	query		string	false	"Project name (defaults to the default project)"
//	@Param			q		query		string	true	"Search query"
//	@Success		200		{array}		store.Session
//	@Failure		404		{object}	errorResponse
//	@Failure		500		{object}	errorResponse
//	@Router			/search/sessions [get]
func (s *Server) searchSessions(c *gin.Context) {
	query := strings.TrimSpace(c.Query("q"))
	if query == "" {
		c.JSON(http.StatusOK, []store.Session{})
		return
	}

	boundProject, ok := s.resolveProjectQuery(c)
	if !ok {
		return
	}

	sessions, err := s.sessions.SearchByTitle(boundProject.ID, query, 20)
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, sessions)
}

// searchMessages godoc
//
//	@Summary		Full-text search chat messages
//	@Description	Slow tier of chat history search: case-insensitive substring match against message content, scoped to one Project, paginated.
//	@Tags			sessions
//	@Produce		json
//	@Param			project	query		string	false	"Project name (defaults to the default project)"
//	@Param			q		query		string	true	"Search query"
//	@Param			limit	query		int		false	"Max results (default 50, max 200)"
//	@Param			offset	query		int		false	"Result offset"
//	@Success		200		{array}		session.MessageSearchResult
//	@Failure		404		{object}	errorResponse
//	@Failure		500		{object}	errorResponse
//	@Router			/search/messages [get]
func (s *Server) searchMessages(c *gin.Context) {
	query := strings.TrimSpace(c.Query("q"))
	if query == "" {
		c.JSON(http.StatusOK, []session.MessageSearchResult{})
		return
	}

	boundProject, ok := s.resolveProjectQuery(c)
	if !ok {
		return
	}

	limit, _ := strconv.Atoi(c.Query("limit"))
	offset, _ := strconv.Atoi(c.Query("offset"))

	results, err := s.sessions.SearchMessages(boundProject.ID, query, limit, offset)
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, results)
}

// resolveProjectQuery looks up the Project named by the "project" query
// parameter (defaulting like listSessions), writing an error response and
// returning ok=false if it can't be resolved.
func (s *Server) resolveProjectQuery(c *gin.Context) (*store.Project, bool) {
	projectName := strings.TrimSpace(c.Query("project"))
	if projectName == "" {
		projectName = project.DefaultName
	}
	boundProject, err := s.projects.GetByName(projectName)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, errorResponse{Error: "project not found: " + projectName})
			return nil, false
		}
		internalError(c, err)
		return nil, false
	}
	return boundProject, true
}
