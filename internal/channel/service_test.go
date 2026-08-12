package channel

import "testing"

func TestIsApproval(t *testing.T) {
	for _, input := range []string{"确认", "好的，确认执行", "YES", "yes please", "y", "reply: y", "1"} {
		if !isApproval(input) {
			t.Errorf("isApproval(%q) = false", input)
		}
	}
	for _, input := range []string{"no", "deny", "maybe", "10", ""} {
		if isApproval(input) {
			t.Errorf("isApproval(%q) = true", input)
		}
	}
}

func TestReplacementSessionID(t *testing.T) {
	got, ok := replacementSessionID("Archived session 4. New session: 17 (from concierge default).")
	if !ok || got != 17 {
		t.Fatalf("replacementSessionID() = %d, %v", got, ok)
	}
	if _, ok := replacementSessionID("no replacement"); ok {
		t.Fatal("unexpected replacement session")
	}
}

func TestChannelTurnOptionsPreservesExpectedLeaf(t *testing.T) {
	leaf := uint(42)
	options := channelTurnOptions(&leaf, nil)
	if options.ExpectedLeaf == nil || *options.ExpectedLeaf != leaf {
		t.Fatalf("ExpectedLeaf = %v, want %d", options.ExpectedLeaf, leaf)
	}
}
