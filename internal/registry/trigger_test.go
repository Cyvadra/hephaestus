package registry

import (
	"testing"
	"time"
)

func TestCompileTrigger_RejectsEmpty(t *testing.T) {
	if _, err := CompileTrigger("   "); err == nil {
		t.Fatal("expected error for empty trigger")
	}
}

func TestCompileTrigger_RejectsNonBoolean(t *testing.T) {
	if _, err := CompileTrigger(`Hour + 1`); err == nil {
		t.Fatal("expected error for non-boolean trigger")
	}
}

func TestTriggerEvaluate_TimeWindow(t *testing.T) {
	tr, err := CompileTrigger(`Hour >= 9 && Hour < 12`)
	if err != nil {
		t.Fatalf("CompileTrigger: %v", err)
	}
	base := TriggerEnv{Now: time.Date(2026, 8, 11, 0, 0, 0, 0, time.Local), Hour: 9, Minute: 30}
	ok, err := tr.Evaluate(base)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !ok {
		t.Fatal("expected 9am to be inside window")
	}
	base.Hour = 14
	ok, err = tr.Evaluate(base)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if ok {
		t.Fatal("expected 2pm to be outside window")
	}
}

func TestTriggerEvaluate_PersistentTrue(t *testing.T) {
	tr, err := CompileTrigger(`true`)
	if err != nil {
		t.Fatalf("CompileTrigger: %v", err)
	}
	ok, err := tr.Evaluate(TriggerEnv{})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !ok {
		t.Fatal("expected constant true trigger to fire")
	}
}

func TestTriggerEvaluate_IdleAndExecutionWindow(t *testing.T) {
	tr, err := CompileTrigger(`IdleSeconds > 3600 && ExecutionsToday == 0`)
	if err != nil {
		t.Fatalf("CompileTrigger: %v", err)
	}
	ok, err := tr.Evaluate(TriggerEnv{IdleSeconds: 7200, ExecutionsToday: 0})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !ok {
		t.Fatal("expected idle, no-run-today to fire")
	}
	ok, err = tr.Evaluate(TriggerEnv{IdleSeconds: 7200, ExecutionsToday: 1})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if ok {
		t.Fatal("expected already-run-today to stay quiet")
	}
}

func TestTriggerEvaluate_HostLocalFields(t *testing.T) {
	now := time.Date(2026, 8, 11, 7, 0, 0, 0, time.Local)
	tr, err := CompileTrigger(`Date == "2026-08-11" && Weekday == 2 && Minute == 0`)
	if err != nil {
		t.Fatalf("CompileTrigger: %v", err)
	}
	ok, err := tr.Evaluate(TriggerEnv{Now: now, Date: now.Format("2006-01-02"), Weekday: int(now.Weekday()), Minute: now.Minute()})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !ok {
		t.Fatal("expected host-local date/weekday/minute to match")
	}
}

// TestTriggerEvaluate_OncePerDayDedupe guards the canonical dedupe pattern
// documented for daily jobs.
func TestTriggerEvaluate_OncePerDayDedupe(t *testing.T) {
	tr, err := CompileTrigger(`Hour >= 9 && LastSucceededDate != Date`)
	if err != nil {
		t.Fatalf("CompileTrigger: %v", err)
	}
	env := TriggerEnv{Date: "2026-08-12", Hour: 10}
	if ok, _ := tr.Evaluate(env); !ok {
		t.Fatal("expected never-succeeded job to fire")
	}
	env.LastSucceededDate = "2026-08-11"
	if ok, _ := tr.Evaluate(env); !ok {
		t.Fatal("expected yesterday's success to allow today's run")
	}
	env.LastSucceededDate = "2026-08-12"
	if ok, _ := tr.Evaluate(env); ok {
		t.Fatal("expected today's success to suppress a second run")
	}
}
