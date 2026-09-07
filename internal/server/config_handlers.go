package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Cyvadra/ds4"
	"github.com/Cyvadra/hephaestus/internal/registry"
	"github.com/gin-gonic/gin"
)

type configurationCompletionRequest struct {
	Kind         registry.Kind       `json:"kind" binding:"required"`
	Identity     registry.Identity   `json:"identity"`
	Impression   registry.Impression `json:"impression"`
	BaseIdentity *registry.Identity  `json:"base_identity"`
	Messages     []registry.Message  `json:"messages"`
	UserMessage  string              `json:"user_message" binding:"required"`
}

// completeConfiguration godoc
//
//	@Summary		Complete a configuration message
//	@Description	Streams a non-persistent assistant reference response using the submitted configuration snapshot.
//	@Tags		configurations
//	@Accept		json
//	@Produce		text/event-stream
//	@Param		request	body	configurationCompletionRequest	true	"Configuration completion payload"
//	@Success		200	{string}	string
//	@Failure		400	{object}	errorResponse
//	@Router		/configurations/complete [post]
func (s *Server) completeConfiguration(c *gin.Context) {
	var req configurationCompletionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	if req.Kind != registry.KindIdentity && req.Kind != registry.KindImpression {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "kind must be identities or impressions"})
		return
	}
	identity := req.Identity
	messages := req.Messages
	if req.Kind == registry.KindImpression {
		if req.BaseIdentity == nil || req.BaseIdentity.Name == "" {
			c.JSON(http.StatusBadRequest, errorResponse{Error: "base_identity is required for impressions"})
			return
		}
		identity = *req.BaseIdentity
		messages = append([]registry.Message(nil), req.Messages...)
	}
	if identity.Name == "" {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "identity is required"})
		return
	}
	if err := validateCompletionMessages(messages); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache, no-transform")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	c.Writer.Flush()
	sequence := uint64(0)
	emit := func(event string, data any) {
		c.SSEvent(event, streamEventEnvelope{Sequence: sequence, Data: data})
		sequence++
		c.Writer.Flush()
	}
	_, err := s.pipeline.CompleteConfiguration(c.Request.Context(), identity, messages, req.UserMessage, func(delta string) {
		emit("delta", gin.H{"text": delta})
	})
	if err != nil {
		emit("error", err.Error())
		return
	}
	emit("done", gin.H{"status": "succeeded"})
}

func validateCompletionMessages(messages []registry.Message) error {
	for index, message := range messages {
		switch message.Role {
		case ds4.RoleSystem, ds4.RoleUser, ds4.RoleAssistant:
		default:
			return fmt.Errorf("completion message %d has invalid role %q", index+1, message.Role)
		}
	}
	return nil
}

// configurationCatalog godoc
//
//	@Summary		List available configuration references
//	@Description	Returns names from the active database registry and registered tools/plugins.
//	@Tags			configurations
//	@Produce		json
//	@Success		200	{object}	registry.Catalog
//	@Failure		500	{object}	errorResponse
//	@Router			/configurations/catalog [get]
func (s *Server) configurationCatalog(c *gin.Context) {
	catalog, err := s.configs.Catalog()
	if err != nil {
		configurationError(c, err)
		return
	}
	c.JSON(http.StatusOK, catalog)
}

// listConfigurations godoc
//
//	@Summary		List configurations
//	@Description	Returns all database-backed configuration records. The kind must be identities, impressions, tool-groups, concierges, workflows, jobs, or constants.
//	@Tags			configurations
//	@Produce		json
//	@Param			kind	path	string	true	"Configuration kind"
//	@Success		200	{array}	object
//	@Failure		400	{object}	errorResponse
//	@Router			/configurations/{kind} [get]
func (s *Server) listConfigurations(c *gin.Context) {
	kind := configurationKind(c)
	values, err := s.configs.List(kind)
	if err != nil {
		configurationError(c, err)
		return
	}
	response, err := s.withConciergeAvailability(kind, values)
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, response)
}

// getConfiguration godoc
//
//	@Summary		Get a persisted configuration
//	@Tags			configurations
//	@Produce		json
//	@Param			kind	path	string	true	"Configuration kind"
//	@Param			name	path	string	true	"Configuration name"
//	@Success		200	{object}	object
//	@Failure		400	{object}	errorResponse
//	@Failure		404	{object}	errorResponse
//	@Router			/configurations/{kind}/{name} [get]
func (s *Server) getConfiguration(c *gin.Context) {
	kind := configurationKind(c)
	value, err := s.configs.Get(kind, c.Param("name"))
	if err != nil {
		configurationError(c, err)
		return
	}
	response, err := s.withConciergeAvailability(kind, value)
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, response)
}

// createConfiguration godoc
//
//	@Summary		Create a persisted configuration
//	@Description	Persists a configuration. The change becomes active for new requests and subsequent chat turns immediately.
//	@Tags			configurations
//	@Accept			json
//	@Produce		json
//	@Param			kind		path	string	true	"Configuration kind"
//	@Param			request	body	object	true	"Configuration payload determined by kind"
//	@Success		201	{object}	object
//	@Failure		400	{object}	errorResponse
//	@Failure		409	{object}	errorResponse
//	@Router			/configurations/{kind} [post]
func (s *Server) createConfiguration(c *gin.Context) {
	kind := configurationKind(c)
	value, availableProjects, err := decodeConfiguration(c, kind)
	if err != nil {
		configurationError(c, err)
		return
	}
	if kind == registry.KindConcierge {
		if err := s.projects.ValidateNames(availableProjects); err != nil {
			configurationError(c, err)
			return
		}
	}
	if err := s.configs.Create(kind, value); err != nil {
		configurationError(c, err)
		return
	}
	if kind == registry.KindConcierge {
		name, _ := registry.ValueName(kind, value)
		if err := s.projects.SetConciergeAvailability(name, availableProjects); err != nil {
			internalError(c, err)
			return
		}
	}
	response, err := s.withConciergeAvailability(kind, value)
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusCreated, response)
}

// replaceConfiguration godoc
//
//	@Summary		Replace a persisted configuration
//	@Description	Replaces a configuration. The path and payload names must match, and the change becomes active for new requests and subsequent chat turns immediately.
//	@Tags			configurations
//	@Accept			json
//	@Produce		json
//	@Param			kind		path	string	true	"Configuration kind"
//	@Param			name		path	string	true	"Configuration name"
//	@Param			request	body	object	true	"Configuration payload determined by kind"
//	@Success		200	{object}	object
//	@Failure		400	{object}	errorResponse
//	@Failure		404	{object}	errorResponse
//	@Failure		409	{object}	errorResponse
//	@Router			/configurations/{kind}/{name} [put]
func (s *Server) replaceConfiguration(c *gin.Context) {
	kind := configurationKind(c)
	value, availableProjects, err := decodeConfiguration(c, kind)
	if err != nil {
		configurationError(c, err)
		return
	}
	name, err := registry.ValueName(kind, value)
	if err != nil {
		configurationError(c, err)
		return
	}
	if name != c.Param("name") {
		configurationError(c, fmt.Errorf("configuration name must match path name"))
		return
	}
	if kind == registry.KindConcierge {
		if err := s.projects.ValidateNames(availableProjects); err != nil {
			configurationError(c, err)
			return
		}
	}
	if err := s.configs.Replace(kind, value); err != nil {
		configurationError(c, err)
		return
	}
	if kind == registry.KindConcierge {
		if err := s.projects.SetConciergeAvailability(name, availableProjects); err != nil {
			internalError(c, err)
			return
		}
	}
	response, err := s.withConciergeAvailability(kind, value)
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, response)
}

// deleteConfiguration godoc
//
//	@Summary		Delete a persisted configuration
//	@Description	Deletes a configuration immediately. A same-named default template is restored only after the next process start.
//	@Tags			configurations
//	@Param			kind	path	string	true	"Configuration kind"
//	@Param			name	path	string	true	"Configuration name"
//	@Success		204
//	@Failure		400	{object}	errorResponse
//	@Failure		404	{object}	errorResponse
//	@Failure		409	{object}	errorResponse
//	@Router			/configurations/{kind}/{name} [delete]
func (s *Server) deleteConfiguration(c *gin.Context) {
	kind := configurationKind(c)
	name := c.Param("name")
	if err := s.configs.Delete(kind, name); err != nil {
		configurationError(c, err)
		return
	}
	if kind == registry.KindConcierge {
		if err := s.projects.RemoveConciergeFromProjects(name); err != nil {
			internalError(c, err)
			return
		}
	}
	c.Status(http.StatusNoContent)
}

func configurationKind(c *gin.Context) registry.Kind {
	return registry.Kind(c.Param("kind"))
}

type conciergeConfigurationPayload struct {
	registry.Concierge
	AvailableProjects []string `json:"available_projects"`
}

func decodeConfiguration(c *gin.Context, kind registry.Kind) (any, []string, error) {
	if kind == registry.KindConcierge {
		var payload conciergeConfigurationPayload
		decoder := json.NewDecoder(c.Request.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&payload); err != nil {
			return nil, nil, fmt.Errorf("invalid configuration payload: %w", err)
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			return nil, nil, fmt.Errorf("invalid configuration payload: expected one JSON value")
		}
		return &payload.Concierge, payload.AvailableProjects, nil
	}
	value, err := registry.NewValue(kind)
	if err != nil {
		return nil, nil, err
	}
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return nil, nil, fmt.Errorf("invalid configuration payload: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, nil, fmt.Errorf("invalid configuration payload: expected one JSON value")
	}
	return value, nil, nil
}

func (s *Server) withConciergeAvailability(kind registry.Kind, value any) (any, error) {
	if kind != registry.KindConcierge {
		return value, nil
	}
	projects, err := s.projects.List()
	if err != nil {
		return nil, err
	}
	availableProjects := func(name string) []string {
		result := make([]string, 0)
		for _, p := range projects {
			if s.projects.IsConciergeAvailable(p, name) {
				result = append(result, p.Name)
			}
		}
		return result
	}
	switch typed := value.(type) {
	case *registry.Concierge:
		return conciergeConfigurationPayload{Concierge: *typed, AvailableProjects: availableProjects(typed.Name)}, nil
	case []registry.Concierge:
		result := make([]conciergeConfigurationPayload, 0, len(typed))
		for _, concierge := range typed {
			result = append(result, conciergeConfigurationPayload{Concierge: concierge, AvailableProjects: availableProjects(concierge.Name)})
		}
		return result, nil
	default:
		return nil, fmt.Errorf("configuration: unexpected concierge payload %T", value)
	}
}

func configurationError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, registry.ErrInvalidKind):
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
	case errors.Is(err, registry.ErrNotFound):
		c.JSON(http.StatusNotFound, errorResponse{Error: err.Error()})
	case errors.Is(err, registry.ErrExists):
		c.JSON(http.StatusConflict, errorResponse{Error: err.Error()})
	case errors.Is(err, registry.ErrConflict):
		c.JSON(http.StatusConflict, errorResponse{Error: err.Error()})
	case strings.HasPrefix(err.Error(), "registry:") || strings.HasPrefix(err.Error(), "project:") || strings.HasPrefix(err.Error(), "invalid configuration") || strings.HasPrefix(err.Error(), "configuration name"):
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
	default:
		internalError(c, err)
	}
}
