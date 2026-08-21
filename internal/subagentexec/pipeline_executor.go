package subagentexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Cyvadra/ds4"
	"github.com/Cyvadra/hephaestus/internal/chat"
	"github.com/Cyvadra/hephaestus/internal/chatrun"
	"github.com/Cyvadra/hephaestus/internal/session"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/Cyvadra/hephaestus/internal/subagent"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// PipelineExecutor runs delegated work as a normal turn in an independent
// child session, retaining all existing identity, plugin, tool, and workspace behavior.
type PipelineExecutor struct {
	db       *gorm.DB
	sessions *session.Service
	pipeline *chat.Pipeline
	chatRuns *chatrun.Service
}

func NewPipelineExecutor(db *gorm.DB, sessions *session.Service, pipeline *chat.Pipeline, chatRuns *chatrun.Service) *PipelineExecutor {
	return &PipelineExecutor{db: db, sessions: sessions, pipeline: pipeline, chatRuns: chatRuns}
}

func (e *PipelineExecutor) ExecuteSubagent(ctx context.Context, run *store.SubagentRun) (uint, string, error) {
	var parent store.Session
	if err := e.db.First(&parent, run.ParentSessionID).Error; err != nil {
		return 0, "", fmt.Errorf("subagent: load parent session: %w", err)
	}
	var seed []store.ChatMessage
	if len(run.Seed) > 0 {
		if err := json.Unmarshal(run.Seed, &seed); err != nil {
			return 0, "", fmt.Errorf("subagent: decode seed: %w", err)
		}
	}
	child := store.Session{
		ProjectID: parent.ProjectID, SourceConcierge: parent.SourceConcierge,
		Settings: parent.Settings, ReasoningEffort: parent.ReasoningEffort,
		EnableWebSearch: parent.EnableWebSearch, Title: run.Label, LastMessageTime: time.Now(),
		ParentSubagentRunID: &run.ID,
	}
	if err := e.db.Create(&child).Error; err != nil {
		return 0, "", fmt.Errorf("subagent: create child session: %w", err)
	}
	if err := e.db.Model(&store.SubagentRun{}).Where("id = ?", run.ID).Update("child_session_id", child.ID).Error; err != nil {
		return child.ID, "", fmt.Errorf("subagent: link child session: %w", err)
	}
	var promptParentID *uint
	if len(run.Seed) > 0 {
		for index := range seed {
			seed[index].ID = 0
			seed[index].SessionID = 0
			seed[index].ParentMessageID = nil
			seed[index].Attachments = nil
		}
		savedSeed, err := e.sessions.AppendMessages(child.ID, nil, seed)
		if err != nil {
			return child.ID, "", fmt.Errorf("subagent: persist seed: %w", err)
		}
		promptParentID = childPromptParentID(savedSeed)
	}
	_, err := e.sessions.AppendMessages(child.ID, promptParentID, []store.ChatMessage{{
		Role: ds4.RoleUser, Content: run.Prompt, Status: store.MessageStatusComplete, Timestamp: time.Now(),
	}})
	if err != nil {
		return child.ID, "", fmt.Errorf("subagent: persist prompt: %w", err)
	}
	chatRun, err := e.chatRuns.StartSubagent(child.ID, child.ProjectID, run.ID, map[string]any{"subagent_run_id": run.ID}, func(runCtx context.Context, onDelta func(chat.StreamEvent)) (*chatrun.Result, error) {
		result, runErr := e.pipeline.Regenerate(runCtx, child.ID, chat.TurnOptions{OnDelta: onDelta})
		if result == nil || result.Message == nil {
			return &chatrun.Result{}, runErr
		}
		return &chatrun.Result{FinalMessageID: &result.Message.ID}, runErr
	})
	if err != nil {
		return child.ID, "", fmt.Errorf("subagent: start child chat run: %w", err)
	}
	terminal, err := e.chatRuns.Wait(ctx, chatRun.ID)
	if err != nil {
		_ = e.chatRuns.Cancel(chatRun.ID)
		terminal, err = e.chatRuns.Wait(context.Background(), chatRun.ID)
		if err != nil {
			return child.ID, "", err
		}
	}
	result, resultErr := e.resultText(terminal)
	if resultErr != nil {
		return child.ID, terminal.Snapshot.Data().Content, resultErr
	}
	switch terminal.Status {
	case store.ChatRunSucceeded:
		return child.ID, result, nil
	case store.ChatRunCancelled:
		return child.ID, result, context.Canceled
	default:
		if terminal.Error == "" {
			terminal.Error = string(terminal.Status)
		}
		return child.ID, result, errors.New(terminal.Error)
	}
}

func (e *PipelineExecutor) resultText(run *store.ChatRun) (string, error) {
	if run.FinalMessageID == nil {
		return run.Snapshot.Data().Content, nil
	}
	var message store.ChatMessage
	if err := e.db.First(&message, *run.FinalMessageID).Error; err != nil {
		return "", fmt.Errorf("subagent: load final message: %w", err)
	}
	return message.Content, nil
}

func childPromptParentID(seed []store.ChatMessage) *uint {
	if len(seed) == 0 {
		return nil
	}
	return &seed[len(seed)-1].ID
}

// ForkSeed pairs every open tool call in the current snapshot with an explicit
// synthetic result. The result exists only in the child transcript.
func ForkSeed(messages []store.ChatMessage) ([]store.ChatMessage, error) {
	seed := append([]store.ChatMessage(nil), messages...)
	if len(seed) == 0 {
		return seed, nil
	}
	last := seed[len(seed)-1]
	if len(last.ToolCalls) == 0 {
		return seed, nil
	}
	var calls []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(last.ToolCalls, &calls); err != nil {
		return nil, fmt.Errorf("subagent: decode current tool calls: %w", err)
	}
	for _, call := range calls {
		seed = append(seed, store.ChatMessage{
			Role: "tool", ToolCallID: call.ID, Status: store.MessageStatusComplete,
			Content:   "This tool call was delegated in the parent and is still in progress. Continue independently using the delegated task.",
			Timestamp: time.Now(), ToolCalls: datatypes.JSON(nil),
		})
	}
	return seed, nil
}

var _ subagent.Executor = (*PipelineExecutor)(nil)
