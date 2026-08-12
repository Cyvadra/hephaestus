package store

import (
	"time"

	"github.com/Cyvadra/hephaestus/internal/registry"
	"gorm.io/datatypes"
)

// WorkflowRunStatus is the lifecycle state of one workflow execution.
type WorkflowRunStatus string

const (
	WorkflowRunPending     WorkflowRunStatus = "pending"
	WorkflowRunRunning     WorkflowRunStatus = "running"
	WorkflowRunSucceeded   WorkflowRunStatus = "succeeded"
	WorkflowRunFailed      WorkflowRunStatus = "failed"
	WorkflowRunFatal       WorkflowRunStatus = "fatal"
	WorkflowRunCancelled   WorkflowRunStatus = "cancelled"
	WorkflowRunInterrupted WorkflowRunStatus = "interrupted"
)

// IsTerminal reports whether the run has reached a final, non-resumable state.
func (s WorkflowRunStatus) IsTerminal() bool {
	switch s {
	case WorkflowRunSucceeded, WorkflowRunFailed, WorkflowRunFatal, WorkflowRunCancelled, WorkflowRunInterrupted:
		return true
	}
	return false
}

// WorkflowStepRunStatus is the lifecycle state of one workflow step run; it
// shares the WorkflowRunStatus value set.
type WorkflowStepRunStatus = WorkflowRunStatus

// WorkflowRun is one durable execution of a Workflow, recording the resolved
// definition snapshot so historical logs stay meaningful after config changes.
type WorkflowRun struct {
	ID uint `gorm:"primaryKey;autoIncrement"`

	// JobRunID links a scheduler-triggered run; nil for manually started
	// runs. JobName and BindingIndex identify the owning binding.
	JobRunID     *uint  `gorm:"index"`
	JobName      string `gorm:"size:255"`
	BindingIndex int

	WorkflowName string `gorm:"size:255;index"`
	Concierge    string `gorm:"size:255"`
	ProjectName  string `gorm:"size:255"`
	// Workflow is the resolved Workflow definition at run creation.
	Workflow datatypes.JSONType[registry.Workflow] `gorm:"type:jsonb"`

	Input  datatypes.JSON `gorm:"type:jsonb"`
	Output datatypes.JSON `gorm:"type:jsonb"`

	// Attempt is the 1-based retry attempt this run represents.
	Attempt int
	Status  WorkflowRunStatus `gorm:"size:32;index"`
	Error   string            `gorm:"type:text"`

	StartedAt  *time.Time
	FinishedAt *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Terminal reports whether the run has finished.
func (r *WorkflowRun) Terminal() bool { return r.Status.IsTerminal() }

// NewWorkflowRun builds the pending run row shared by manual starts and
// scheduled job bindings, snapshotting the resolved Workflow definition.
func NewWorkflowRun(wf registry.Workflow, projectName string, input datatypes.JSON, attempt int) *WorkflowRun {
	return &WorkflowRun{
		WorkflowName: wf.Name,
		Concierge:    wf.Concierge,
		ProjectName:  projectName,
		Workflow:     datatypes.NewJSONType(wf),
		Input:        input,
		Attempt:      attempt,
		Status:       WorkflowRunPending,
	}
}

// WorkflowStepRun is one durable execution of a single Workflow step,
// recording its agent-run transcript and final output.
type WorkflowStepRun struct {
	ID uint `gorm:"primaryKey;autoIncrement"`

	WorkflowRunID uint `gorm:"index"`
	Index         int
	Text          string `gorm:"type:text"`
	// Transcript is the step's agent-run messages as JSON.
	Transcript datatypes.JSON `gorm:"type:jsonb"`
	Output     string         `gorm:"type:text"`

	Status     WorkflowStepRunStatus `gorm:"size:32"`
	Error      string                `gorm:"type:text"`
	StartedAt  *time.Time
	FinishedAt *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}
