package llm

import (
	"encoding/base64"
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

// feed pushes s through a guard in small chunks, mimicking how deltas arrive.
func feed(g *repetitionGuard, s string) *DegenerateOutputError {
	const chunk = 37
	for i := 0; i < len(s); i += chunk {
		end := i + chunk
		if end > len(s) {
			end = len(s)
		}
		if d := g.Append(s[i:end]); d != nil {
			return d
		}
	}
	return nil
}

// mustTrip asserts the guard tripped for the expected reason and returns the
// non-nil report, so callers can inspect the offset without a nil check.
func mustTrip(t *testing.T, degen *DegenerateOutputError, want string) *DegenerateOutputError {
	t.Helper()
	if degen == nil {
		t.Fatalf("expected the guard to trip with reason %q", want)
		return nil
	}
	if degen.Reason != want {
		t.Fatalf("reason = %q, want %q", degen.Reason, want)
		return nil
	}
	return degen
}

func TestGuardTripsOnSingleCharacterRun(t *testing.T) {
	g := newRepetitionGuard(GuardConfig{}, channelReasoning, true)
	prefix := "Here is the answer. "
	degen := mustTrip(t, feed(g, prefix+strings.Repeat("0", 8192)), reasonCharRun)
	if degen.Offset != len(prefix) {
		t.Fatalf("offset = %d, want the run start %d", degen.Offset, len(prefix))
	}
}

func TestGuardTripsOnSession758Reasoning(t *testing.T) {
	// The shape that caused the incident: a little real reasoning, then an
	// unbounded run of hex zeros.
	prefix := "Hmm, the raw was 6 words:\nword0 = 0x0000...0000\n\nWait, let me re-read:\n\n"
	g := newRepetitionGuard(GuardConfig{}, channelReasoning, true)
	degen := mustTrip(t, feed(g, prefix+strings.Repeat("0", 32*1024)), reasonCharRun)
	kept := truncateChannel(prefix+strings.Repeat("0", 32*1024), degen.Offset)
	if !strings.HasPrefix(kept, "Hmm, the raw was 6 words:") {
		t.Fatalf("truncation lost the clean prefix: %q", kept)
	}
	if len(kept) > len(prefix)+defaultMaxCharRun {
		t.Fatalf("kept %d bytes, want the zeros dropped (prefix is %d)", len(kept), len(prefix))
	}
}

func TestGuardTripsOnRepeatedPhraseNotSingleChar(t *testing.T) {
	// No long single-character run here, so only the compression check can
	// catch it.
	g := newRepetitionGuard(GuardConfig{}, channelContent, true)
	_ = mustTrip(t, feed(g, strings.Repeat("word0 = 0x00\n", 1024)), reasonLowEntropy)
}

func TestGuardIgnoresShortRepetition(t *testing.T) {
	g := newRepetitionGuard(GuardConfig{}, channelContent, true)
	if degen := feed(g, strings.Repeat("0", 2048)); degen != nil {
		t.Fatalf("2KB is below MinBytes, want no trip, got %v", degen)
	}
}

// TestGuardAllowsLegitimateLongOutput is the false-positive check. Measured
// worst-case 4KB-window deflate ratios across this repo: Go/TS source 28-31%,
// Markdown 50-64%, go.sum 47%. Degenerate output sits below 4% (see
// TestGuardAllowsLegitimateLongOutput is the false-positive check. Measured
// worst-case 4KB-window deflate ratios across this repo: Go/TS source 28-31%,
// Markdown 50-64%, go.sum 47%. Degenerate output sits below 4% (see
// TestGuardRatioAcceptsFractionalThreshold for exact figures). The 3.9%
// default therefore has an ~7x margin below anything real.
func TestGuardAllowsLegitimateLongOutput(t *testing.T) {
	var corpus strings.Builder
	for _, name := range []string{"client.go", "degen.go", "client_test.go", "../../README.md", "../../go.sum"} {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		corpus.Write(body)
	}
	if corpus.Len() < 64*1024 {
		t.Fatalf("test corpus is only %d bytes", corpus.Len())
	}
	g := newRepetitionGuard(GuardConfig{}, channelContent, true)
	if degen := feed(g, corpus.String()); degen != nil {
		t.Fatalf("real mixed source and prose tripped the guard: %v", degen)
	}
}

func TestGuardAllowsBase64Payload(t *testing.T) {
	raw := make([]byte, 96*1024)
	for i := range raw {
		raw[i] = byte(i*7 + i/251)
	}
	g := newRepetitionGuard(GuardConfig{}, channelContent, true)
	if degen := feed(g, base64.StdEncoding.EncodeToString(raw)); degen != nil {
		t.Fatalf("base64 payload tripped the guard: %v", degen)
	}
}

func TestGuardCapsNonRepetitiveRunaway(t *testing.T) {
	raw := make([]byte, 64*1024)
	for i := range raw {
		raw[i] = byte(i*31 + i/97)
	}
	noise := base64.StdEncoding.EncodeToString(raw)
	g := newRepetitionGuard(GuardConfig{MaxChannelBytes: 32 * 1024}, channelContent, true)
	var degen *DegenerateOutputError
	for degen == nil {
		degen = feed(g, noise)
	}
	_ = mustTrip(t, degen, reasonMaxBytes)
}

func TestGuardDisabledNeverTrips(t *testing.T) {
	g := newRepetitionGuard(GuardConfig{Disabled: true}, channelContent, true)
	if degen := feed(g, strings.Repeat("0", 256*1024)); degen != nil {
		t.Fatalf("disabled guard tripped: %v", degen)
	}
}

func TestTruncateChannelKeepsValidUTF8(t *testing.T) {
	s := "héllo" // 'é' occupies bytes 1 and 2
	if got := truncateChannel(s, 2); got != "h" {
		t.Fatalf("truncateChannel = %q, want %q", got, "h")
	}
	if got := truncateChannel(s, 99); got != s {
		t.Fatalf("over-long offset = %q, want the whole string", got)
	}
	for offset := 0; offset <= len(s); offset++ {
		if !utf8.ValidString(truncateChannel(s, offset)) {
			t.Fatalf("offset %d produced invalid UTF-8", offset)
		}
	}
}

// TestGuardRatioAcceptsFractionalThreshold pins the fractional threshold and
// the boundary it sits on. Measured 4KB-window deflate ratios: a repeated
// character is 0.61%, a repeated phrase 1.53%, one repeated sentence 3.56%.
// The 3.9% default clears all three while staying ~7x below real text (which
// bottoms out at 28%). The gap between sentence looping and the threshold is
// only 0.34pp, so RatioPercent must stay a float: rounding it to an integer
// would either drop sentence looping (3) or halve the margin (4).
func TestGuardRatioAcceptsFractionalThreshold(t *testing.T) {
	phrase := strings.Repeat("word0 = 0x00\n", 1024) // ~1.53%
	sentence := strings.Repeat("The quick brown fox jumps over the lazy dog, and then considers "+
		"whether the arrangement of these words carries any meaning at all. ", 700) // ~3.56%

	// The default catches every degeneration shape, sentence looping included.
	for name, corpus := range map[string]string{"phrase": phrase, "sentence": sentence} {
		g := newRepetitionGuard(GuardConfig{RatioPercent: defaultRatioPercent}, channelContent, true)
		if degen := feed(g, corpus); degen == nil {
			t.Fatalf("%s looping did not trip the %.1f%% default", name, defaultRatioPercent)
		} else if degen.Reason != reasonLowEntropy {
			t.Fatalf("%s: reason = %q, want %q", name, degen.Reason, reasonLowEntropy)
		}
	}

	// Just below the sentence ratio it must not trip, proving the fractional
	// value is honoured rather than truncated to an int.
	g := newRepetitionGuard(GuardConfig{RatioPercent: 3.5}, channelContent, true)
	if degen := feed(g, sentence); degen != nil {
		t.Fatalf("a 3.5%% threshold should not trip on ~3.56%% content: %v", degen)
	}
}
