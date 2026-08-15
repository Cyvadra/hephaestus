package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/Cyvadra/hephaestus/internal/subagent"
	"github.com/Cyvadra/hephaestus/internal/subagentexec"
	"github.com/Cyvadra/hephaestus/internal/toolkit"
	"github.com/Cyvadra/hephaestus/internal/transform"
	"gorm.io/gorm"
)

type subagentStarter interface {
	StartSpawn(context.Context, subagent.Request) (*store.SubagentRun, error)
	RunFork(context.Context, subagent.Request) (*store.SubagentRun, error)
	AwaitActiveDirect(context.Context, uint, *uint) ([]store.SubagentRun, error)
}

type SubagentTool struct {
	db      *gorm.DB
	service subagentStarter
	mode    store.SubagentMode
}

func NewSpawnTool(db *gorm.DB, service subagentStarter) *SubagentTool {
	return &SubagentTool{db: db, service: service, mode: store.SubagentModeSpawn}
}

func NewForkTool(db *gorm.DB, service subagentStarter) *SubagentTool {
	return &SubagentTool{db: db, service: service, mode: store.SubagentModeFork}
}

func (t SubagentTool) Name() string { return string(t.mode) }

func (t SubagentTool) Description() string {
	if t.mode == store.SubagentModeSpawn {
		return "Starts an independent subagent in the background and immediately returns its run id. Use await when the current response depends on active spawned tasks."
	}
	return "Forks the current conversation into an independent subagent, waits for it to finish, and returns its result."
}

func (SubagentTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"description": map[string]any{"type": "string", "description": "Short 3-5 word task label."},
		"prompt":      map[string]any{"type": "string", "description": "Complete task instructions for the subagent."},
	}, "required": []string{"description", "prompt"}}
}

func (t SubagentTool) Execute(ctx context.Context, args map[string]any) *toolkit.ToolResult {
	label, _ := args["description"].(string)
	prompt, _ := args["prompt"].(string)
	label, prompt = strings.TrimSpace(label), strings.TrimSpace(prompt)
	if label == "" || prompt == "" {
		return toolkit.ErrorResult(t.Name() + ": description and prompt are required")
	}
	sessionID, ok := toolkit.SessionIDFromContext(ctx)
	if !ok || sessionID == 0 {
		return toolkit.ErrorResult(t.Name() + ": no parent session in context")
	}
	var parent store.Session
	if err := t.db.First(&parent, sessionID).Error; err != nil {
		return toolkit.ErrorResult(fmt.Sprintf("%s: load parent session: %v", t.Name(), err))
	}
	owner := toolkit.SubagentContextFromContext(ctx)
	request := subagent.Request{ParentSessionID: sessionID, ProjectID: parent.ProjectID, Depth: owner.Depth, Label: label, Prompt: prompt}
	if owner.RunID != 0 {
		request.ParentRunID = &owner.RunID
	}
	if t.mode == store.SubagentModeFork {
		messages, ok := toolkit.TurnMessagesFromContext(ctx)
		if !ok || len(messages) == 0 {
			return toolkit.ErrorResult("fork: current turn snapshot unavailable")
		}
		seed, err := subagentexec.ForkSeed(messages)
		if err != nil {
			return toolkit.ErrorResult(err.Error())
		}
		request.Seed = seed
		run, err := t.service.RunFork(ctx, request)
		if err != nil {
			return toolkit.ErrorResult("fork: " + err.Error())
		}
		if run.Status != store.SubagentRunSucceeded {
			return toolkit.ErrorResult(fmt.Sprintf("fork run %d %s: %s", run.ID, run.Status, run.Error))
		}
		return toolkit.NewToolResult(fmt.Sprintf("fork run %d completed:\n%s", run.ID, run.Result))
	}
	run, err := t.service.StartSpawn(ctx, request)
	if err != nil {
		return toolkit.ErrorResult("spawn: " + err.Error())
	}
	return toolkit.NewToolResult(fmt.Sprintf("spawned background subagent run %d", run.ID))
}

type SubagentAwaitTool struct{ service subagentStarter }

type subagentResult struct {
	RunID  uint                    `json:"run_id"`
	Label  string                  `json:"label"`
	Status store.SubagentRunStatus `json:"status"`
	Result string                  `json:"result,omitempty"`
	Error  string                  `json:"error,omitempty"`
}

func NewSubagentAwaitTool(service subagentStarter) *SubagentAwaitTool {
	return &SubagentAwaitTool{service: service}
}
func (SubagentAwaitTool) Name() string { return "await" }
func (SubagentAwaitTool) Description() string {
	return "Waits for the background subagents directly spawned by this agent that were active when await was called. It does not wait for descendants or cancel tasks."
}
func (SubagentAwaitTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (t SubagentAwaitTool) Execute(ctx context.Context, _ map[string]any) *toolkit.ToolResult {
	sessionID, ok := toolkit.SessionIDFromContext(ctx)
	if !ok || sessionID == 0 {
		return toolkit.ErrorResult("await: no parent session in context")
	}
	owner := toolkit.SubagentContextFromContext(ctx)
	var parentRunID *uint
	if owner.RunID != 0 {
		parentRunID = &owner.RunID
	}
	runs, err := t.service.AwaitActiveDirect(ctx, sessionID, parentRunID)
	if err != nil {
		return toolkit.ErrorResult("await: " + err.Error())
	}
	results := make([]subagentResult, len(runs))
	for index := range runs {
		results[index] = subagentResult{
			RunID: runs[index].ID, Label: runs[index].Label, Status: runs[index].Status,
			Result: transform.LimitTextBytes(runs[index].Result, 64*1024), Error: transform.LimitTextBytes(runs[index].Error, 8*1024),
		}
	}
	encoded, err := json.Marshal(results)
	if err != nil {
		return toolkit.ErrorResult("await: encode results: " + err.Error())
	}
	return toolkit.NewToolResult(string(encoded))
}
