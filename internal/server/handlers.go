package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/Cyvadra/hephaestus/internal/chat"
	"github.com/Cyvadra/hephaestus/internal/command"
	"github.com/Cyvadra/hephaestus/internal/session"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
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

	messages, err := s.sessions.Messages(sessionID)
	if err != nil {
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

// streamEventEnvelope makes every SSE payload self-describing and ordered.
// Sequence begins at one for each HTTP stream.
type streamEventEnvelope struct {
	Sequence uint64 `json:"sequence"`
	Data     any    `json:"data"`
}

type editAssistantMessageRequest struct {
	ActiveLeafMessageID uint   `json:"active_leaf_message_id" binding:"required"`
	Content             string `json:"content" binding:"required"`
	ReasoningContent    string `json:"reasoning_content"`
}

// editAssistantMessage godoc
//
//	@Summary		Edit an assistant message
//	@Description	Creates an edited sibling of an assistant message without invoking the LLM, then makes the new message the session's active leaf.
//	@Tags			sessions
//	@Accept			json
//	@Produce		json
//	@Param			id			path	int						true	"Session ID"
//	@Param			messageID	path	int						true	"Assistant message ID"
//	@Param			request		body	editAssistantMessageRequest	true	"Edited assistant content"
//	@Success		200			{object}	sendMessageResponse
//	@Failure		400			{object}	errorResponse
//	@Failure		404			{object}	errorResponse
//	@Failure		409			{object}	errorResponse
//	@Failure		500			{object}	errorResponse
//	@Router			/sessions/{id}/messages/{messageID}/edit [post]
func (s *Server) editAssistantMessage(c *gin.Context) {
	sessionID, err := parseSessionID(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	messageID, err := parseUintParam(c, "messageID", "message id")
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	var req editAssistantMessageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	edited, err := s.sessions.EditAssistantAtLeaf(
		sessionID,
		messageID,
		req.ActiveLeafMessageID,
		req.Content,
		req.ReasoningContent,
	)
	if err != nil {
		switch {
		case errors.Is(err, session.ErrStaleActiveLeaf):
			c.JSON(http.StatusConflict, errorResponse{Error: "session changed; refresh and retry"})
		case errors.Is(err, session.ErrMessageNotFound), errors.Is(err, gorm.ErrRecordNotFound):
			c.JSON(http.StatusNotFound, errorResponse{Error: "session or message not found"})
		case errors.Is(err, session.ErrNotAssistant),
			errors.Is(err, session.ErrToolCallMessage),
			errors.Is(err, session.ErrMessageNotOnPath),
			errors.Is(err, session.ErrEmptyContent):
			c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		default:
			c.JSON(http.StatusInternalServerError, errorResponse{Error: err.Error()})
		}
		return
	}

	c.JSON(http.StatusOK, sendMessageResponse{Message: edited})
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
	sessionID, req, ok := s.prepareMessage(c)
	if !ok {
		return
	}

	ctx, cancel := context.WithCancel(c.Request.Context())
	s.commands.RegisterCancel(sessionID, cancel)
	defer s.commands.UnregisterCancel(sessionID)

	result, err := s.pipeline.Run(ctx, sessionID, req.Text, chat.TurnOptions{ExpectedLeaf: req.ActiveLeafMessageID})
	if err != nil {
		if errors.Is(err, session.ErrStaleActiveLeaf) {
			c.JSON(http.StatusConflict, errorResponse{Error: "session changed; refresh and retry"})
			return
		}
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
//	@Description	Like sendMessage, but streams typed assistant progress as Server-Sent Events ("delta", "reasoning", "tool_call", "tool_result", and "session_updated" events), finishing with a "done" event carrying the same body sendMessage would return (or an "error" event).
//	@Tags			sessions
//	@Accept			json
//	@Produce		text/event-stream
//	@Param			id		path	int					true	"Session ID"
//	@Param			request	body	sendMessageRequest	true	"Message to send"
//	@Success		200
//	@Failure		400	{object}	errorResponse
//	@Router			/sessions/{id}/messages/stream [post]
func (s *Server) streamMessage(c *gin.Context) {
	sessionID, req, ok := s.prepareMessage(c)
	if !ok {
		return
	}

	s.streamTurn(c, sessionID, func(ctx context.Context, onDelta func(chat.StreamEvent)) (*chat.TurnResult, error) {
		return s.pipeline.Run(ctx, sessionID, req.Text, chat.TurnOptions{ExpectedLeaf: req.ActiveLeafMessageID, OnDelta: onDelta})
	})
}

// prepareMessage performs the shared request parsing, active-branch switch,
// and slash-command dispatch for streaming and non-streaming endpoints.
func (s *Server) prepareMessage(c *gin.Context) (uint, sendMessageRequest, bool) {
	sessionID, err := parseSessionID(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return 0, sendMessageRequest{}, false
	}
	var req sendMessageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return 0, sendMessageRequest{}, false
	}
	if req.ActiveLeafMessageID != nil {
		if err := s.sessions.SelectActiveLeaf(sessionID, *req.ActiveLeafMessageID); err != nil {
			c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
			return 0, sendMessageRequest{}, false
		}
	}
	if command.IsCommand(req.Text) {
		resp, err := s.commands.Execute(sessionID, req.Text)
		if err != nil {
			c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		} else {
			c.JSON(http.StatusOK, sendMessageResponse{CommandResponse: resp})
		}
		return 0, sendMessageRequest{}, false
	}
	return sessionID, req, true
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

	result, err := s.pipeline.Regenerate(ctx, sessionID, chat.TurnOptions{})
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

// streamRegenerate godoc
//
//	@Summary		Regenerate the last reply with streaming
//	@Description	Like regenerate, but streams typed assistant progress as Server-Sent Events, finishing with a "done" event.
//	@Tags			sessions
//	@Produce		text/event-stream
//	@Param			id	path	int	true	"Session ID"
//	@Success		200
//	@Failure		400	{object}	errorResponse
//	@Router			/sessions/{id}/regenerate/stream [post]
func (s *Server) streamRegenerate(c *gin.Context) {
	sessionID, err := parseSessionID(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	s.streamTurn(c, sessionID, func(ctx context.Context, onDelta func(chat.StreamEvent)) (*chat.TurnResult, error) {
		return s.pipeline.Regenerate(ctx, sessionID, chat.TurnOptions{OnDelta: onDelta})
	})
}

// streamTurn runs a turn-producing closure and streams its progress events
// as Server-Sent Events, finishing with a "done" event carrying the same
// body sendMessage would return (or an "error" event). It owns the cancel
// registration, the delta fan-out, and the SSE sequence numbering shared by
// every streaming endpoint.
func (s *Server) streamTurn(c *gin.Context, sessionID uint, run func(ctx context.Context, onDelta func(chat.StreamEvent)) (*chat.TurnResult, error)) {
	ctx, cancel := context.WithCancel(c.Request.Context())
	s.commands.RegisterCancel(sessionID, cancel)
	defer s.commands.UnregisterCancel(sessionID)

	deltas := make(chan chat.StreamEvent, 16)
	resultCh := make(chan *chat.TurnResult, 1)
	errCh := make(chan error, 1)

	go func() {
		defer close(deltas)
		result, err := run(ctx, func(delta chat.StreamEvent) {
			select {
			case deltas <- delta:
			case <-ctx.Done():
			}
		})
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- result
	}()

	sequence := uint64(0)
	streamEvent := func(event string, data any) {
		sequence++
		c.SSEvent(event, streamEventEnvelope{Sequence: sequence, Data: data})
	}
	c.Stream(func(w io.Writer) bool {
		delta, ok := <-deltas
		if !ok {
			return false
		}
		if delta.Session != nil {
			streamEvent(delta.Type, delta.Session)
		} else if delta.ToolCall != nil {
			streamEvent(delta.Type, delta.ToolCall)
		} else {
			streamEvent(delta.Type, delta.Text)
		}
		return true
	})

	select {
	case err := <-errCh:
		if errors.Is(err, session.ErrStaleActiveLeaf) {
			streamEvent("error", "session changed; refresh and retry")
		} else {
			streamEvent("error", err.Error())
		}
	case result := <-resultCh:
		streamEvent("done", sendMessageResponse{Message: result.Message, Metadata: result.Metadata})
	}
}

type errorResponse struct {
	Error string `json:"error"`
}

type errValidation string

func (e errValidation) Error() string { return string(e) }

func parseSessionID(c *gin.Context) (uint, error) {
	return parseUintParam(c, "id", "session id")
}

func parseUintParam(c *gin.Context, param, label string) (uint, error) {
	id, err := strconv.ParseUint(c.Param(param), 10, 64)
	if err != nil || id == 0 {
		return 0, errValidation("invalid " + label)
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

type updateSessionRequest struct {
	Title    *string `json:"title"`
	Archived *bool   `json:"archived"`
	Pinned   *bool   `json:"pinned"`
}

// updateSession godoc
//
//	@Summary		Update a session
//	@Tags			sessions
//	@Accept			json
//	@Produce		json
//	@Param			id		path	int					true	"Session ID"
//	@Param			request	body	updateSessionRequest	true	"Session changes"
//	@Success		200		{object}	store.Session
//	@Failure		400		{object}	errorResponse
//	@Failure		404		{object}	errorResponse
//	@Router			/sessions/{id} [patch]
func (s *Server) updateSession(c *gin.Context) {
	sessionID, err := parseSessionID(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	var req updateSessionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	if req.Title == nil && req.Archived == nil && req.Pinned == nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "no session changes provided"})
		return
	}

	changes := map[string]any{}
	if req.Title != nil {
		title := strings.TrimSpace(*req.Title)
		if title == "" {
			c.JSON(http.StatusBadRequest, errorResponse{Error: "session title cannot be empty"})
			return
		}
		if len([]rune(title)) > 64 {
			c.JSON(http.StatusBadRequest, errorResponse{Error: "session title must be 64 characters or fewer"})
			return
		}
		changes["title"] = title
	}
	if req.Archived != nil {
		changes["flag_archived"] = *req.Archived
	}
	if req.Pinned != nil {
		if *req.Pinned {
			changes["flag_pinned"] = uint8(1)
		} else {
			changes["flag_pinned"] = uint8(0)
		}
	}

	var sess store.Session
	if err := s.db.First(&sess, sessionID).Error; err != nil {
		c.JSON(http.StatusNotFound, errorResponse{Error: "session not found"})
		return
	}
	if err := s.db.Model(&sess).Updates(changes).Error; err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}
	if err := s.db.First(&sess, sessionID).Error; err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}
	c.JSON(http.StatusOK, sess)
}

// deleteSession godoc
//
//	@Summary		Delete a session and its conversation data
//	@Tags			sessions
//	@Param			id	path	int	true	"Session ID"
//	@Success		204
//	@Failure		404	{object}	errorResponse
//	@Router			/sessions/{id} [delete]
func (s *Server) deleteSession(c *gin.Context) {
	sessionID, err := parseSessionID(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	err = s.db.Transaction(func(tx *gorm.DB) error {
		var sess store.Session
		if err := tx.First(&sess, sessionID).Error; err != nil {
			return err
		}
		for _, model := range []any{&store.ChatMessage{}, &store.Compression{}, &store.PluginState{}} {
			if err := tx.Where("session_id = ?", sessionID).Delete(model).Error; err != nil {
				return err
			}
		}
		return tx.Delete(&sess).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, errorResponse{Error: "session not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
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
