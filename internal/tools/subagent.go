package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/Cyvadra/hephaestus/internal/subagent"
	"github.com/Cyvadra/hephaestus/internal/subagentexec"
	"github.com/Cyvadra/hephaestus/internal/toolkit"
	"gorm.io/gorm"
)

type subagentStarter interface {
	StartSpawn(context.Context, subagent.Request) (*store.SubagentRun, error)
	RunFork(context.Context, subagent.Request) (*store.SubagentRun, error)
	AwaitOwned(ctx context.Context, parentSessionID uint, parentRunID *uint, ids []uint, timeout time.Duration) ([]store.SubagentRun, error)
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
func (SubagentTool) Delegating()    {}

func (t SubagentTool) Description() string {
	if t.mode == store.SubagentModeSpawn {
		return "Prefer a subagent for substantial work: code development, multi-step operations, independent research, and long-running background tasks, so the main conversation stays focused. Do trivial tasks directly. Starts an independent background subagent and immediately returns its run id. Use spawn when the task can proceed in parallel or its result can be consumed later; monitor it with subagent_status, redirect it with subagent_steer, stop it with subagent_stop, and use await when this response needs its result."
	}
	return "Prefer a subagent for substantial work: code development, multi-step operations, independent research, and long-running background tasks, so the main conversation stays focused. Do trivial tasks directly. Forks the current conversation into an independent subagent, waits for it to finish, and returns its result. Use fork when the current response depends on the delegated task's result."
}

func (SubagentTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"description": map[string]any{"type": "string", "description": "Short 3-5 word task label."},
		"category":    map[string]any{"type": "string", "enum": subagentCategoryValues(), "description": "Task type: coding (implementation, debugging, tests), operations (system or deployment work), research (investigation), background (long-running independent work), or general."},
		"prompt":      map[string]any{"type": "string", "description": "Complete task instructions for the subagent."},
	}, "required": []string{"description", "category", "prompt"}}
}

func (t SubagentTool) Execute(ctx context.Context, args map[string]any) *toolkit.ToolResult {
	label, _ := args["description"].(string)
	categoryText, _ := args["category"].(string)
	prompt, _ := args["prompt"].(string)
	label, categoryText, prompt = strings.TrimSpace(label), strings.TrimSpace(categoryText), strings.TrimSpace(prompt)
	category := store.SubagentCategory(categoryText)
	if label == "" || categoryText == "" || prompt == "" {
		return toolkit.ErrorResult(t.Name() + ": description, category, and prompt are required")
	}
	if !category.Valid() {
		return toolkit.ErrorResult(fmt.Sprintf("%s: invalid category %q", t.Name(), categoryText))
	}
	sessionID, parentRunID, failure := subagentOwner(ctx, t.Name())
	if failure != nil {
		return failure
	}
	var parent store.Session
	if err := t.db.First(&parent, sessionID).Error; err != nil {
		return toolkit.ErrorResult(fmt.Sprintf("%s: load parent session: %v", t.Name(), err))
	}
	request := subagent.Request{ParentSessionID: sessionID, ParentRunID: parentRunID, ProjectID: parent.ProjectID, Category: category, Label: label, Prompt: prompt}
	if chatRunID, ok := toolkit.ChatRunIDFromContext(ctx); ok {
		request.ParentChatRunID = &chatRunID
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
			// Partial output from a failed child is still useful work; never drop it.
			message := fmt.Sprintf("fork run %d %s: %s", run.ID, run.Status, run.Error)
			if strings.TrimSpace(run.Result) != "" {
				message += "\nPartial result before failure:\n" + run.Result
			}
			return toolkit.ErrorResult(message)
		}
		return toolkit.NewToolResult(fmt.Sprintf("fork run %d completed:\n%s", run.ID, run.Result))
	}
	run, err := t.service.StartSpawn(ctx, request)
	if err != nil {
		return toolkit.ErrorResult("spawn: " + err.Error())
	}
	return toolkit.NewToolResult(fmt.Sprintf("spawned background subagent run %d", run.ID))
}

func subagentCategoryValues() []string {
	categories := store.SubagentCategories()
	values := make([]string, len(categories))
	for index := range categories {
		values[index] = string(categories[index])
	}
	return values
}

type SubagentAwaitTool struct{ service subagentStarter }

type subagentResult struct {
	RunID  uint                    `json:"run_id"`
	Label  string                  `json:"label"`
	Status store.SubagentRunStatus `json:"status"`
	// Progress is the latest in-progress output of a running run.
	Progress string `json:"progress,omitempty"`
	Result   string `json:"result,omitempty"`
	Error    string `json:"error,omitempty"`
	// StillRunning marks a run that had not finished when await timed out.
	StillRunning bool `json:"still_running,omitempty"`
}

func NewSubagentAwaitTool(service subagentStarter) *SubagentAwaitTool {
	return &SubagentAwaitTool{service: service}
}
func (SubagentAwaitTool) Name() string { return "await" }
func (SubagentAwaitTool) Delegating()  {}
func (SubagentAwaitTool) Description() string {
	return "Waits for subagents you spawned and returns their results, including partial output from failed runs. By default waits for all your background subagents active when await was called; pass run_ids to wait for specific runs. With timeout_seconds, returns early with still_running set on unfinished runs. It does not wait for descendants or cancel tasks."
}
func (SubagentAwaitTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"run_ids":         map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "Specific run ids to wait for. Omit to wait for all active background subagents you spawned."},
		"timeout_seconds": map[string]any{"type": "integer", "description": "Stop waiting after this many seconds and report current state. Omit or 0 to wait until finished."},
	}}
}
func subagentRunIDsArg(args map[string]any, tool string) ([]uint, *toolkit.ToolResult) {
	raw, present := args["run_ids"]
	if !present || raw == nil {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, toolkit.ErrorResult(tool + ": run_ids must be an array of positive integers")
	}
	ids := make([]uint, 0, len(items))
	for _, item := range items {
		value, ok := item.(float64)
		if !ok || value < 1 || value != float64(uint(value)) {
			return nil, toolkit.ErrorResult(tool + ": run_ids must be an array of positive integers")
		}
		ids = append(ids, uint(value))
	}
	return ids, nil
}

// maxAwaitTimeout bounds timeout_seconds so the conversion to a Duration
// cannot overflow into a negative (unbounded) wait.
const maxAwaitTimeout = 24 * time.Hour

func awaitTimeoutArg(args map[string]any, tool string) (time.Duration, *toolkit.ToolResult) {
	raw, present := args["timeout_seconds"]
	if !present || raw == nil {
		return 0, nil
	}
	value, ok := raw.(float64)
	if !ok || value < 0 {
		return 0, toolkit.ErrorResult(tool + ": timeout_seconds must be a non-negative number")
	}
	if value >= maxAwaitTimeout.Seconds() {
		return maxAwaitTimeout, nil
	}
	return time.Duration(value * float64(time.Second)), nil
}

func (t SubagentAwaitTool) Execute(ctx context.Context, args map[string]any) *toolkit.ToolResult {
	sessionID, parentRunID, failure := subagentOwner(ctx, t.Name())
	if failure != nil {
		return failure
	}
	ids, failure := subagentRunIDsArg(args, t.Name())
	if failure != nil {
		return failure
	}
	timeout, failure := awaitTimeoutArg(args, t.Name())
	if failure != nil {
		return failure
	}
	runs, err := t.service.AwaitOwned(ctx, sessionID, parentRunID, ids, timeout)
	if errors.Is(err, subagent.ErrRunNotFound) {
		return toolkit.ErrorResult("await: one or more run_ids are not subagents you spawned")
	}
	if err != nil {
		return toolkit.ErrorResult("await: " + err.Error())
	}
	results := make([]subagentResult, len(runs))
	for index := range runs {
		results[index] = subagentResult{
			RunID: runs[index].ID, Label: runs[index].Label, Status: runs[index].Status,
			Result: runs[index].Result, Error: runs[index].Error,
			StillRunning: !runs[index].Terminal(),
		}
	}
	return jsonResult(t.Name(), results)
}
