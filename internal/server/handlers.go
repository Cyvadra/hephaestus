package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"

	"github.com/Cyvadra/hephaestus/internal/chat"
	"github.com/Cyvadra/hephaestus/internal/command"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/gin-gonic/gin"
)

type createSessionRequest struct {
	Concierge string `json:"concierge" binding:"required"`
}

// createSession godoc
//
//	@Summary		Create a session
//	@Description	Creates a new Session from the named Concierge's current settings.
//	@Tags			sessions
//	@Accept			json
//	@Produce		json
//	@Param			request	body		createSessionRequest	true	"Concierge to instantiate"
//	@Success		201		{object}	store.Session
//	@Failure		400		{object}	errorResponse
//	@Failure		404		{object}	errorResponse
//	@Router			/sessions [post]
func (s *Server) createSession(c *gin.Context) {
	var req createSessionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	concierge, ok := s.reg.Concierges[req.Concierge]
	if !ok {
		c.JSON(http.StatusNotFound, errorResponse{Error: "concierge not found: " + req.Concierge})
		return
	}

	sess, err := s.sessions.CreateFromConcierge(concierge)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}
	c.JSON(http.StatusCreated, sess)
}

type historyResponse struct {
	Session  store.Session       `json:"session"`
	Messages []store.ChatMessage `json:"messages"`
}

// getHistory godoc
//
//	@Summary		Get full session history
//	@Description	Returns the session row plus every message in its (unpruned) tree, so the client can reconstruct and switch between branches itself.
//	@Tags			sessions
//	@Produce		json
//	@Param			id	path		int	true	"Session ID"
//	@Success		200	{object}	historyResponse
//	@Failure		404	{object}	errorResponse
//	@Router			/sessions/{id}/history [get]
func (s *Server) getHistory(c *gin.Context) {
	sessionID, err := parseSessionID(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	var sess store.Session
	if err := s.db.First(&sess, sessionID).Error; err != nil {
		c.JSON(http.StatusNotFound, errorResponse{Error: "session not found"})
		return
	}

	var messages []store.ChatMessage
	if err := s.db.Where("session_id = ?", sessionID).Order("id").Find(&messages).Error; err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}

	c.JSON(http.StatusOK, historyResponse{Session: sess, Messages: messages})
}

type sendMessageRequest struct {
	// ActiveLeafMessageID, when set, switches the session onto this
	// branch before the message is processed (see design doc's session
	// branching semantics). Required for every continuation, per doc.
	ActiveLeafMessageID *uint  `json:"active_leaf_message_id"`
	Text                string `json:"text" binding:"required"`
}

type sendMessageResponse struct {
	// CommandResponse is set (and never persisted) when Text was a slash command.
	CommandResponse string `json:"command_response,omitempty"`
	// Message is the persisted final assistant ChatMessage for a chat turn.
	Message *store.ChatMessage `json:"message,omitempty"`
	// Metadata carries any plugin-attached data for this turn (e.g.
	// suggested next-user-message alternatives).
	Metadata map[string]any `json:"metadata,omitempty"`
}

// sendMessage godoc
//
//	@Summary		Send a message
//	@Description	Sends a user message (or slash command) into a session and returns the assistant's reply.
//	@Tags			sessions
//	@Accept			json
//	@Produce		json
//	@Param			id		path		int					true	"Session ID"
//	@Param			request	body		sendMessageRequest	true	"Message to send"
//	@Success		200		{object}	sendMessageResponse
//	@Failure		400		{object}	errorResponse
//	@Failure		500		{object}	errorResponse
//	@Router			/sessions/{id}/messages [post]
func (s *Server) sendMessage(c *gin.Context) {
	sessionID, err := parseSessionID(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	var req sendMessageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	if req.ActiveLeafMessageID != nil {
		if err := s.switchActiveLeaf(sessionID, *req.ActiveLeafMessageID); err != nil {
			c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
			return
		}
	}

	if command.IsCommand(req.Text) {
		resp, err := s.commands.Execute(sessionID, req.Text)
		if err != nil {
			c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
			return
		}
		c.JSON(http.StatusOK, sendMessageResponse{CommandResponse: resp})
		return
	}

	ctx, cancel := context.WithCancel(c.Request.Context())
	s.commands.RegisterCancel(sessionID, cancel)
	defer s.commands.UnregisterCancel(sessionID)

	result, err := s.pipeline.RunTurn(ctx, sessionID, req.Text)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			c.JSON(http.StatusRequestTimeout, errorResponse{Error: "stopped"})
			return
		}
		c.JSON(http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}

	c.JSON(http.StatusOK, sendMessageResponse{Message: result.Message, Metadata: result.Metadata})
}

// streamMessage godoc
//
//	@Summary		Send a message with streaming
//	@Description	Like sendMessage, but streams assistant content deltas as Server-Sent Events ("delta" events), finishing with a "done" event carrying the same body sendMessage would return (or an "error" event).
//	@Tags			sessions
//	@Accept			json
//	@Produce		text/event-stream
//	@Param			id		path	int					true	"Session ID"
//	@Param			request	body	sendMessageRequest	true	"Message to send"
//	@Success		200
//	@Failure		400	{object}	errorResponse
//	@Router			/sessions/{id}/messages/stream [post]
func (s *Server) streamMessage(c *gin.Context) {
	sessionID, err := parseSessionID(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	var req sendMessageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	if req.ActiveLeafMessageID != nil {
		if err := s.switchActiveLeaf(sessionID, *req.ActiveLeafMessageID); err != nil {
			c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
			return
		}
	}

	if command.IsCommand(req.Text) {
		resp, err := s.commands.Execute(sessionID, req.Text)
		if err != nil {
			c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
			return
		}
		c.JSON(http.StatusOK, sendMessageResponse{CommandResponse: resp})
		return
	}

	ctx, cancel := context.WithCancel(c.Request.Context())
	s.commands.RegisterCancel(sessionID, cancel)
	defer s.commands.UnregisterCancel(sessionID)

	deltas := make(chan string, 16)
	resultCh := make(chan *chat.TurnResult, 1)
	errCh := make(chan error, 1)

	go func() {
		defer close(deltas)
		result, err := s.pipeline.RunTurnStream(ctx, sessionID, req.Text, func(delta string) {
			deltas <- delta
		})
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- result
	}()

	c.Stream(func(w io.Writer) bool {
		delta, ok := <-deltas
		if !ok {
			return false
		}
		c.SSEvent("delta", delta)
		return true
	})

	select {
	case err := <-errCh:
		c.SSEvent("error", err.Error())
	case result := <-resultCh:
		c.SSEvent("done", sendMessageResponse{Message: result.Message, Metadata: result.Metadata})
	}
}

// regenerate godoc
//
//	@Summary		Regenerate the last reply
//	@Description	Re-answers the nearest ancestor user message on the session's active path, creating a sibling assistant branch rather than a new user message.
//	@Tags			sessions
//	@Produce		json
//	@Param			id	path		int	true	"Session ID"
//	@Success		200	{object}	sendMessageResponse
//	@Failure		400	{object}	errorResponse
//	@Failure		500	{object}	errorResponse
//	@Router			/sessions/{id}/regenerate [post]
func (s *Server) regenerate(c *gin.Context) {
	sessionID, err := parseSessionID(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	ctx, cancel := context.WithCancel(c.Request.Context())
	s.commands.RegisterCancel(sessionID, cancel)
	defer s.commands.UnregisterCancel(sessionID)

	result, err := s.pipeline.Regenerate(ctx, sessionID)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			c.JSON(http.StatusRequestTimeout, errorResponse{Error: "stopped"})
			return
		}
		c.JSON(http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}

	c.JSON(http.StatusOK, sendMessageResponse{Message: result.Message, Metadata: result.Metadata})
}

// switchActiveLeaf validates that leafID belongs to sessionID before
// pointing the session's active branch at it.
func (s *Server) switchActiveLeaf(sessionID, leafID uint) error {
	var count int64
	if err := s.db.Model(&store.ChatMessage{}).
		Where("id = ? AND session_id = ?", leafID, sessionID).
		Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return errValidation("active_leaf_message_id does not belong to this session")
	}
	return s.db.Model(&store.Session{}).Where("id = ?", sessionID).
		Update("active_leaf_message_id", leafID).Error
}

type errorResponse struct {
	Error string `json:"error"`
}

type errValidation string

func (e errValidation) Error() string { return string(e) }

func parseSessionID(c *gin.Context) (uint, error) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		return 0, errValidation("invalid session id")
	}
	return uint(id), nil
}

// listSessions godoc
//
//	@Summary		List all sessions
//	@Description	Returns every session ordered by updated_at descending.
//	@Tags			sessions
//	@Produce		json
//	@Success		200	{array}		store.Session
//	@Failure		500	{object}	errorResponse
//	@Router			/sessions [get]
func (s *Server) listSessions(c *gin.Context) {
	var sessions []store.Session
	if err := s.db.Order("updated_at desc").Find(&sessions).Error; err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}
	c.JSON(http.StatusOK, sessions)
}

// conciergeItem is the JSON shape returned by listConcierges.
type conciergeItem struct {
	Name        string   `json:"name"`
	Identity    string   `json:"identity"`
	Impressions []string `json:"impressions"`
	ToolGroups  []string `json:"tool_groups"`
	Plugins     []string `json:"plugins"`
}

// listConcierges godoc
//
//	@Summary		List registered concierges
//	@Description	Returns the names and static settings of every loaded Concierge, sorted alphabetically.
//	@Tags			concierges
//	@Produce		json
//	@Success		200	{array}	conciergeItem
//	@Router			/concierges [get]
func (s *Server) listConcierges(c *gin.Context) {
	items := make([]conciergeItem, 0, len(s.reg.Concierges))
	for _, cg := range s.reg.Concierges {
		items = append(items, conciergeItem{
			Name:        cg.Name,
			Identity:    cg.Identity,
			Impressions: nullSafe(cg.Impressions),
			ToolGroups:  nullSafe(cg.ToolGroups),
			Plugins:     nullSafe(cg.Plugins),
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	c.JSON(http.StatusOK, items)
}

func nullSafe(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
