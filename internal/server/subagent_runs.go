package server

import (
	"errors"
	"net/http"
	"time"

	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/Cyvadra/hephaestus/internal/subagent"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type subagentRunResponse struct {
	ID              uint                    `json:"id"`
	ParentSessionID uint                    `json:"parent_session_id"`
	ParentRunID     *uint                   `json:"parent_run_id,omitempty"`
	ChildSessionID  *uint                   `json:"child_session_id,omitempty"`
	Mode            store.SubagentMode      `json:"mode"`
	Schedule        store.SubagentSchedule  `json:"schedule"`
	Status          store.SubagentRunStatus `json:"status"`
	Depth           int                     `json:"depth"`
	Category        store.SubagentCategory  `json:"category"`
	Label           string                  `json:"label"`
	Result          string                  `json:"result,omitempty"`
	Error           string                  `json:"error,omitempty"`
}

type subagentRunSummary struct {
	ID             uint                    `json:"id"`
	ChildSessionID *uint                   `json:"child_session_id,omitempty"`
	Status         store.SubagentRunStatus `json:"status"`
	Category       store.SubagentCategory  `json:"category"`
	Label          string                  `json:"label"`
	StartedAt      *time.Time              `json:"started_at,omitempty"`
	FinishedAt     *time.Time              `json:"finished_at,omitempty"`
	CreatedAt      time.Time               `json:"created_at"`
}

func publicSubagentRun(run store.SubagentRun) subagentRunResponse {
	return subagentRunResponse{ID: run.ID, ParentSessionID: run.ParentSessionID, ParentRunID: run.ParentRunID, ChildSessionID: run.ChildSessionID, Mode: run.Mode, Schedule: run.Schedule, Status: run.Status, Depth: run.Depth, Category: run.Category, Label: run.Label, Result: run.Result, Error: run.Error}
}

func publicSubagentRunSummary(run store.SubagentRun) subagentRunSummary {
	return subagentRunSummary{ID: run.ID, ChildSessionID: run.ChildSessionID, Status: run.Status, Category: run.Category, Label: run.Label, StartedAt: run.StartedAt, FinishedAt: run.FinishedAt, CreatedAt: run.CreatedAt}
}

// listSubagentRuns godoc
//
//	@Summary		List session subagent runs
//	@Tags			subagents
//	@Produce		json
//	@Param			id path int true "Parent session ID"
//	@Success		200 {array} subagentRunResponse
//	@Failure		400 {object} errorResponse
//	@Router			/sessions/{id}/subagent-runs [get]
func (s *Server) listSubagentRuns(c *gin.Context) {
	sessionID, err := parseSessionID(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	runs, err := s.subagents.ListByParentSession(sessionID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, errorResponse{Error: "session not found"})
		return
	}
	if err != nil {
		internalError(c, err)
		return
	}
	response := make([]subagentRunResponse, len(runs))
	for i := range runs {
		response[i] = publicSubagentRun(runs[i])
	}
	c.JSON(http.StatusOK, response)
}

// getSubagentRun godoc
//
//	@Summary		Get a subagent run
//	@Tags			subagents
//	@Produce		json
//	@Param			id path int true "Subagent run ID"
//	@Success		200 {object} subagentRunResponse
//	@Failure		404 {object} errorResponse
//	@Router			/subagent-runs/{id} [get]
func (s *Server) getSubagentRun(c *gin.Context) {
	runID, err := parseUintParam(c, "id", "subagent run id")
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	run, err := s.subagents.Get(runID)
	if errors.Is(err, subagent.ErrRunNotFound) {
		c.JSON(http.StatusNotFound, errorResponse{Error: err.Error()})
		return
	}
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, publicSubagentRun(*run))
}

// cancelSubagentRun godoc
//
//	@Summary		Cancel a subagent run
//	@Tags			subagents
//	@Produce		json
//	@Param			id path int true "Subagent run ID"
//	@Success		202 {object} map[string]string
//	@Failure		404 {object} errorResponse
//	@Failure		409 {object} errorResponse
//	@Router			/subagent-runs/{id}/cancel [post]
func (s *Server) cancelSubagentRun(c *gin.Context) {
	runID, err := parseUintParam(c, "id", "subagent run id")
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	err = s.subagents.Cancel(runID)
	switch {
	case errors.Is(err, subagent.ErrRunNotFound):
		c.JSON(http.StatusNotFound, errorResponse{Error: err.Error()})
	case errors.Is(err, subagent.ErrRunFinished):
		c.JSON(http.StatusConflict, errorResponse{Error: err.Error()})
	case err != nil:
		internalError(c, err)
	default:
		c.JSON(http.StatusAccepted, gin.H{"status": "cancelling"})
	}
}
