package server

import (
	"context"
	"errors"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/Cyvadra/hephaestus/internal/chat"
	"github.com/Cyvadra/hephaestus/internal/command"
	"github.com/Cyvadra/hephaestus/internal/project"
	"github.com/Cyvadra/hephaestus/internal/registry"
	"github.com/Cyvadra/hephaestus/internal/session"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/Cyvadra/hephaestus/internal/tools"
	"github.com/Cyvadra/hephaestus/internal/upload"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type createSessionRequest struct {
	Concierge string `json:"concierge" binding:"required"`
	Project   string `json:"project"`
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
	reg := s.registries.Current()
	var req createSessionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	concierge, ok := reg.Concierges[req.Concierge]
	if !ok {
		c.JSON(http.StatusNotFound, errorResponse{Error: "concierge not found: " + req.Concierge})
		return
	}

	projectName := strings.TrimSpace(req.Project)
	if projectName == "" {
		projectName = project.DefaultName
	}
	boundProject, err := s.projects.GetByName(projectName)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, errorResponse{Error: "project not found: " + projectName})
			return
		}
		internalError(c, err)
		return
	}
	if !s.projects.IsConciergeAvailable(*boundProject, concierge.Name) {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "concierge is not available for project: " + projectName})
		return
	}

	reasoningEffort := ""
	if identity, ok := reg.Identities[concierge.Identity]; ok {
		reasoningEffort = identity.ReasoningEffort
	}
	sess, err := s.sessions.CreateFromConcierge(concierge, boundProject.ID, reasoningEffort)
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusCreated, sess)
}

// forkSessionAtMessage godoc
//
//	@Summary		Fork a session from an assistant message
//	@Description	Creates a new session from the source session's conversation path ending at the selected assistant message.
//	@Tags			sessions
//	@Produce		json
//	@Param			id			path	int	true	"Session ID"
//	@Param			messageID	path	int	true	"Assistant message ID"
//	@Success		201		{object}	store.Session
//	@Failure		400		{object}	errorResponse
//	@Failure		404		{object}	errorResponse
//	@Router			/sessions/{id}/messages/{messageID}/fork [post]
func (s *Server) forkSessionAtMessage(c *gin.Context) {
	sessionID, err := parseSessionID(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	messageID, err := strconv.ParseUint(c.Param("messageID"), 10, 64)
	if err != nil || messageID == 0 {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "message ID must be a positive integer"})
		return
	}

	fork, err := s.sessions.ForkAt(sessionID, uint(messageID))
	if err != nil {
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound), errors.Is(err, session.ErrMessageNotFound):
			c.JSON(http.StatusNotFound, errorResponse{Error: "session or assistant message not found"})
		case errors.Is(err, session.ErrNotAssistant):
			c.JSON(http.StatusBadRequest, errorResponse{Error: "message must be an assistant message"})
		default:
			internalError(c, err)
		}
		return
	}
	c.JSON(http.StatusCreated, fork)
}

type historyResponse struct {
	Session         store.Session       `json:"session"`
	Messages        []store.ChatMessage `json:"messages"`
	ReasoningEffort string              `json:"reasoning_effort"`
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
	reg := s.registries.Current()
	sessionID, err := parseSessionID(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	sess, err := s.sessions.Get(sessionID)
	if err != nil {
		c.JSON(http.StatusNotFound, errorResponse{Error: "session not found"})
		return
	}

	messages, err := s.sessions.Messages(sessionID)
	if err != nil {
		internalError(c, err)
		return
	}

	settings := sess.Settings.Data()
	identity, ok := reg.Identities[settings.Identity]
	if !ok {
		// The session references an identity that no longer exists; fall
		// back to the registry default so history stays viewable.
		identity, ok = reg.Identities[reg.DefaultIdentityName()]
		if !ok {
			c.JSON(http.StatusInternalServerError, errorResponse{Error: "session identity not found"})
			return
		}
	}
	c.JSON(http.StatusOK, historyResponse{Session: *sess, Messages: messages, ReasoningEffort: identity.ReasoningEffort})
}

// downloadAttachment godoc
//
//	@Summary		Download an assistant attachment
//	@Description	Downloads a file explicitly delivered by an assistant message. The referenced Project file is revalidated at download time.
//	@Tags			sessions
//	@Produce		application/octet-stream
//	@Param			id			path	int	true	"Session ID"
//	@Param			attachmentID	path	int	true	"Attachment ID"
//	@Success		200	{file}	binary
//	@Failure		404	{object}	errorResponse
//	@Failure		410	{object}	errorResponse
//	@Router			/sessions/{id}/attachments/{attachmentID}/download [get]
func (s *Server) downloadAttachment(c *gin.Context) {
	sessionID, err := parseSessionID(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	attachmentID, err := parseUintParam(c, "attachmentID", "attachment id")
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	sess, attachment, err := s.sessions.Attachment(sessionID, attachmentID)
	if err != nil {
		c.JSON(http.StatusNotFound, errorResponse{Error: "session not found"})
		return
	}
	boundProject, err := s.projects.Get(sess.ProjectID)
	if err != nil {
		internalError(c, err)
		return
	}
	path, delivery, err := tools.ResolveProjectFile(s.projects.Path(*boundProject), attachment.Path)
	if err != nil {
		if errors.Is(err, tools.ErrDeliveryFileNotFound) {
			c.JSON(http.StatusGone, errorResponse{Error: "attachment source file is no longer available"})
			return
		}
		c.JSON(http.StatusNotFound, errorResponse{Error: "attachment is no longer available"})
		return
	}
	c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": attachment.Name}))
	c.Header("Content-Type", delivery.MIME)
	c.File(path)
}

type sendMessageRequest struct {
	// ActiveLeafMessageID, when set, switches the session onto this
	// branch before the message is processed (see design doc's session
	// branching semantics). Required for every continuation, per doc.
	ActiveLeafMessageID *uint    `json:"active_leaf_message_id"`
	SelectRoot          bool     `json:"select_root"`
	Text                string   `json:"text"`
	ReasoningEffort     string   `json:"reasoning_effort"`
	DisabledTools       []string `json:"disabled_tools"`
}

func (r sendMessageRequest) turnOptions(onDelta func(chat.StreamEvent)) chat.TurnOptions {
	return chat.TurnOptions{
		ExpectedLeaf:    r.ActiveLeafMessageID,
		OnDelta:         onDelta,
		ReasoningEffort: r.ReasoningEffort,
		DisabledTools:   r.DisabledTools,
	}
}

func (r *sendMessageRequest) normalizeActiveLeaf() {
	if r.SelectRoot {
		r.ActiveLeafMessageID = nil
	}
	if r.ActiveLeafMessageID != nil && *r.ActiveLeafMessageID == 0 {
		r.ActiveLeafMessageID = nil
	}
}

// validateGenerationOptions rejects per-turn reasoning_effort values the
// composer UI does not expose. "low" is deliberately absent (identities may
// still declare it) so the per-turn override stays aligned with the UI.
func validateGenerationOptions(req *sendMessageRequest) error {
	switch req.ReasoningEffort {
	case "", registry.ReasoningNone, registry.ReasoningHigh, registry.ReasoningMax:
	default:
		return errValidation("reasoning_effort must be none, high, or max")
	}
	seen := make(map[string]struct{}, len(req.DisabledTools))
	tools := req.DisabledTools[:0]
	for _, name := range req.DisabledTools {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		tools = append(tools, name)
	}
	req.DisabledTools = tools
	return nil
}

type sendMessageResponse struct {
	// CommandResponse is set (and never persisted) when Text was a slash command.
	CommandResponse string `json:"command_response,omitempty"`
	// SessionTarget asks the client to navigate to another existing session.
	// It is set only by slash commands and does not represent a session write.
	SessionTarget *command.SessionTarget `json:"session_target,omitempty"`
	// Message is the persisted final assistant ChatMessage for a chat turn.
	Message *store.ChatMessage `json:"message,omitempty"`
	// Metadata carries any plugin-attached data for this turn (e.g.
	// suggested next-user-message alternatives).
	Metadata map[string]any `json:"metadata,omitempty"`
	// BranchNotActivated is set when the turn's output was persisted as a
	// reachable but inactive branch because the session's active leaf
	// changed mid-turn.
	BranchNotActivated bool `json:"branch_not_activated,omitempty"`
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
			writeStaleLeaf(c)
		case errors.Is(err, session.ErrMessageNotFound), errors.Is(err, gorm.ErrRecordNotFound):
			c.JSON(http.StatusNotFound, errorResponse{Error: "session or message not found"})
		case errors.Is(err, session.ErrNotAssistant),
			errors.Is(err, session.ErrToolCallMessage),
			errors.Is(err, session.ErrMessageNotOnPath),
			errors.Is(err, session.ErrEmptyContent):
			c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		default:
			internalError(c, err)
		}
		return
	}

	c.JSON(http.StatusOK, sendMessageResponse{Message: edited})
}

// sendMessage godoc
//
//	@Summary		Send a message
//	@Description	Sends a JSON user message or multipart form with text and repeated files into a session. Optional reasoning_effort overrides the identity for this turn; disabled_tools removes named tools from this turn only.
//	@Tags			sessions
//	@Accept			json
//	@Accept			mpfd
//	@Produce		json
//	@Param			id		path		int					true	"Session ID"
//	@Param			request	body		sendMessageRequest	true	"Message to send"
//	@Success		200		{object}	sendMessageResponse
//	@Failure		400		{object}	errorResponse
//	@Failure		500		{object}	errorResponse
//	@Router			/sessions/{id}/messages [post]
func (s *Server) sendMessage(c *gin.Context) {
	sessionID, req, uploadResult, ok := s.prepareMessage(c)
	if !ok {
		return
	}

	ctx, cancel := context.WithCancel(c.Request.Context())
	registrationID := s.commands.RegisterCancel(sessionID, cancel)
	defer s.commands.UnregisterCancel(sessionID, registrationID)

	result, err := s.pipeline.Run(ctx, sessionID, req.Text, req.turnOptions(nil))
	if err != nil && result == nil {
		rollbackUpload(uploadResult)
		if errors.Is(err, session.ErrStaleActiveLeaf) {
			writeStaleLeaf(c)
			return
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			c.JSON(http.StatusRequestTimeout, errorResponse{Error: "stopped"})
			return
		}
		internalError(c, err)
		return
	}
	commitUpload(uploadResult)

	c.JSON(http.StatusOK, sendMessageResponse{
		Message:            result.Message,
		Metadata:           mergeMetadata(result.Metadata, uploadResult),
		BranchNotActivated: result.Metadata["stale_active_leaf"] == true,
	})
}

// streamMessage godoc
//
//	@Summary		Send a message with streaming
//	@Description	Like sendMessage, including per-turn reasoning_effort and disabled_tools overrides, but streams typed assistant progress as Server-Sent Events ("delta", "reasoning", "tool_call", "tool_output", "tool_result", and "session_updated" events), finishing with a "done" event carrying the same body sendMessage would return (or an "error" event).
//	@Tags			sessions
//	@Accept			json
//	@Accept			mpfd
//	@Produce		text/event-stream
//	@Param			id		path	int					true	"Session ID"
//	@Param			request	body	sendMessageRequest	true	"Message to send"
//	@Success		200
//	@Failure		400	{object}	errorResponse
//	@Router			/sessions/{id}/messages/stream [post]
func (s *Server) streamMessage(c *gin.Context) {
	sessionID, req, uploadResult, ok := s.prepareMessage(c)
	if !ok {
		return
	}

	s.streamTurn(c, sessionID, func(ctx context.Context, onDelta func(chat.StreamEvent)) (*chat.TurnResult, error) {
		result, err := s.pipeline.Run(ctx, sessionID, req.Text, req.turnOptions(onDelta))
		if result == nil {
			rollbackUpload(uploadResult)
			return nil, err
		}
		commitUpload(uploadResult)
		result.Metadata = mergeMetadata(result.Metadata, uploadResult)
		return result, err
	})
}

func rollbackUpload(result *upload.Result) {
	if result != nil {
		_ = result.Rollback()
	}
}

func commitUpload(result *upload.Result) {
	if result != nil {
		result.Commit()
	}
}

// prepareMessage performs the shared request parsing, active-branch switch,
// and slash-command dispatch for streaming and non-streaming endpoints.
func (s *Server) prepareMessage(c *gin.Context) (uint, sendMessageRequest, *upload.Result, bool) {
	sessionID, err := parseSessionID(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return 0, sendMessageRequest{}, nil, false
	}
	var req sendMessageRequest
	files, err := s.bindMessageRequest(c, &req)
	if err != nil {
		status := http.StatusBadRequest
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			status = http.StatusRequestEntityTooLarge
		}
		c.JSON(status, errorResponse{Error: err.Error()})
		return 0, sendMessageRequest{}, nil, false
	}
	if err := validateGenerationOptions(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return 0, sendMessageRequest{}, nil, false
	}
	req.normalizeActiveLeaf()
	if strings.TrimSpace(req.Text) == "" {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "text is required"})
		return 0, sendMessageRequest{}, nil, false
	}
	if req.SelectRoot {
		if err := s.sessions.SelectRoot(sessionID); err != nil {
			internalError(c, err)
			return 0, sendMessageRequest{}, nil, false
		}
	}
	if req.ActiveLeafMessageID != nil {
		if err := s.sessions.SelectActiveLeaf(sessionID, *req.ActiveLeafMessageID); err != nil {
			c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
			return 0, sendMessageRequest{}, nil, false
		}
	}
	if command.IsCommand(req.Text) {
		if len(files) > 0 {
			c.JSON(http.StatusBadRequest, errorResponse{Error: "attachments cannot be sent with slash commands"})
			return 0, sendMessageRequest{}, nil, false
		}
		result, err := s.commands.ExecuteResult(sessionID, req.Text)
		if err != nil {
			c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		} else {
			c.JSON(http.StatusOK, sendMessageResponse{CommandResponse: result.Response, SessionTarget: result.SessionTarget})
		}
		return 0, sendMessageRequest{}, nil, false
	}
	if len(files) == 0 {
		return sessionID, req, nil, true
	}
	if s.uploads == nil {
		c.JSON(http.StatusServiceUnavailable, errorResponse{Error: "file uploads are not configured"})
		return 0, sendMessageRequest{}, nil, false
	}
	sess, err := s.sessions.Get(sessionID)
	if err != nil {
		c.JSON(http.StatusNotFound, errorResponse{Error: "session not found"})
		return 0, sendMessageRequest{}, nil, false
	}
	boundProject, err := s.projects.Get(sess.ProjectID)
	if err != nil {
		internalError(c, err)
		return 0, sendMessageRequest{}, nil, false
	}
	result, err := s.uploads.Process(c.Request.Context(), s.projects.Path(*boundProject), files)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, upload.ErrFileTooLarge) || errors.Is(err, upload.ErrTotalTooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		c.JSON(status, errorResponse{Error: err.Error()})
		return 0, sendMessageRequest{}, nil, false
	}
	req.Text = result.Prefix + req.Text
	return sessionID, req, &result, true
}

func (s *Server) bindMessageRequest(c *gin.Context, req *sendMessageRequest) ([]*multipart.FileHeader, error) {
	if !strings.HasPrefix(c.GetHeader("Content-Type"), "multipart/form-data") {
		return nil, c.ShouldBindJSON(req)
	}
	if s.uploads != nil {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, s.uploads.MaxRequestBytes())
	}
	if err := c.Request.ParseMultipartForm(32 << 20); err != nil {
		return nil, err
	}
	req.Text = c.PostForm("text")
	if leaf := c.PostForm("active_leaf_message_id"); leaf != "" {
		parsed, err := strconv.ParseUint(leaf, 10, 64)
		if err != nil {
			return nil, errValidation("invalid active leaf message id")
		}
		if parsed != 0 {
			value := uint(parsed)
			req.ActiveLeafMessageID = &value
		}
	}
	req.ReasoningEffort = c.PostForm("reasoning_effort")
	req.SelectRoot = c.PostForm("select_root") == "true"
	req.DisabledTools = c.PostFormArray("disabled_tools")
	return c.Request.MultipartForm.File["files"], nil
}

func mergeMetadata(metadata map[string]any, result *upload.Result) map[string]any {
	if result == nil {
		return metadata
	}
	merged := make(map[string]any, len(metadata)+1)
	for key, value := range metadata {
		merged[key] = value
	}
	merged["uploads"] = result
	return merged
}

// regenerate godoc
//
//	@Summary		Regenerate the last reply
//	@Description	Re-answers the nearest ancestor user message on the session's active path, creating a sibling assistant branch rather than a new user message.
//	@Tags			sessions
//	@Accept			json
//	@Produce		json
//	@Param			id	path		int	true	"Session ID"
//	@Param			request	body		sendMessageRequest	false	"Per-turn generation overrides"
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

	var req sendMessageRequest
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
			return
		}
	}
	if err := validateGenerationOptions(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	ctx, cancel := context.WithCancel(c.Request.Context())
	registrationID := s.commands.RegisterCancel(sessionID, cancel)
	defer s.commands.UnregisterCancel(sessionID, registrationID)

	result, err := s.pipeline.Regenerate(ctx, sessionID, req.turnOptions(nil))
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			c.JSON(http.StatusRequestTimeout, errorResponse{Error: "stopped"})
			return
		}
		internalError(c, err)
		return
	}

	c.JSON(http.StatusOK, sendMessageResponse{Message: result.Message, Metadata: result.Metadata})
}

// streamRegenerate godoc
//
//	@Summary		Regenerate the last reply with streaming
//	@Description	Like regenerate, but streams typed assistant progress as Server-Sent Events, finishing with a "done" event.
//	@Tags			sessions
//	@Accept			json
//	@Produce		text/event-stream
//	@Param			id	path	int	true	"Session ID"
//	@Param			request	body	sendMessageRequest	false	"Per-turn generation overrides"
//	@Success		200
//	@Failure		400	{object}	errorResponse
//	@Router			/sessions/{id}/regenerate/stream [post]
func (s *Server) streamRegenerate(c *gin.Context) {
	sessionID, err := parseSessionID(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	var req sendMessageRequest
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
			return
		}
	}
	if err := validateGenerationOptions(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	s.streamTurn(c, sessionID, func(ctx context.Context, onDelta func(chat.StreamEvent)) (*chat.TurnResult, error) {
		return s.pipeline.Regenerate(ctx, sessionID, req.turnOptions(onDelta))
	})
}

// streamContinue godoc
//
//	@Summary		Resume an incomplete assistant reply with streaming
//	@Description	Resumes generation at messageID, an incomplete assistant message on the session's active path, using its persisted content as the model's prefix. Streams only the newly generated suffix as Server-Sent Events, finishing with a "done" event carrying the same body sendMessage would return (or an "error" event).
//	@Tags			sessions
//	@Produce		text/event-stream
//	@Param			id			path	int	true	"Session ID"
//	@Param			messageID	path	int	true	"Incomplete assistant message ID"
//	@Success		200
//	@Failure		400	{object}	errorResponse
//	@Router			/sessions/{id}/messages/{messageID}/continue/stream [post]
func (s *Server) streamContinue(c *gin.Context) {
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

	s.streamTurn(c, sessionID, func(ctx context.Context, onDelta func(chat.StreamEvent)) (*chat.TurnResult, error) {
		return s.pipeline.Continue(ctx, sessionID, messageID, chat.TurnOptions{OnDelta: onDelta})
	})
}

// streamTurn runs a turn-producing closure and streams its progress events
// as Server-Sent Events, finishing with a "done" event carrying the same
// body sendMessage would return (or an "error" event). It owns the cancel
// registration, the delta fan-out, and the SSE sequence numbering shared by
// every streaming endpoint.
func (s *Server) streamTurn(c *gin.Context, sessionID uint, run func(ctx context.Context, onDelta func(chat.StreamEvent)) (*chat.TurnResult, error)) {
	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()
	registrationID := s.commands.RegisterCancel(sessionID, cancel)
	defer s.commands.UnregisterCancel(sessionID, registrationID)

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache, no-transform")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	c.Writer.Flush()

	deltas := make(chan chat.StreamEvent, 16)
	type turnOutcome struct {
		result *chat.TurnResult
		err    error
	}
	outcomes := make(chan turnOutcome, 1)

	go func() {
		defer close(deltas)
		result, err := run(ctx, func(delta chat.StreamEvent) {
			select {
			case deltas <- delta:
			case <-ctx.Done():
			}
		})
		outcomes <- turnOutcome{result: result, err: err}
	}()

	sequence := uint64(0)
	streamEvent := func(event string, data any) {
		sequence++
		c.SSEvent(event, streamEventEnvelope{Sequence: sequence, Data: data})
		c.Writer.Flush()
	}
	for delta := range deltas {
		if delta.Interaction != nil {
			streamEvent(delta.Type, delta.Interaction)
		} else if delta.Session != nil {
			streamEvent(delta.Type, delta.Session)
		} else if delta.ToolCall != nil {
			streamEvent(delta.Type, delta.ToolCall)
		} else {
			streamEvent(delta.Type, delta.Text)
		}
	}

	outcome := <-outcomes
	if outcome.err != nil && outcome.result == nil {
		if errors.Is(outcome.err, session.ErrStaleActiveLeaf) {
			streamEvent("error", staleLeafMessage)
		} else {
			streamEvent("error", outcome.err.Error())
		}
		return
	}
	streamEvent("done", sendMessageResponse{
		Message:            outcome.result.Message,
		Metadata:           outcome.result.Metadata,
		BranchNotActivated: outcome.result.Metadata["stale_active_leaf"] == true,
	})
}

type errorResponse struct {
	Error string `json:"error"`
}

// staleLeafMessage is the stable client-visible message for a session whose
// active branch changed mid-request.
const staleLeafMessage = "session changed; refresh and retry"

func writeStaleLeaf(c *gin.Context) {
	c.JSON(http.StatusConflict, errorResponse{Error: staleLeafMessage})
}

func internalError(c *gin.Context, err error) {
	log.Printf("server: %s %s: %v", c.Request.Method, c.Request.URL.Path, err)
	c.JSON(http.StatusInternalServerError, errorResponse{Error: "internal server error"})
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
	projectName := strings.TrimSpace(c.Query("project"))
	if projectName == "" {
		projectName = project.DefaultName
	}
	boundProject, err := s.projects.GetByName(projectName)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, errorResponse{Error: "project not found: " + projectName})
			return
		}
		internalError(c, err)
		return
	}
	sessions, err := s.sessions.ListByProject(boundProject.ID)
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, sessions)
}

type projectResponse struct {
	store.Project
	IsDefault bool `json:"is_default"`
}

type createProjectRequest struct {
	Name        string `json:"name" binding:"required"`
	Description string `json:"description"`
}

type deleteProjectRequest struct {
	DeleteDirectory bool `json:"delete_directory"`
}

func (s *Server) listProjects(c *gin.Context) {
	projects, err := s.projects.List()
	if err != nil {
		internalError(c, err)
		return
	}
	response := make([]projectResponse, 0, len(projects))
	for _, item := range projects {
		response = append(response, projectResponse{Project: item, IsDefault: item.Name == project.DefaultName})
	}
	c.JSON(http.StatusOK, response)
}

func (s *Server) createProject(c *gin.Context) {
	var req createProjectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	created, err := s.projects.Create(strings.TrimSpace(req.Name), strings.TrimSpace(req.Description))
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	c.JSON(http.StatusCreated, projectResponse{Project: *created, IsDefault: created.Name == project.DefaultName})
}

// deleteProject godoc
//
//	@Summary		Delete an empty project
//	@Description	Deletes a non-default project only when it has no sessions. Its project directory is deleted only when requested.
//	@Tags			projects
//	@Accept			json
//	@Param			name	path	string	true	"Project name"
//	@Param			request	body	deleteProjectRequest	false	"Deletion options"
//	@Success		204
//	@Failure		400	{object}	errorResponse
//	@Failure		404	{object}	errorResponse
//	@Router			/projects/{name} [delete]
func (s *Server) deleteProject(c *gin.Context) {
	name := strings.TrimSpace(c.Param("name"))
	var req deleteProjectRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	if err := s.projects.Delete(name, req.DeleteDirectory); err != nil {
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			c.JSON(http.StatusNotFound, errorResponse{Error: "project not found: " + name})
		default:
			c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		}
		return
	}
	c.Status(http.StatusNoContent)
}

type updateSessionRequest struct {
	Title           *string `json:"title"`
	Archived        *bool   `json:"archived"`
	Pinned          *bool   `json:"pinned"`
	ReasoningEffort *string `json:"reasoning_effort"`
	EnableWebSearch *bool   `json:"enable_web_search"`
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
	if req.Title == nil && req.Archived == nil && req.Pinned == nil && req.ReasoningEffort == nil && req.EnableWebSearch == nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "no session changes provided"})
		return
	}
	sess, err := s.sessions.Update(sessionID, session.Patch{
		Title:           req.Title,
		Archived:        req.Archived,
		Pinned:          req.Pinned,
		ReasoningEffort: req.ReasoningEffort,
		EnableWebSearch: req.EnableWebSearch,
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, errorResponse{Error: "session not found"})
		return
	}
	if err != nil {
		var validation session.ValidationError
		if errors.As(err, &validation) {
			c.JSON(http.StatusBadRequest, errorResponse{Error: validation.Error()})
			return
		}
		internalError(c, err)
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

	err = s.sessions.Delete(sessionID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, errorResponse{Error: "session not found"})
		return
	}
	if err != nil {
		internalError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// conciergeItem is the JSON shape returned by listConcierges.
type conciergeItem struct {
	Name            string   `json:"name"`
	Nickname        string   `json:"nickname"`
	Description     string   `json:"description"`
	Identity        string   `json:"identity"`
	ReasoningEffort string   `json:"reasoning_effort"`
	Impressions     []string `json:"impressions"`
	ToolGroups      []string `json:"tool_groups"`
	Plugins         []string `json:"plugins"`
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
	reg := s.registries.Current()
	projectName := strings.TrimSpace(c.Query("project"))
	var boundProject *store.Project
	if projectName != "" {
		var err error
		boundProject, err = s.projects.GetByName(projectName)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusNotFound, errorResponse{Error: "project not found: " + projectName})
				return
			}
			internalError(c, err)
			return
		}
	}
	items := make([]conciergeItem, 0, len(reg.Concierges))
	for _, cg := range reg.Concierges {
		if boundProject != nil && !s.projects.IsConciergeAvailable(*boundProject, cg.Name) {
			continue
		}
		identity := reg.Identities[cg.Identity]
		items = append(items, conciergeItem{
			Name:            cg.Name,
			Nickname:        cg.Nickname,
			Description:     cg.Description,
			Identity:        cg.Identity,
			ReasoningEffort: identity.ReasoningEffort,
			Impressions:     nullSafe(cg.Impressions),
			ToolGroups:      nullSafe(cg.ToolGroups),
			Plugins:         nullSafe(cg.Plugins),
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
