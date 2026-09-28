package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/Cyvadra/hephaestus/internal/subagent"
	"github.com/Cyvadra/hephaestus/internal/toolkit"
)

// subagentController inspects and controls the caller's direct children.
type subagentController interface {
	ListOwned(parentSessionID uint, parentRunID *uint, activeOnly bool) ([]store.SubagentRun, error)
	Status(runID, parentSessionID uint, parentRunID *uint) (*subagent.RunStatus, error)
	Steer(runID, parentSessionID uint, parentRunID *uint, text string, aggressive bool) (string, error)
	StopOwned(runID, parentSessionID uint, parentRunID *uint) error
}

// subagentOwner resolves the calling agent: the main agent of a session, or
// the subagent run executing in it.
func subagentOwner(ctx context.Context, tool string) (uint, *uint, *toolkit.ToolResult) {
	sessionID, ok := toolkit.SessionIDFromContext(ctx)
	if !ok || sessionID == 0 {
		return 0, nil, toolkit.ErrorResult(tool + ": no parent session in context")
	}
	owner := toolkit.SubagentContextFromContext(ctx)
	if owner.RunID == 0 {
		return sessionID, nil, nil
	}
	runID := owner.RunID
	return sessionID, &runID, nil
}

func subagentRunIDArg(args map[string]any, tool string) (uint, *toolkit.ToolResult) {
	value, ok := args["run_id"].(float64)
	if !ok || value < 1 || value != float64(uint(value)) {
		return 0, toolkit.ErrorResult(tool + ": run_id must be a positive integer")
	}
	return uint(value), nil
}

// ownedRunArgs resolves the calling agent and the run_id it targets.
func ownedRunArgs(ctx context.Context, args map[string]any, tool string) (uint, *uint, uint, *toolkit.ToolResult) {
	sessionID, parentRunID, failure := subagentOwner(ctx, tool)
	if failure != nil {
		return 0, nil, 0, failure
	}
	runID, failure := subagentRunIDArg(args, tool)
	return sessionID, parentRunID, runID, failure
}

func subagentControlError(tool string, runID uint, err error) *toolkit.ToolResult {
	switch {
	case errors.Is(err, subagent.ErrRunNotFound):
		return toolkit.ErrorResult(fmt.Sprintf("%s: run %d not found among subagents you spawned", tool, runID))
	case errors.Is(err, subagent.ErrChildStarting):
		return toolkit.ErrorResult(fmt.Sprintf("%s: run %d is still starting; retry shortly", tool, runID))
	case errors.Is(err, subagent.ErrRunFinished):
		return toolkit.ErrorResult(fmt.Sprintf("%s: run %d has already finished; use subagent_status to read its result", tool, runID))
	default:
		return toolkit.ErrorResult(fmt.Sprintf("%s: %v", tool, err))
	}
}

func jsonResult(tool string, value any) *toolkit.ToolResult {
	encoded, err := json.Marshal(value)
	if err != nil {
		return toolkit.ErrorResult(tool + ": encode result: " + err.Error())
	}
	return toolkit.NewToolResult(string(encoded))
}

var subagentRunIDParam = map[string]any{"type": "integer", "description": "Run id returned by spawn or fork, or listed by subagent_list."}

type SubagentListTool struct{ service subagentController }

type subagentListEntry struct {
	RunID      uint                    `json:"run_id"`
	Label      string                  `json:"label"`
	Category   store.SubagentCategory  `json:"category"`
	Mode       store.SubagentMode      `json:"mode"`
	Status     store.SubagentRunStatus `json:"status"`
	StartedAt  *time.Time              `json:"started_at,omitempty"`
	FinishedAt *time.Time              `json:"finished_at,omitempty"`
}

func NewSubagentListTool(service subagentController) *SubagentListTool {
	return &SubagentListTool{service: service}
}
func (SubagentListTool) Name() string { return "subagent_list" }
func (SubagentListTool) Delegating()  {}
func (SubagentListTool) Description() string {
	return "Lists the subagents you spawned or forked (newest first, up to 50) with their run id, label, and status. Use subagent_status to read a run's progress or result."
}
func (SubagentListTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"active_only": map[string]any{"type": "boolean", "description": "List only pending or running subagents."},
	}}
}
func (t SubagentListTool) Execute(ctx context.Context, args map[string]any) *toolkit.ToolResult {
	sessionID, parentRunID, failure := subagentOwner(ctx, t.Name())
	if failure != nil {
		return failure
	}
	activeOnly, _ := args["active_only"].(bool)
	runs, err := t.service.ListOwned(sessionID, parentRunID, activeOnly)
	if err != nil {
		return toolkit.ErrorResult(t.Name() + ": " + err.Error())
	}
	entries := make([]subagentListEntry, len(runs))
	for i, run := range runs {
		entries[i] = subagentListEntry{RunID: run.ID, Label: run.Label, Category: run.Category, Mode: run.Mode, Status: run.Status, StartedAt: run.StartedAt, FinishedAt: run.FinishedAt}
	}
	return jsonResult(t.Name(), entries)
}

type SubagentStatusTool struct{ service subagentController }

func NewSubagentStatusTool(service subagentController) *SubagentStatusTool {
	return &SubagentStatusTool{service: service}
}
func (SubagentStatusTool) Name() string { return "subagent_status" }
func (SubagentStatusTool) Delegating()  {}
func (SubagentStatusTool) Description() string {
	return "Reports one of your subagents without waiting: status, the latest in-progress output while running, or the full result and error once finished (including partial output from failed or stopped runs)."
}
func (SubagentStatusTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{"run_id": subagentRunIDParam}, "required": []string{"run_id"}}
}
func (t SubagentStatusTool) Execute(ctx context.Context, args map[string]any) *toolkit.ToolResult {
	sessionID, parentRunID, runID, failure := ownedRunArgs(ctx, args, t.Name())
	if failure != nil {
		return failure
	}
	status, err := t.service.Status(runID, sessionID, parentRunID)
	if err != nil {
		return subagentControlError(t.Name(), runID, err)
	}
	run := status.Run
	return jsonResult(t.Name(), subagentResult{RunID: run.ID, Label: run.Label, Status: run.Status, Progress: status.Progress, Result: run.Result, Error: run.Error})
}

type SubagentSteerTool struct{ service subagentController }

func NewSubagentSteerTool(service subagentController) *SubagentSteerTool {
	return &SubagentSteerTool{service: service}
}
func (SubagentSteerTool) Name() string { return "subagent_steer" }
func (SubagentSteerTool) Delegating()  {}
func (SubagentSteerTool) Description() string {
	return "Sends a new instruction to one of your running subagents, delivered at its next tool boundary; if it finishes its turn first, the instruction is reported as undelivered in its result. A later message replaces an undelivered earlier one. Set interrupt to also skip the subagent's next pending tool call."
}
func (SubagentSteerTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"run_id":    subagentRunIDParam,
		"message":   map[string]any{"type": "string", "description": "Instruction for the subagent."},
		"interrupt": map[string]any{"type": "boolean", "description": "Skip the subagent's next selected tool call so the instruction takes effect immediately."},
	}, "required": []string{"run_id", "message"}}
}
func (t SubagentSteerTool) Execute(ctx context.Context, args map[string]any) *toolkit.ToolResult {
	sessionID, parentRunID, runID, failure := ownedRunArgs(ctx, args, t.Name())
	if failure != nil {
		return failure
	}
	message, _ := args["message"].(string)
	message = strings.TrimSpace(message)
	if message == "" {
		return toolkit.ErrorResult(t.Name() + ": message is required")
	}
	interrupt, _ := args["interrupt"].(bool)
	outcome, err := t.service.Steer(runID, sessionID, parentRunID, message, interrupt)
	if err != nil {
		return subagentControlError(t.Name(), runID, err)
	}
	return jsonResult(t.Name(), map[string]any{"run_id": runID, "outcome": outcome})
}

type SubagentStopTool struct{ service subagentController }

func NewSubagentStopTool(service subagentController) *SubagentStopTool {
	return &SubagentStopTool{service: service}
}
func (SubagentStopTool) Name() string { return "subagent_stop" }
func (SubagentStopTool) Delegating()  {}
func (SubagentStopTool) Description() string {
	return "Stops one of your running subagents. Its partial output is kept; read it with subagent_status."
}
func (SubagentStopTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{"run_id": subagentRunIDParam}, "required": []string{"run_id"}}
}
func (t SubagentStopTool) Execute(ctx context.Context, args map[string]any) *toolkit.ToolResult {
	sessionID, parentRunID, runID, failure := ownedRunArgs(ctx, args, t.Name())
	if failure != nil {
		return failure
	}
	if err := t.service.StopOwned(runID, sessionID, parentRunID); err != nil {
		return subagentControlError(t.Name(), runID, err)
	}
	return jsonResult(t.Name(), map[string]any{"run_id": runID, "status": "cancelling"})
}
