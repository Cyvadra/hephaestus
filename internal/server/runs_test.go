package server

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Cyvadra/hephaestus/internal/agent"
	"github.com/Cyvadra/hephaestus/internal/job"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/Cyvadra/hephaestus/internal/workflow"
	"github.com/gin-gonic/gin"
)

type fakeWorkflowRunner struct {
	startErr  error
	started   []string
	getRun    *store.WorkflowRun
	getSteps  []store.WorkflowStepRun
	getErr    error
	listErr   error
	listRuns  []store.WorkflowRun
	byJobErr  error
	byJobRuns []store.WorkflowRun
	cancelErr error

	subEvents chan workflow.ProgressEvent
	subErr    error
	subCount  int
}

func (f *fakeWorkflowRunner) Start(name, project string, input map[string]any) (*store.WorkflowRun, error) {
	f.started = append(f.started, name+"|"+project)
	if f.startErr != nil {
		return nil, f.startErr
	}
	return &store.WorkflowRun{ID: 1, WorkflowName: name, ProjectName: project, Status: store.WorkflowRunPending}, nil
}
func (f *fakeWorkflowRunner) List(string, int, int) ([]store.WorkflowRun, error) {
	return f.listRuns, f.listErr
}
func (f *fakeWorkflowRunner) Get(uint) (*store.WorkflowRun, []store.WorkflowStepRun, error) {
	return f.getRun, f.getSteps, f.getErr
}
func (f *fakeWorkflowRunner) ListByJobRun(uint) ([]store.WorkflowRun, error) {
	return f.byJobRuns, f.byJobErr
}
func (f *fakeWorkflowRunner) Cancel(uint) error { return f.cancelErr }
func (f *fakeWorkflowRunner) Subscribe(uint) (*workflow.Subscription, error) {
	f.subCount++
	if f.subErr != nil {
		return nil, f.subErr
	}
	ch := f.subEvents
	if ch == nil {
		ch = make(chan workflow.ProgressEvent)
		close(ch)
	}
	return &workflow.Subscription{Events: ch, Close: func() {}}, nil
}

type fakeJobRunner struct {
	getRun    *store.JobRun
	getErr    error
	listErr   error
	listRuns  []store.JobRun
	cancelErr error
}

func (f *fakeJobRunner) List(string, int, int) ([]store.JobRun, error) { return f.listRuns, f.listErr }
func (f *fakeJobRunner) Get(uint) (*store.JobRun, error)               { return f.getRun, f.getErr }
func (f *fakeJobRunner) Cancel(uint) error                             { return f.cancelErr }

func newRunContext(method, target string, body string, params gin.Params) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	var reader *bytes.Reader
	if body == "" {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader([]byte(body))
	}
	req := httptest.NewRequest(method, target, reader)
	req.Header.Set("Content-Type", "application/json")
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = req
	ctx.Params = params
	return ctx, recorder
}

func TestStartWorkflowRun_Accepted(t *testing.T) {
	fake := &fakeWorkflowRunner{}
	s := &Server{workflows: fake}
	c, recorder := newRunContext("POST", "/workflows/summary-workflow/runs", `{"project":"default-workspace","input":{"topic":"go"}}`, gin.Params{{Key: "name", Value: "summary-workflow"}})
	s.startWorkflowRun(c)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d (%s)", recorder.Code, recorder.Body.String())
	}
	if len(fake.started) != 1 || fake.started[0] != "summary-workflow|default-workspace" {
		t.Fatalf("unexpected start call: %v", fake.started)
	}
}

func TestStartWorkflowRun_NotFoundAndInvalidInput(t *testing.T) {
	fake := &fakeWorkflowRunner{startErr: workflow.ErrWorkflowNotFound}
	s := &Server{workflows: fake}
	c, recorder := newRunContext("POST", "/workflows/missing/runs", `{}`, gin.Params{{Key: "name", Value: "missing"}})
	s.startWorkflowRun(c)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown workflow, got %d", recorder.Code)
	}

	fake.startErr = workflow.ErrInvalidInput
	c, recorder = newRunContext("POST", "/workflows/summary-workflow/runs", `{}`, gin.Params{{Key: "name", Value: "summary-workflow"}})
	s.startWorkflowRun(c)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid input, got %d", recorder.Code)
	}
}

func TestStartWorkflowRun_DefaultsProject(t *testing.T) {
	fake := &fakeWorkflowRunner{}
	s := &Server{workflows: fake}
	c, _ := newRunContext("POST", "/workflows/summary-workflow/runs", `{}`, gin.Params{{Key: "name", Value: "summary-workflow"}})
	s.startWorkflowRun(c)
	if fake.started[0] != "summary-workflow|default-workspace" {
		t.Fatalf("expected project to default, got %v", fake.started)
	}
}

func TestGetWorkflowRun_ReturnsRunAndSteps(t *testing.T) {
	fake := &fakeWorkflowRunner{
		getRun:   &store.WorkflowRun{ID: 5, WorkflowName: "summary-workflow", Status: store.WorkflowRunSucceeded},
		getSteps: []store.WorkflowStepRun{{ID: 1, Index: 0, Output: "out", Status: store.WorkflowRunSucceeded}},
	}
	s := &Server{workflows: fake}
	c, recorder := newRunContext("GET", "/workflow-runs/5", "", gin.Params{{Key: "id", Value: "5"}})
	s.getWorkflowRun(c)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `"WorkflowName":"summary-workflow"`) || !strings.Contains(body, `"Output":"out"`) {
		t.Fatalf("unexpected body: %s", body)
	}

	fake.getErr = workflow.ErrRunNotFound
	c, recorder = newRunContext("GET", "/workflow-runs/99", "", gin.Params{{Key: "id", Value: "99"}})
	s.getWorkflowRun(c)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", recorder.Code)
	}
}

func TestCancelWorkflowRun_AcceptedConflictNotFound(t *testing.T) {
	fake := &fakeWorkflowRunner{}
	s := &Server{workflows: fake}
	c, recorder := newRunContext("POST", "/workflow-runs/5/cancel", "", gin.Params{{Key: "id", Value: "5"}})
	s.cancelWorkflowRun(c)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", recorder.Code)
	}

	fake.cancelErr = workflow.ErrRunFinished
	c, recorder = newRunContext("POST", "/workflow-runs/5/cancel", "", gin.Params{{Key: "id", Value: "5"}})
	s.cancelWorkflowRun(c)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("expected 409 for finished run, got %d", recorder.Code)
	}

	fake.cancelErr = workflow.ErrRunNotFound
	c, recorder = newRunContext("POST", "/workflow-runs/99/cancel", "", gin.Params{{Key: "id", Value: "99"}})
	s.cancelWorkflowRun(c)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", recorder.Code)
	}
}

func TestListWorkflowRuns(t *testing.T) {
	fake := &fakeWorkflowRunner{listRuns: []store.WorkflowRun{{ID: 1, WorkflowName: "w", Status: store.WorkflowRunSucceeded}}}
	s := &Server{workflows: fake}
	c, recorder := newRunContext("GET", "/workflow-runs?workflow=w&limit=10", "", nil)
	s.listWorkflowRuns(c)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}
	if body := recorder.Body.String(); !strings.Contains(body, `"WorkflowName":"w"`) {
		t.Fatalf("unexpected body: %s", body)
	}
}

func TestGetJobRun_IncludesWorkflowRuns(t *testing.T) {
	fake := &fakeJobRunner{getRun: &store.JobRun{ID: 3, JobName: "morning-job", Status: store.JobRunSucceeded}}
	wf := &fakeWorkflowRunner{byJobRuns: []store.WorkflowRun{{ID: 7, WorkflowName: "summary-workflow", Status: store.WorkflowRunSucceeded}}}
	s := &Server{jobs: fake, workflows: wf}
	c, recorder := newRunContext("GET", "/job-runs/3", "", gin.Params{{Key: "id", Value: "3"}})
	s.getJobRun(c)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `"JobName":"morning-job"`) || !strings.Contains(body, `"WorkflowName":"summary-workflow"`) {
		t.Fatalf("unexpected body: %s", body)
	}

	fake.getErr = job.ErrJobNotFound
	c, recorder = newRunContext("GET", "/job-runs/99", "", gin.Params{{Key: "id", Value: "99"}})
	s.getJobRun(c)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", recorder.Code)
	}
}

func TestCancelJobRun_AcceptedConflict(t *testing.T) {
	fake := &fakeJobRunner{}
	s := &Server{jobs: fake}
	c, recorder := newRunContext("POST", "/job-runs/3/cancel", "", gin.Params{{Key: "id", Value: "3"}})
	s.cancelJobRun(c)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", recorder.Code)
	}

	fake.cancelErr = job.ErrRunFinished
	c, recorder = newRunContext("POST", "/job-runs/3/cancel", "", gin.Params{{Key: "id", Value: "3"}})
	s.cancelJobRun(c)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", recorder.Code)
	}
}

func TestPagination(t *testing.T) {
	gin.SetMode(gin.TestMode)
	_, recorder := gin.CreateTestContext(httptest.NewRecorder())
	_ = recorder
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("GET", "/workflow-runs?limit=5&offset=2", nil)
	limit, offset := pagination(ctx)
	if limit != 5 || offset != 2 {
		t.Fatalf("expected limit=5 offset=2, got %d/%d", limit, offset)
	}

	ctx2, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx2.Request = httptest.NewRequest("GET", "/workflow-runs", nil)
	limit, offset = pagination(ctx2)
	if limit != 50 || offset != 0 {
		t.Fatalf("expected defaults 50/0, got %d/%d", limit, offset)
	}

	if !errors.Is(workflow.ErrRunNotFound, workflow.ErrRunNotFound) {
		t.Fatal("sentinel sanity check")
	}
}

func TestStreamWorkflowRun_EmitsSnapshotProgressAndDone(t *testing.T) {
	ch := make(chan workflow.ProgressEvent, 8)
	fake := &fakeWorkflowRunner{
		getRun:    &store.WorkflowRun{ID: 5, WorkflowName: "w", Status: store.WorkflowRunRunning},
		getSteps:  []store.WorkflowStepRun{{ID: 1, Index: 0, Status: store.WorkflowRunRunning}},
		subEvents: ch,
	}
	s := &Server{workflows: fake}

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/workflow-runs/5/stream", nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req = req.WithContext(ctx)
	gc, _ := gin.CreateTestContext(recorder)
	gc.Request = req
	gc.Params = gin.Params{{Key: "id", Value: "5"}}

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.streamWorkflowRun(gc)
	}()

	ch <- workflow.ProgressEvent{
		Type:  workflow.ProgressDelta,
		Step:  &store.WorkflowStepRun{ID: 1, Index: 0, Status: store.WorkflowRunRunning},
		Delta: &agent.StreamEvent{Type: "delta", Text: "hi there"},
	}
	ch <- workflow.ProgressEvent{
		Type: workflow.ProgressRun,
		Run:  &store.WorkflowRun{ID: 5, WorkflowName: "w", Status: store.WorkflowRunSucceeded},
	}
	close(ch)
	<-done

	body := recorder.Body.String()
	for _, want := range []string{
		`"WorkflowName":"w"`, // snapshot
		`"type":"delta"`,     // progress event type
		`"text":"hi there"`,  // agent delta
		`"WorkflowName":"w"`, // final run
		"event:done",         // terminal signal
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected stream body to contain %q, got:\n%s", want, body)
		}
	}
	if fake.subCount != 1 {
		t.Fatalf("expected one subscription, got %d", fake.subCount)
	}
}

func TestStreamWorkflowRun_TerminalImmediately(t *testing.T) {
	fake := &fakeWorkflowRunner{
		getRun: &store.WorkflowRun{ID: 6, WorkflowName: "w", Status: store.WorkflowRunSucceeded},
	}
	s := &Server{workflows: fake}

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/workflow-runs/6/stream", nil)
	gc, _ := gin.CreateTestContext(recorder)
	gc.Request = req
	gc.Params = gin.Params{{Key: "id", Value: "6"}}

	s.streamWorkflowRun(gc)

	body := recorder.Body.String()
	if !strings.Contains(body, "event:done") || !strings.Contains(body, `"Status":"succeeded"`) {
		t.Fatalf("expected immediate done for terminal run, got:\n%s", body)
	}
	// The handler always subscribes first (before the snapshot) so no event
	// between snapshot and subscription can be lost; Subscribe itself hands
	// back a closed channel for terminal runs.
	if fake.subCount != 1 {
		t.Fatalf("expected one subscription, got %d", fake.subCount)
	}
}
