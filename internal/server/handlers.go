package server

import (
	"context"
	"errors"
	"fmt"
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
	Concierge  string   `json:"concierge" binding:"required"`
	Project    string   `json:"project"`
	ToolGroups []string `json:"tool_groups"`
	Plugins    []string `json:"plugins"`
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
	toolGroups, err := selectedConciergeCapabilities(req.ToolGroups, concierge.ToolGroups, "tool group")
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	plugins, err := selectedConciergeCapabilities(req.Plugins, concierge.Plugins, "plugin")
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	settings := session.SettingsFromConcierge(concierge)
	settings.ToolGroups = toolGroups
	settings.Plugins = plugins
	sess, err := s.sessions.CreateFromConciergeWithSettings(concierge, boundProject.ID, reasoningEffort, settings)
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusCreated, sess)
}

func selectedConciergeCapabilities(selected, allowed []string, kind string) ([]string, error) {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, value := range allowed {
		allowedSet[value] = struct{}{}
	}
	values := make([]string, 0, len(selected))
	seen := make(map[string]struct{}, len(selected))
	for _, value := range selected {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := allowedSet[value]; !ok {
			return nil, fmt.Errorf("%s is not available from the selected concierge: %s", kind, value)
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		values = append(values, value)
	}
	return values, nil
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
	AutoApprove     bool                `json:"auto_approve"`
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
	c.JSON(http.StatusOK, historyResponse{
		Session:         *sess,
		Messages:        messages,
		ReasoningEffort: identity.ReasoningEffort,
		AutoApprove:     s.commands.AutoApprove(sessionID),
	})
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
	// ActiveLeafMessageID selects the branch whose context the new turn
	// continues from. SelectRoot instead starts the turn from an empty path.
	// Neither selector changes session state until the turn is persisted.
	ActiveLeafMessageID *uint    `json:"active_leaf_message_id"`
	SelectRoot          bool     `json:"select_root"`
	Text                string   `json:"text"`
	ReasoningEffort     string   `json:"reasoning_effort"`
	DisabledTools       []string `json:"disabled_tools"`
}

func (r sendMessageRequest) turnOptions(onDelta func(chat.StreamEvent)) chat.TurnOptions {
	return chat.TurnOptions{
		SelectedLeaf:    r.ActiveLeafMessageID,
		SelectRoot:      r.SelectRoot,
		OnDelta:         onDelta,
		ReasoningEffort: r.ReasoningEffort,
		DisabledTools:   r.DisabledTools,
	}
}

// validateGenerationOptions rejects unsupported per-turn generation options.
func validateGenerationOptions(req *sendMessageRequest) error {
	switch req.ReasoningEffort {
	case "", registry.ReasoningNone, registry.ReasoningLow, registry.ReasoningHigh, registry.ReasoningMax:
	default:
		return errValidation("reasoning_effort must be none, low, high, or max")
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

func validateBranchSelection(req sendMessageRequest) error {
	if req.SelectRoot && req.ActiveLeafMessageID != nil {
		return errValidation("select_root and active_leaf_message_id cannot both be set")
	}
	if req.ActiveLeafMessageID != nil && *req.ActiveLeafMessageID == 0 {
		return errValidation("invalid active leaf message id")
	}
	return nil
}

type sendMessageResponse struct {
	// CommandResponse is set (and never persisted) when Text was a slash command.
	CommandResponse string `json:"command_response,omitempty"`
	// ReplayedMessages are transient history copies produced by /last and /replay.
	ReplayedMessages []command.ReplayedMessage `json:"replayed_messages,omitempty"`
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
		if errors.Is(ctx.Err(), context.Canceled) {
			c.JSON(http.StatusRequestTimeout, errorResponse{Error: "stopped"})
			return
		}
		writeTurnError(c, err)
		return
	}
	commitUpload(uploadResult)

	c.JSON(http.StatusOK, sendMessageResponse{
		Message:            result.Message,
		Metadata:           mergeMetadata(result.Metadata, uploadResult),
		BranchNotActivated: result.Metadata["stale_active_leaf"] == true,
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
	if err := validateBranchSelection(req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return 0, sendMessageRequest{}, nil, false
	}
	if strings.TrimSpace(req.Text) == "" {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "text is required"})
		return 0, sendMessageRequest{}, nil, false
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
			c.JSON(http.StatusOK, sendMessageResponse{CommandResponse: result.Response, SessionTarget: result.SessionTarget, ReplayedMessages: result.ReplayedMessages})
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
		if err != nil || parsed == 0 {
			return nil, errValidation("invalid active leaf message id")
		}
		value := uint(parsed)
		req.ActiveLeafMessageID = &value
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
	if err := validateBranchSelection(req); err != nil {
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
		writeTurnError(c, err)
		return
	}

	c.JSON(http.StatusOK, sendMessageResponse{Message: result.Message, Metadata: result.Metadata})
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

func writeTurnError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, session.ErrStaleActiveLeaf):
		writeStaleLeaf(c)
	case errors.Is(err, session.ErrInvalidParent):
		c.JSON(http.StatusBadRequest, errorResponse{Error: "active leaf message does not belong to session"})
	case errors.Is(err, gorm.ErrRecordNotFound):
		c.JSON(http.StatusNotFound, errorResponse{Error: "session not found"})
	default:
		internalError(c, err)
	}
}

func turnErrorMessage(err error) string {
	switch {
	case errors.Is(err, session.ErrStaleActiveLeaf):
		return staleLeafMessage
	case errors.Is(err, session.ErrInvalidParent):
		return "active leaf message does not belong to session"
	case errors.Is(err, gorm.ErrRecordNotFound):
		return "session not found"
	default:
		return "internal server error"
	}
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
//	@Description	Returns every session ordered by last_message_time descending, including direct background subagent summaries.
//	@Tags			sessions
//	@Produce		json
//	@Success		200	{array}		sessionListResponse
//	@Failure		500	{object}	errorResponse
//	@Router			/sessions [get]
type sessionListResponse struct {
	store.Session
	SubagentRuns []subagentRunSummary `json:"subagent_runs"`
}

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
	responses, err := s.sessionListResponses(sessions)
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, responses)
}

func (s *Server) sessionListResponses(sessions []store.Session) ([]sessionListResponse, error) {
	responses := make([]sessionListResponse, len(sessions))
	ids := make([]uint, len(sessions))
	for index := range sessions {
		responses[index] = sessionListResponse{Session: sessions[index], SubagentRuns: []subagentRunSummary{}}
		ids[index] = sessions[index].ID
	}
	if len(ids) == 0 {
		return responses, nil
	}
	runs, err := s.subagents.ListBackgroundByParentSessions(ids)
	if err != nil {
		return nil, err
	}
	bySession := make(map[uint][]subagentRunSummary)
	for _, run := range runs {
		bySession[run.ParentSessionID] = append(bySession[run.ParentSessionID], publicSubagentRunSummary(run))
	}
	for index := range responses {
		if summaries := bySession[responses[index].ID]; summaries != nil {
			responses[index].SubagentRuns = summaries
		}
	}
	return responses, nil
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
	if errors.Is(err, session.ErrSessionBusy) {
		c.JSON(http.StatusConflict, errorResponse{Error: "cannot delete a session while background work is active"})
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
	Name              string   `json:"name"`
	Nickname          string   `json:"nickname"`
	Description       string   `json:"description"`
	Identity          string   `json:"identity"`
	ReasoningEffort   string   `json:"reasoning_effort"`
	Impressions       []string `json:"impressions"`
	ToolGroups        []string `json:"tool_groups"`
	DefaultToolGroups []string `json:"default_tool_groups"`
	Plugins           []string `json:"plugins"`
	DefaultPlugins    []string `json:"default_plugins"`
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
			Name:              cg.Name,
			Nickname:          cg.Nickname,
			Description:       cg.Description,
			Identity:          cg.Identity,
			ReasoningEffort:   identity.ReasoningEffort,
			Impressions:       nullSafe(cg.Impressions),
			ToolGroups:        nullSafe(cg.ToolGroups),
			DefaultToolGroups: nullSafe(cg.DefaultToolGroups),
			Plugins:           nullSafe(cg.Plugins),
			DefaultPlugins:    nullSafe(cg.DefaultPlugins),
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
