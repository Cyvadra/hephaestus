package job

import (
	"context"
	"testing"
	"time"

	"github.com/Cyvadra/hephaestus/internal/notify"
	"github.com/Cyvadra/hephaestus/internal/registry"
	"github.com/Cyvadra/hephaestus/internal/store"
	"gorm.io/gorm"
)

func newScheduler(s *services, reg *registry.Registry, clock func() time.Time) *Scheduler {
	return &Scheduler{
		svc:        s.job,
		registries: s.regStore,
		db:         s.db,
		notify:     notify.New(""),
		clock:      clock,
		interval:   func() time.Duration { return time.Hour },
	}
}

func waitJobTerminal(t *testing.T, db *gorm.DB, runID uint) store.JobRun {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var run store.JobRun
		if err := db.First(&run, runID).Error; err != nil {
			t.Fatalf("load run %d: %v", runID, err)
		}
		switch run.Status {
		case store.JobRunSucceeded, store.JobRunCompletedWithErrors, store.JobRunFailed, store.JobRunCancelled, store.JobRunInterrupted:
			return run
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("run %d did not reach a terminal status", runID)
	return store.JobRun{}
}

func TestIntegration_SchedulerRunsEligibleJob(t *testing.T) {
	jobName := uniqueJobName("morning")
	reg := baseRegistry()
	reg.Jobs[jobName] = morningJob(jobName, 1)
	s := newServices(t, reg, &fakeRunner{replies: []string{"ok"}})
	now := time.Date(2026, 8, 11, 9, 0, 0, 0, time.Local)
	scheduler := newScheduler(s, reg, func() time.Time { return now })

	scheduler.evaluate(context.Background())

	var runs []store.JobRun
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := s.db.Where("job_name = ?", jobName).Find(&runs).Error; err != nil {
			t.Fatalf("load runs: %v", err)
		}
		if len(runs) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(runs) != 1 {
		t.Fatalf("expected one job run created, got %d", len(runs))
	}
	finished := waitJobTerminal(t, s.db, runs[0].ID)
	if finished.Status != store.JobRunSucceeded {
		t.Fatalf("expected succeeded run, got %q (%s)", finished.Status, finished.Error)
	}
}

func TestIntegration_SchedulerSkipsIneligibleJob(t *testing.T) {
	jobName := uniqueJobName("morning")
	reg := baseRegistry()
	job := morningJob(jobName, 1)
	job.Trigger = "false"
	reg.Jobs[jobName] = job
	s := newServices(t, reg, &fakeRunner{})
	scheduler := newScheduler(s, reg, time.Now)

	scheduler.evaluate(context.Background())

	var count int64
	if err := s.db.Model(&store.JobRun{}).Where("job_name = ?", jobName).Count(&count).Error; err != nil {
		t.Fatalf("count runs: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no run for false trigger, got %d", count)
	}
}

func TestIntegration_SchedulerEnvReflectsState(t *testing.T) {
	jobName := uniqueJobName("morning")
	reg := baseRegistry()
	reg.Jobs[jobName] = morningJob(jobName, 1)
	s := newServices(t, reg, &fakeRunner{replies: []string{"ok"}})
	now := time.Now()

	started := now.Add(-2 * time.Hour).Truncate(time.Microsecond)
	succeeded := now.Add(-1 * time.Hour).Truncate(time.Microsecond)
	state := store.JobState{
		JobName: jobName, LocalDate: now.Format("2006-01-02"), ExecutionsToday: 3,
		LastStartedAt: &started, LastSucceededAt: &succeeded,
	}
	if err := s.db.Create(&state).Error; err != nil {
		t.Fatalf("create state: %v", err)
	}

	env, err := newScheduler(s, reg, func() time.Time { return now }).buildEnv(jobName, now)
	if err != nil {
		t.Fatalf("buildEnv: %v", err)
	}
	// The real clock is used so idle time against the latest persisted
	// message is non-negative.
	if env.Date != now.Format("2006-01-02") || env.Hour != now.Hour() || env.IdleSeconds < 0 {
		t.Fatalf("unexpected base env: %+v", env)
	}
	if env.ExecutionsToday != 3 || !env.HasLastStarted || !env.HasLastSucceeded {
		t.Fatalf("expected persisted state reflected, got %+v", env)
	}
	if !env.LastSucceededAt.Equal(succeeded) {
		t.Fatalf("expected last succeeded %v, got %v", succeeded, env.LastSucceededAt)
	}
	if env.LastSucceededDate != succeeded.Format("2006-01-02") {
		t.Fatalf("expected last succeeded date %q, got %q", succeeded.Format("2006-01-02"), env.LastSucceededDate)
	}
}

// TestIntegration_SchedulerEnvResetsExecutionsOnNewDay guards against the
// stale-counter deadlock: yesterday's ExecutionsToday must read as zero, or
// triggers like `ExecutionsToday == 0` never fire again.
func TestIntegration_SchedulerEnvResetsExecutionsOnNewDay(t *testing.T) {
	jobName := uniqueJobName("morning")
	reg := baseRegistry()
	reg.Jobs[jobName] = morningJob(jobName, 1)
	s := newServices(t, reg, &fakeRunner{})
	now := time.Now()

	state := store.JobState{
		JobName:         jobName,
		LocalDate:       now.AddDate(0, 0, -1).Format("2006-01-02"),
		ExecutionsToday: 5,
	}
	if err := s.db.Create(&state).Error; err != nil {
		t.Fatalf("create state: %v", err)
	}

	env, err := newScheduler(s, reg, func() time.Time { return now }).buildEnv(jobName, now)
	if err != nil {
		t.Fatalf("buildEnv: %v", err)
	}
	if env.ExecutionsToday != 0 {
		t.Fatalf("expected stale counter to read as zero on a new day, got %d", env.ExecutionsToday)
	}
}
