package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Cyvadra/hephaestus/internal/job"
	"github.com/Cyvadra/hephaestus/internal/project"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/Cyvadra/hephaestus/internal/workflow"
	"github.com/gin-gonic/gin"
)

type startWorkflowRunRequest struct {
	Project string         `json:"project"`
	Input   map[string]any `json:"input"`
}

// workflowRunner is the workflow-run surface the handlers need, satisfied by
// *workflow.Service and fakeable in tests.
type workflowRunner interface {
	Start(workflowName, projectName string, input map[string]any) (*store.WorkflowRun, error)
	List(workflowName string, limit, offset int) ([]store.WorkflowRun, error)
	Get(runID uint) (*store.WorkflowRun, []store.WorkflowStepRun, error)
	ListByJobRun(jobRunID uint) ([]store.WorkflowRun, error)
	Cancel(runID uint) error
	Subscribe(runID uint) (*workflow.Subscription, error)
}

// jobRunner is the job-run surface the handlers need, satisfied by
// *job.Service and fakeable in tests.
type jobRunner interface {
	List(jobName string, limit, offset int) ([]store.JobRun, error)
	Get(runID uint) (*store.JobRun, error)
	Cancel(runID uint) error
}

type subagentRunner interface {
	Get(uint) (*store.SubagentRun, error)
	ListByParentSession(uint) ([]store.SubagentRun, error)
	ListBackgroundByParentSessions([]uint) ([]store.SubagentRun, error)
	Cancel(uint) error
}

// startWorkflowRun godoc
//
//	@Summary		Start a workflow run
//	@Description	Begins an asynchronous run of the named Workflow in a Project with the given input, returning the created run (202).
//	@Tags			runs
//	@Accept			json
//	@Produce		json
//	@Param			name	path		string					true	"Workflow name"
//	@Param			request	body		startWorkflowRunRequest	true	"Project and input"
//	@Success		202		{object}	store.WorkflowRun
//	@Failure		400		{object}	errorResponse
//	@Failure		404		{object}	errorResponse
//	@Router			/workflows/{name}/runs [post]
func (s *Server) startWorkflowRun(c *gin.Context) {
	workflowName := c.Param("name")
	var req startWorkflowRunRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	projectName := strings.TrimSpace(req.Project)
	if projectName == "" {
		projectName = project.DefaultName
	}
	run, err := s.workflows.Start(workflowName, projectName, req.Input)
	switch {
	case errors.Is(err, workflow.ErrWorkflowNotFound), errors.Is(err, workflow.ErrProjectNotFound):
		c.JSON(http.StatusNotFound, errorResponse{Error: err.Error()})
	case errors.Is(err, workflow.ErrInvalidInput):
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
	case err != nil:
		internalError(c, err)
	default:
		c.JSON(http.StatusAccepted, run)
	}
}

// listWorkflowRuns godoc
//
//	@Summary		List workflow runs
//	@Description	Lists workflow runs, newest first, with optional workflow-name filter and bounded pagination.
//	@Tags			runs
//	@Produce		json
//	@Param			workflow	query		string	false	"Filter by workflow name"
//	@Param			limit		query		int		false	"Max results (default 50, max 200)"
//	@Param			offset		query		int		false	"Result offset"
//	@Success		200			{array}		store.WorkflowRun
//	@Router			/workflow-runs [get]
func (s *Server) listWorkflowRuns(c *gin.Context) {
	name := strings.TrimSpace(c.Query("workflow"))
	limit, offset := pagination(c)
	runs, err := s.workflows.List(name, limit, offset)
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, runs)
}

type workflowRunDetail struct {
	Run   store.WorkflowRun       `json:"run"`
	Steps []store.WorkflowStepRun `json:"steps"`
}

// getWorkflowRun godoc
//
//	@Summary		Get a workflow run
//	@Description	Returns a workflow run with its ordered steps and transcripts.
//	@Tags			runs
//	@Produce		json
//	@Param			id	path		int	true	"Workflow run ID"
//	@Success		200	{object}	workflowRunDetail
//	@Failure		400	{object}	errorResponse
//	@Failure		404	{object}	errorResponse
//	@Router			/workflow-runs/{id} [get]
func (s *Server) getWorkflowRun(c *gin.Context) {
	id, err := parseUintParam(c, "id", "workflow run id")
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	run, steps, err := s.workflows.Get(id)
	if errors.Is(err, workflow.ErrRunNotFound) {
		c.JSON(http.StatusNotFound, errorResponse{Error: "workflow run not found"})
		return
	}
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, workflowRunDetail{Run: *run, Steps: steps})
}

// cancelWorkflowRun godoc
//
//	@Summary		Cancel a workflow run
//	@Description	Requests cancellation of an active workflow run.
//	@Tags			runs
//	@Produce		json
//	@Param			id	path		int	true	"Workflow run ID"
//	@Success		202	{object}	errorResponse
//	@Failure		400	{object}	errorResponse
//	@Failure		404	{object}	errorResponse
//	@Failure		409	{object}	errorResponse
//	@Router			/workflow-runs/{id}/cancel [post]
func (s *Server) cancelWorkflowRun(c *gin.Context) {
	id, err := parseUintParam(c, "id", "workflow run id")
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	err = s.workflows.Cancel(id)
	switch {
	case errors.Is(err, workflow.ErrRunNotFound):
		c.JSON(http.StatusNotFound, errorResponse{Error: "workflow run not found"})
	case errors.Is(err, workflow.ErrRunFinished):
		c.JSON(http.StatusConflict, errorResponse{Error: err.Error()})
	case err != nil:
		internalError(c, err)
	default:
		c.JSON(http.StatusAccepted, gin.H{"status": "cancelling"})
	}
}

// listJobRuns godoc
//
//	@Summary		List job runs
//	@Description	Lists job runs, newest first, with optional job-name filter and bounded pagination.
//	@Tags			runs
//	@Produce		json
//	@Param			job		query	string	false	"Filter by job name"
//	@Param			limit	query	int		false	"Max results (default 50, max 200)"
//	@Param			offset	query	int		false	"Result offset"
//	@Success		200		{array}	store.JobRun
//	@Router			/job-runs [get]
func (s *Server) listJobRuns(c *gin.Context) {
	name := strings.TrimSpace(c.Query("job"))
	limit, offset := pagination(c)
	runs, err := s.jobs.List(name, limit, offset)
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, runs)
}

type jobRunDetail struct {
	Run          store.JobRun        `json:"run"`
	WorkflowRuns []store.WorkflowRun `json:"workflow_runs"`
}

// getJobRun godoc
//
//	@Summary		Get a job run
//	@Description	Returns a job run with its attempted workflow runs.
//	@Tags			runs
//	@Produce		json
//	@Param			id	path		int	true	"Job run ID"
//	@Success		200	{object}	jobRunDetail
//	@Failure		400	{object}	errorResponse
//	@Failure		404	{object}	errorResponse
//	@Router			/job-runs/{id} [get]
func (s *Server) getJobRun(c *gin.Context) {
	id, err := parseUintParam(c, "id", "job run id")
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	run, err := s.jobs.Get(id)
	if errors.Is(err, job.ErrJobNotFound) {
		c.JSON(http.StatusNotFound, errorResponse{Error: "job run not found"})
		return
	}
	if err != nil {
		internalError(c, err)
		return
	}
	workflowRuns, err := s.workflows.ListByJobRun(id)
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, jobRunDetail{Run: *run, WorkflowRuns: workflowRuns})
}

// cancelJobRun godoc
//
//	@Summary		Cancel a job run
//	@Description	Requests cancellation of an active job run.
//	@Tags			runs
//	@Produce		json
//	@Param			id	path	int	true	"Job run ID"
//	@Success		202	{object}	errorResponse
//	@Failure		400	{object}	errorResponse
//	@Failure		404	{object}	errorResponse
//	@Failure		409	{object}	errorResponse
//	@Router			/job-runs/{id}/cancel [post]
func (s *Server) cancelJobRun(c *gin.Context) {
	id, err := parseUintParam(c, "id", "job run id")
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	err = s.jobs.Cancel(id)
	switch {
	case errors.Is(err, job.ErrJobNotFound):
		c.JSON(http.StatusNotFound, errorResponse{Error: "job run not found"})
	case errors.Is(err, job.ErrRunFinished):
		c.JSON(http.StatusConflict, errorResponse{Error: err.Error()})
	case err != nil:
		internalError(c, err)
	default:
		c.JSON(http.StatusAccepted, gin.H{"status": "cancelling"})
	}
}

// streamWorkflowRun godoc
//
//	@Summary		Stream a workflow run's live progress
//	@Description	Streams a workflow run's live progress over SSE: run snapshots, step lifecycle, and agent deltas (text/reasoning/tool calls). Ends with a "done" event when the run reaches a terminal status.
//	@Tags			runs
//	@Produce		text/event-stream
//	@Param			id	path	int	true	"Workflow run ID"
//	@Success		200
//	@Failure		400	{object}	errorResponse
//	@Failure		404	{object}	errorResponse
//	@Router			/workflow-runs/{id}/stream [get]
func (s *Server) streamWorkflowRun(c *gin.Context) {
	runID, err := parseUintParam(c, "id", "workflow run id")
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	// Subscribe before reading the snapshot so no event between the two is
	// lost; Subscribe itself closes the channel for already-finished runs.
	sub, err := s.workflows.Subscribe(runID)
	if errors.Is(err, workflow.ErrRunNotFound) {
		c.JSON(http.StatusNotFound, errorResponse{Error: "workflow run not found"})
		return
	}
	if err != nil {
		internalError(c, err)
		return
	}
	defer sub.Close()

	run, steps, err := s.workflows.Get(runID)
	if err != nil {
		internalError(c, err)
		return
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache, no-transform")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	c.Writer.Flush()

	sequence := uint64(0)
	ctx := c.Request.Context()
	streamEvent := func(event string, data any) {
		sequence++
		c.SSEvent(event, streamEventEnvelope{Sequence: sequence, Data: data})
		c.Writer.Flush()
	}
	emitDone := func(run *store.WorkflowRun) {
		streamEvent("done", workflow.ProgressEvent{Type: workflow.ProgressDone, Run: run})
		// 发送 done 后短暂保持连接，确保客户端收到 done 并主动调用
		// EventSource.close()，避免浏览器自动重连造成反复请求。
		select {
		case <-time.After(s.streamDoneGrace):
		case <-ctx.Done():
		}
	}

	// 快照与实时事件统一使用 ProgressEvent 信封，前端按同一结构解析。
	streamEvent("run", workflow.ProgressEvent{Type: workflow.ProgressRun, Run: run})
	for _, step := range steps {
		step := step
		streamEvent("step", workflow.ProgressEvent{Type: workflow.ProgressStep, Step: &step})
	}
	if run.Status.IsTerminal() {
		emitDone(run)
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-sub.Events:
			if !ok {
				// The executor closed the stream: fetch the final state.
				if final, _, getErr := s.workflows.Get(runID); getErr == nil {
					streamEvent("run", workflow.ProgressEvent{Type: workflow.ProgressRun, Run: final})
					emitDone(final)
				} else {
					emitDone(nil)
				}
				return
			}
			streamEvent(string(ev.Type), ev)
			if ev.Type == workflow.ProgressRun && ev.Run != nil && ev.Run.Status.IsTerminal() {
				emitDone(ev.Run)
				return
			}
		}
	}
}

func pagination(c *gin.Context) (limit, offset int) {
	limit = 50
	if raw := c.Query("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	if raw := c.Query("offset"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 0 {
			offset = parsed
		}
	}
	return store.NormalizePagination(limit, offset)
}
