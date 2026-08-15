package subagentexec

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Cyvadra/hephaestus/internal/chat"
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
}

func NewPipelineExecutor(db *gorm.DB, sessions *session.Service, pipeline *chat.Pipeline) *PipelineExecutor {
	return &PipelineExecutor{db: db, sessions: sessions, pipeline: pipeline}
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
	if len(run.Seed) > 0 {
		for index := range seed {
			seed[index].ID = 0
			seed[index].SessionID = 0
			seed[index].ParentMessageID = nil
			seed[index].Attachments = nil
		}
		if _, err := e.sessions.AppendMessages(child.ID, nil, seed); err != nil {
			return child.ID, "", fmt.Errorf("subagent: persist seed: %w", err)
		}
	}
	result, err := e.pipeline.Run(ctx, child.ID, run.Prompt, chat.TurnOptions{})
	if result == nil || result.Message == nil {
		return child.ID, "", err
	}
	return child.ID, result.Message.Content, err
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
