package job

import (
	"context"
	"errors"
	"math/rand"
	"time"

	"github.com/Cyvadra/hephaestus/internal/notify"
	"github.com/Cyvadra/hephaestus/internal/registry"
	"github.com/Cyvadra/hephaestus/internal/store"
	"gorm.io/gorm"
)

// Scheduler evaluates Job trigger conditions on a randomized 40-80 minute
// interval. It follows the design doc: an initial pass at startup, then
// polling. Because a trigger is evaluated only at each poll, triggers must
// express broad eligibility conditions (idle time, time-of-day windows, daily
// caps) rather than exact-minute times: the polling granularity can skip an
// exact minute entirely.
type Scheduler struct {
	svc        *Service
	registries *registry.Store
	db         *gorm.DB
	notify     *notify.Notifier
	clock      func() time.Time
	interval   func() time.Duration

	// triggers caches compiled trigger programs by expression source, since
	// hot-published registries may change job definitions between polls.
	triggers map[string]*registry.Trigger
}

// NewScheduler wires the scheduler to its dependencies.
func NewScheduler(svc *Service, registries *registry.Store, db *gorm.DB, notify *notify.Notifier) *Scheduler {
	return &Scheduler{
		svc:        svc,
		registries: registries,
		db:         db,
		notify:     notify,
		clock:      time.Now,
		interval:   randomInterval,
		triggers:   map[string]*registry.Trigger{},
	}
}

func randomInterval() time.Duration {
	const (
		min = 40 * time.Minute
		max = 80 * time.Minute
	)
	return min + time.Duration(rand.Int63n(int64(max-min)))
}

// Run evaluates all jobs once, then polls on the randomized interval until
// ctx is canceled.
func (s *Scheduler) Run(ctx context.Context) {
	s.evaluate(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.interval()):
			s.evaluate(ctx)
		}
	}
}

// evaluate checks every job's trigger sequentially (a handful of jobs and a
// few DB reads each; parallelism buys nothing here) and detaches execution
// of the ones it claims.
func (s *Scheduler) evaluate(ctx context.Context) {
	reg := s.registries.Current()
	now := s.clock()
	for jobName, job := range reg.Jobs {
		trigger, err := s.compileTrigger(job.Trigger)
		if err != nil {
			s.notify.Error("job: %q trigger compile: %v", jobName, err)
			continue
		}
		env, err := s.buildEnv(jobName, now)
		if err != nil {
			s.notify.Error("job: %q trigger env: %v", jobName, err)
			continue
		}
		ok, err := trigger.Evaluate(env)
		if err != nil {
			s.notify.Error("job: %q trigger evaluate: %v", jobName, err)
			continue
		}
		if !ok {
			continue
		}
		run, claimed, err := s.svc.claim(ctx, reg, jobName, now)
		if err != nil {
			s.notify.Error("job: %q claim: %v", jobName, err)
			continue
		}
		if claimed {
			go s.svc.executeJob(ctx, reg, run)
		}
	}
}

func (s *Scheduler) compileTrigger(src string) (*registry.Trigger, error) {
	if trigger, ok := s.triggers[src]; ok {
		return trigger, nil
	}
	trigger, err := registry.CompileTrigger(src)
	if err != nil {
		return nil, err
	}
	if s.triggers == nil {
		s.triggers = map[string]*registry.Trigger{}
	}
	s.triggers[src] = trigger
	return trigger, nil
}

// buildEnv assembles the trigger environment from the host-local clock, the
// latest persisted chat message, and the job's durable state.
func (s *Scheduler) buildEnv(jobName string, now time.Time) (registry.TriggerEnv, error) {
	localDate := now.Format("2006-01-02")
	env := registry.TriggerEnv{
		Now:     now,
		Date:    localDate,
		Hour:    now.Hour(),
		Minute:  now.Minute(),
		Weekday: int(now.Weekday()),
	}

	var last store.ChatMessage
	switch err := s.db.Order("timestamp DESC, id DESC").First(&last).Error; {
	case err == nil:
		env.HasMessages = true
		env.LastMessageAt = last.Timestamp
		env.IdleSeconds = now.Sub(last.Timestamp).Seconds()
	case errors.Is(err, gorm.ErrRecordNotFound):
		env.IdleSeconds = -1
	default:
		return env, err
	}

	var state store.JobState
	if err := s.db.Where("job_name = ?", jobName).First(&state).Error; err == nil {
		// The persisted counter belongs to state.LocalDate; on a new day it
		// is stale and must read as zero, or triggers referencing
		// ExecutionsToday would never fire again.
		if state.LocalDate == localDate {
			env.ExecutionsToday = state.ExecutionsToday
		}
		env.HasLastStarted = state.LastStartedAt != nil
		if state.LastStartedAt != nil {
			env.LastStartedAt = *state.LastStartedAt
		}
		env.HasLastSucceeded = state.LastSucceededAt != nil
		if state.LastSucceededAt != nil {
			env.LastSucceededAt = *state.LastSucceededAt
			env.LastSucceededDate = state.LastSucceededAt.Format("2006-01-02")
		}
	}
	return env, nil
}
