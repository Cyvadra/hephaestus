package steering

import "testing"

func TestManagerReplacesThenClaimsOneInstruction(t *testing.T) {
	manager := NewManager()
	if outcome, err := manager.Put(11, 7, "first", ModeNormal); err != nil || outcome != OutcomeQueued {
		t.Fatalf("first Put = %q, %v", outcome, err)
	}
	if outcome, err := manager.Put(11, 7, "second", ModeAggressive); err != nil || outcome != OutcomeReplaced {
		t.Fatalf("second Put = %q, %v", outcome, err)
	}

	lease, ok := manager.Claim(11)
	if !ok {
		t.Fatal("expected claimed steering")
	}
	if lease.Text != "second" || lease.Mode != ModeAggressive {
		t.Fatalf("claimed %#v, want replacement", lease)
	}
	if _, ok := manager.Claim(11); ok {
		t.Fatal("claimed steering must not be claimed twice")
	}
	if manager.Cancel(11) {
		t.Fatal("claimed steering must not be cancellable")
	}
	if !manager.Acknowledge(lease) {
		t.Fatal("expected acknowledgement")
	}
	if _, ok := manager.Finish(11); ok {
		t.Fatal("acknowledged steering must not fall back")
	}
}

func TestManagerReleaseAndFinishPreserveUnconsumedSteering(t *testing.T) {
	manager := NewManager()
	if _, err := manager.Put(12, 8, "redirect", ModeNormal); err != nil {
		t.Fatalf("Put: %v", err)
	}
	lease, ok := manager.Claim(12)
	if !ok {
		t.Fatal("expected claim")
	}
	if !manager.Release(lease) {
		t.Fatal("expected release")
	}
	pending, ok := manager.Finish(12)
	if !ok || pending.Text != "redirect" || pending.Mode != ModeNormal {
		t.Fatalf("Finish = %#v, %v", pending, ok)
	}
}

func TestManagerQueuesReplacementWhilePreviousInstructionIsClaimed(t *testing.T) {
	manager := NewManager()
	if _, err := manager.Put(13, 8, "first", ModeNormal); err != nil {
		t.Fatalf("Put first: %v", err)
	}
	lease, ok := manager.Claim(13)
	if !ok {
		t.Fatal("expected first instruction to be claimed")
	}
	if outcome, err := manager.Put(13, 8, "latest", ModeAggressive); err != nil || outcome != OutcomeQueued {
		t.Fatalf("Put replacement = %q, %v", outcome, err)
	}
	if !manager.Acknowledge(lease) {
		t.Fatal("expected first lease acknowledgement")
	}
	next, ok := manager.Claim(13)
	if !ok || next.Text != "latest" || next.Mode != ModeAggressive {
		t.Fatalf("next claim = %#v, %v", next, ok)
	}
}

func TestManagerClaimNormalLeavesAggressiveInstructionPending(t *testing.T) {
	manager := NewManager()
	if _, err := manager.Put(14, 8, "skip", ModeAggressive); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, ok := manager.ClaimNormal(14); ok {
		t.Fatal("normal claim must not consume aggressive steering")
	}
	lease, ok := manager.Claim(14)
	if !ok || lease.Mode != ModeAggressive {
		t.Fatalf("Claim = %#v, %v", lease, ok)
	}
}

func TestManagerSeparatesRuns(t *testing.T) {
	manager := NewManager()
	if _, err := manager.Put(1, 9, "one", ModeNormal); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Put(2, 9, "two", ModeAggressive); err != nil {
		t.Fatal(err)
	}
	lease, ok := manager.Claim(1)
	if !ok || lease.Text != "one" {
		t.Fatalf("run 1 claim = %#v, %v", lease, ok)
	}
	pending, ok := manager.Get(2)
	if !ok || pending.Text != "two" {
		t.Fatalf("run 2 pending = %#v, %v", pending, ok)
	}
}
