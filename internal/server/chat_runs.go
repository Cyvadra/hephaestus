package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Cyvadra/hephaestus/internal/chat"
	"github.com/Cyvadra/hephaestus/internal/chatrun"
	"github.com/Cyvadra/hephaestus/internal/command"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/Cyvadra/hephaestus/internal/upload"
	"github.com/gin-gonic/gin"
)

type startChatRunRequest struct {
	Kind      store.ChatRunKind  `json:"kind"`
	Text      string             `json:"text"`
	MessageID *uint              `json:"message_id"`
	Options   sendMessageRequest `json:"options"`
}

type chatRunResponse struct {
	ID             uint                `json:"id"`
	SessionID      uint                `json:"session_id"`
	ProjectID      uint                `json:"project_id"`
	Kind           store.ChatRunKind   `json:"kind"`
	Status         store.ChatRunStatus `json:"status"`
	FinalMessageID *uint               `json:"final_message_id,omitempty"`
	Error          string              `json:"error,omitempty"`
	StartedAt      *time.Time          `json:"started_at,omitempty"`
	FinishedAt     *time.Time          `json:"finished_at,omitempty"`
}

type chatRunDone struct {
	Status   store.ChatRunStatus `json:"status"`
	Error    string              `json:"error,omitempty"`
	Response sendMessageResponse `json:"response"`
}

func newChatRunResponse(run *store.ChatRun) chatRunResponse {
	return chatRunResponse{
		ID: run.ID, SessionID: run.SessionID, ProjectID: run.ProjectID,
		Kind: run.Kind, Status: run.Status, FinalMessageID: run.FinalMessageID,
		Error: publicRunError(run), StartedAt: run.StartedAt, FinishedAt: run.FinishedAt,
	}
}

func publicRunError(run *store.ChatRun) string {
	switch run.Status {
	case store.ChatRunSucceeded:
		return ""
	case store.ChatRunCancelled:
		return "stopped"
	case store.ChatRunInterrupted:
		return "chat generation interrupted"
	default:
		return "internal server error"
	}
}

// startChatRun begins a durable chat turn and returns immediately. The run
// remains active after this request and any later SSE subscriptions close.
func (s *Server) startChatRun(c *gin.Context) {
	var req startChatRunRequest
	if strings.HasPrefix(c.GetHeader("Content-Type"), "multipart/form-data") {
		req.Kind = store.ChatRunMessage
	} else if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	} else if req.Kind == "" {
		req.Kind = store.ChatRunMessage
	}

	var (
		sessionID uint
		sess      *store.Session
		err       error
	)
	var execute chatrun.Execute
	var request map[string]any
	var uploadResult *upload.Result
	started := false
	defer func() {
		if !started {
			rollbackUpload(uploadResult)
		}
	}()
	switch req.Kind {
	case store.ChatRunMessage:
		if strings.HasPrefix(c.GetHeader("Content-Type"), "multipart/form-data") {
			sessionID, req.Options, uploadResult, execute, request = s.prepareMessageRun(c)
			if execute == nil {
				return
			}
		} else {
			sessionID, req.Options, _, execute, request = s.prepareMessageRunFromRequest(c, req.Text, req.Options)
			if execute == nil {
				return
			}
		}
	case store.ChatRunRegenerate:
		if err := validateGenerationOptions(&req.Options); err != nil {
			c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
			return
		}
		if err := validateBranchSelection(req.Options); err != nil {
			c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
			return
		}
		sessionID, err = parseSessionID(c)
		if err != nil {
			c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
			return
		}
		request = map[string]any{"message_id": req.MessageID, "options": req.Options}
		execute = func(ctx context.Context, onDelta func(chat.StreamEvent)) (*chatrun.Result, error) {
			result, err := s.pipeline.Regenerate(ctx, sessionID, req.Options.turnOptions(onDelta))
			return turnRunResult(result, nil), err
		}
	case store.ChatRunContinue:
		sessionID, err = parseSessionID(c)
		if err != nil {
			c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
			return
		}
		if req.MessageID == nil || *req.MessageID == 0 {
			c.JSON(http.StatusBadRequest, errorResponse{Error: "message_id is required"})
			return
		}
		request = map[string]any{"message_id": req.MessageID, "options": req.Options}
		execute = func(ctx context.Context, onDelta func(chat.StreamEvent)) (*chatrun.Result, error) {
			result, err := s.pipeline.Continue(ctx, sessionID, *req.MessageID, chat.TurnOptions{OnDelta: onDelta})
			return turnRunResult(result, nil), err
		}
	default:
		c.JSON(http.StatusBadRequest, errorResponse{Error: "kind must be message, regenerate, or continue"})
		return
	}
	sess, err = s.sessions.Get(sessionID)
	if err != nil {
		c.JSON(http.StatusNotFound, errorResponse{Error: "session not found"})
		return
	}

	run, err := s.chatRuns.Start(sessionID, sess.ProjectID, req.Kind, request, execute)
	switch {
	case errors.Is(err, chatrun.ErrRunActive):
		c.JSON(http.StatusConflict, errorResponse{Error: "a chat generation is already running for this session"})
	case err != nil:
		internalError(c, err)
	default:
		started = true
		c.JSON(http.StatusAccepted, newChatRunResponse(run))
	}
}

func (s *Server) getActiveChatRun(c *gin.Context) {
	sessionID, err := parseSessionID(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	run, err := s.chatRuns.ActiveForSession(sessionID)
	if errors.Is(err, chatrun.ErrRunNotFound) {
		c.JSON(http.StatusNotFound, errorResponse{Error: "no active chat run"})
		return
	}
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, newChatRunResponse(run))
}

func (s *Server) getChatRun(c *gin.Context) {
	runID, err := parseUintParam(c, "id", "chat run id")
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	run, err := s.chatRuns.Get(runID)
	if errors.Is(err, chatrun.ErrRunNotFound) {
		c.JSON(http.StatusNotFound, errorResponse{Error: err.Error()})
		return
	}
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, newChatRunResponse(run))
}

func (s *Server) cancelChatRun(c *gin.Context) {
	runID, err := parseUintParam(c, "id", "chat run id")
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	err = s.chatRuns.Cancel(runID)
	switch {
	case errors.Is(err, chatrun.ErrRunNotFound):
		c.JSON(http.StatusNotFound, errorResponse{Error: err.Error()})
	case errors.Is(err, chatrun.ErrRunFinished):
		c.JSON(http.StatusConflict, errorResponse{Error: err.Error()})
	case err != nil:
		internalError(c, err)
	default:
		c.JSON(http.StatusAccepted, gin.H{"status": "cancelling"})
	}
}

func (s *Server) cancelActiveChatRun(c *gin.Context) {
	sessionID, err := parseSessionID(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	err = s.chatRuns.CancelSession(sessionID)
	switch {
	case errors.Is(err, chatrun.ErrRunNotFound):
		c.JSON(http.StatusNotFound, errorResponse{Error: "no active chat run"})
	case errors.Is(err, chatrun.ErrRunFinished):
		c.JSON(http.StatusConflict, errorResponse{Error: err.Error()})
	case err != nil:
		internalError(c, err)
	default:
		c.JSON(http.StatusAccepted, gin.H{"status": "cancelling"})
	}
}

func (s *Server) streamChatRun(c *gin.Context) {
	runID, err := parseUintParam(c, "id", "chat run id")
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	run, events, sub, err := s.chatRuns.Subscribe(runID)
	if errors.Is(err, chatrun.ErrRunNotFound) {
		c.JSON(http.StatusNotFound, errorResponse{Error: err.Error()})
		return
	}
	if err != nil {
		internalError(c, err)
		return
	}
	defer sub.Close()

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache, no-transform")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	c.Writer.Flush()
	emit := func(sequence uint64, event string, data any) {
		c.SSEvent(event, streamEventEnvelope{Sequence: sequence, Data: data})
		c.Writer.Flush()
	}
	sequence := uint64(0)
	emit(sequence, "snapshot", newChatRunResponse(run))
	for index, event := range events {
		// Permission requests block generation. If replay contains a later
		// event, this request was already resolved and must not be shown again.
		if event.Type == "ask_permission" && index != len(events)-1 {
			continue
		}
		sequence++
		emit(sequence, event.Type, json.RawMessage(event.Payload))
	}
	if run.Status.IsTerminal() {
		emitRunDone(func(event string, data any) { sequence++; emit(sequence, event, data) }, run)
		return
	}
	for {
		select {
		case <-c.Request.Context().Done():
			return
		case event, ok := <-sub.Events:
			if !ok {
				final, getErr := s.chatRuns.Get(runID)
				if getErr == nil {
					emitRunDone(func(event string, data any) { sequence++; emit(sequence, event, data) }, final)
				}
				return
			}
			if event.Type == "done" {
				final, getErr := s.chatRuns.Get(runID)
				if getErr == nil {
					emitRunDone(func(event string, data any) { sequence++; emit(sequence, event, data) }, final)
				}
			} else {
				sequence++
				emit(sequence, event.Type, json.RawMessage(event.Payload))
			}
			if event.Type == "done" {
				select {
				case <-time.After(s.streamDoneGrace):
				case <-c.Request.Context().Done():
				}
				return
			}
		}
	}
}

func (s *Server) prepareMessageRun(c *gin.Context) (uint, sendMessageRequest, *upload.Result, chatrun.Execute, map[string]any) {
	sessionID, req, uploadResult, ok := s.prepareMessage(c)
	if !ok {
		return 0, sendMessageRequest{}, nil, nil, nil
	}
	return sessionID, req, uploadResult, messageRunExecute(s, sessionID, req, uploadResult), map[string]any{"text": req.Text, "options": req}
}

func (s *Server) prepareMessageRunFromRequest(c *gin.Context, text string, req sendMessageRequest) (uint, sendMessageRequest, *upload.Result, chatrun.Execute, map[string]any) {
	sessionID, err := parseSessionID(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return 0, sendMessageRequest{}, nil, nil, nil
	}
	if err := validateGenerationOptions(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return 0, sendMessageRequest{}, nil, nil, nil
	}
	if err := validateBranchSelection(req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return 0, sendMessageRequest{}, nil, nil, nil
	}
	if strings.TrimSpace(text) == "" {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "text is required"})
		return 0, sendMessageRequest{}, nil, nil, nil
	}
	if command.IsCommand(text) {
		result, err := s.commands.ExecuteResult(sessionID, text)
		if err != nil {
			c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		} else {
			c.JSON(http.StatusOK, sendMessageResponse{CommandResponse: result.Response, SessionTarget: result.SessionTarget})
		}
		return 0, sendMessageRequest{}, nil, nil, nil
	}
	req.Text = text
	return sessionID, req, nil, messageRunExecute(s, sessionID, req, nil), map[string]any{"text": req.Text, "options": req}
}

func messageRunExecute(s *Server, sessionID uint, req sendMessageRequest, uploadResult *upload.Result) chatrun.Execute {
	return func(ctx context.Context, onDelta func(chat.StreamEvent)) (*chatrun.Result, error) {
		result, err := s.pipeline.Run(ctx, sessionID, req.Text, req.turnOptions(onDelta))
		if result == nil {
			rollbackUpload(uploadResult)
			return nil, err
		}
		commitUpload(uploadResult)
		return turnRunResult(result, uploadResult), err
	}
}

func turnRunResult(result *chat.TurnResult, uploadResult *upload.Result) *chatrun.Result {
	if result == nil {
		return nil
	}
	response := sendMessageResponse{
		Message:            result.Message,
		Metadata:           mergeMetadata(result.Metadata, uploadResult),
		BranchNotActivated: result.Metadata["stale_active_leaf"] == true,
	}
	var finalMessageID *uint
	if result.Message != nil {
		finalMessageID = &result.Message.ID
	}
	return &chatrun.Result{FinalMessageID: finalMessageID, Response: response}
}

func emitRunDone(emit func(string, any), run *store.ChatRun) {
	done := chatRunDone{Status: run.Status, Error: publicRunError(run)}
	if len(run.Result) == 0 {
		emit("done", done)
		return
	}
	var response sendMessageResponse
	if err := json.Unmarshal(run.Result, &response); err != nil {
		emit("error", "invalid chat run result")
		return
	}
	done.Response = response
	emit("done", done)
}
