package store

import (
	"time"

	"github.com/Cyvadra/hephaestus/internal/registry"
	"gorm.io/datatypes"
)

// JobRunStatus is the lifecycle state of one scheduled Job execution.
type JobRunStatus string

const (
	JobRunPending             JobRunStatus = "pending"
	JobRunRunning             JobRunStatus = "running"
	JobRunSucceeded           JobRunStatus = "succeeded"
	JobRunCompletedWithErrors JobRunStatus = "completed_with_errors"
	JobRunFailed              JobRunStatus = "failed"
	JobRunCancelled           JobRunStatus = "cancelled"
	JobRunInterrupted         JobRunStatus = "interrupted"
)

// IsTerminal reports whether the run has reached a final, non-resumable state.
func (s JobRunStatus) IsTerminal() bool {
	switch s {
	case JobRunSucceeded, JobRunCompletedWithErrors, JobRunFailed, JobRunCancelled, JobRunInterrupted:
		return true
	}
	return false
}

// JobRun is one scheduled execution of a Job, recording the resolved
// definition snapshot so historical logs stay meaningful after config changes.
type JobRun struct {
	ID uint `gorm:"primaryKey;autoIncrement"`

	JobName   string `gorm:"size:255;index"`
	LocalDate string `gorm:"size:10"`
	// Job is the resolved Job definition at run creation.
	Job datatypes.JSONType[registry.Job] `gorm:"type:jsonb"`

	Status JobRunStatus `gorm:"size:32;index"`
	Error  string       `gorm:"type:text"`

	StartedAt  *time.Time
	FinishedAt *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Terminal reports whether the run has finished.
func (r *JobRun) Terminal() bool { return r.Status.IsTerminal() }

// JobState is the per-Job durable scheduler state: the active run, today's
// execution count, and last start/success timestamps used by trigger
// evaluation and transactional claims.
type JobState struct {
	ID uint `gorm:"primaryKey;autoIncrement"`

	JobName string `gorm:"size:255;uniqueIndex"`

	// ActiveRunID is the JobRun currently executing, if any.
	ActiveRunID *uint
	// LocalDate is the host-local calendar date (2006-01-02) that
	// ExecutionsToday applies to.
	LocalDate       string `gorm:"size:10"`
	ExecutionsToday int

	LastStartedAt   *time.Time
	LastSucceededAt *time.Time

	UpdatedAt time.Time
}
