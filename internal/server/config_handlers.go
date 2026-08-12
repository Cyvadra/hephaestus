package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Cyvadra/hephaestus/internal/registry"
	"github.com/gin-gonic/gin"
)

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
//	@Description	Returns all database-backed configuration records. The kind must be identities, impressions, tool-groups, concierges, workflows, or jobs.
//	@Tags			configurations
//	@Produce		json
//	@Param			kind	path	string	true	"Configuration kind"
//	@Success		200	{array}	object
//	@Failure		400	{object}	errorResponse
//	@Router			/configurations/{kind} [get]
func (s *Server) listConfigurations(c *gin.Context) {
	values, err := s.configs.List(configurationKind(c))
	if err != nil {
		configurationError(c, err)
		return
	}
	c.JSON(http.StatusOK, values)
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
	value, err := s.configs.Get(configurationKind(c), c.Param("name"))
	if err != nil {
		configurationError(c, err)
		return
	}
	c.JSON(http.StatusOK, value)
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
	value, err := decodeConfiguration(c, kind)
	if err != nil {
		configurationError(c, err)
		return
	}
	if err := s.configs.Create(kind, value); err != nil {
		configurationError(c, err)
		return
	}
	c.JSON(http.StatusCreated, value)
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
	value, err := decodeConfiguration(c, kind)
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
	if err := s.configs.Replace(kind, value); err != nil {
		configurationError(c, err)
		return
	}
	c.JSON(http.StatusOK, value)
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
	if err := s.configs.Delete(configurationKind(c), c.Param("name")); err != nil {
		configurationError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func configurationKind(c *gin.Context) registry.Kind {
	return registry.Kind(c.Param("kind"))
}

func decodeConfiguration(c *gin.Context, kind registry.Kind) (any, error) {
	value, err := registry.NewValue(kind)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return nil, fmt.Errorf("invalid configuration payload: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("invalid configuration payload: expected one JSON value")
	}
	return value, nil
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
	case strings.HasPrefix(err.Error(), "registry:") || strings.HasPrefix(err.Error(), "invalid configuration") || strings.HasPrefix(err.Error(), "configuration name"):
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
	default:
		internalError(c, err)
	}
}
