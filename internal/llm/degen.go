package llm

import (
	"compress/flate"
	"fmt"
	"unicode/utf8"
)

// Guard defaults. They bound a *single* streamed channel (assistant content,
// reasoning, or one tool call's arguments), not the whole response.
const (
	defaultMaxCharRun      = 1024
	defaultWindowBytes     = 4096
	defaultGuardMinBytes   = 4096
	defaultRatioPercent    = 3.9
	defaultMaxChannelBytes = 8 << 20
)

// Streamed channels a guard can be attached to. They name the field of
// ds4.Message the guard's offset refers to.
const (
	channelContent       = "content"
	channelReasoning     = "reasoning_content"
	channelToolArguments = "tool_arguments"
)

// Reasons a guard trips, from cheapest check to most expensive.
const (
	reasonCharRun    = "char_run"
	reasonLowEntropy = "low_entropy"
	reasonMaxBytes   = "max_bytes"
)

// GuardConfig bounds runaway model output within a single streaming request.
// The zero value means "enabled, with the package defaults": every field is
// filled in by withDefaults, so a Client built without WithGuard is guarded.
type GuardConfig struct {
	// Disabled turns every check off.
	Disabled bool
	// MaxCharRun trips on a run of this many identical bytes.
	MaxCharRun int
	// WindowBytes is the size of the trailing window the compression check
	// runs over.
	WindowBytes int
	// MinBytes is how much a channel must accumulate before any check arms,
	// so short responses can never trip.
	MinBytes int
	// RatioPercent trips the compression check when the window deflates to
	// below this percentage of its original size. Fractional values are
	// meaningful: the gap between real text and degenerate output is wide,
	// so the useful range sits well below 10.
	RatioPercent float64
	// MaxChannelBytes caps a channel outright, catching runaway output that
	// is not repetitive enough for the other checks.
	MaxChannelBytes int
}

func (c GuardConfig) withDefaults() GuardConfig {
	if c.MaxCharRun <= 0 {
		c.MaxCharRun = defaultMaxCharRun
	}
	if c.WindowBytes <= 0 {
		c.WindowBytes = defaultWindowBytes
	}
	if c.MinBytes <= 0 {
		c.MinBytes = defaultGuardMinBytes
	}
	if c.RatioPercent <= 0 {
		c.RatioPercent = defaultRatioPercent
	}
	if c.MaxChannelBytes <= 0 {
		c.MaxChannelBytes = defaultMaxChannelBytes
	}
	return c
}

// DegenerateOutputError reports that a streamed channel was aborted because
// the model stopped making progress. Offset is the byte index in that channel
// where the degenerate span begins, so callers can keep the clean prefix and
// discard the rest instead of persisting (and later replaying) the garbage.
type DegenerateOutputError struct {
	Channel string
	Reason  string
	Offset  int
	Total   int
	// ToolIndex identifies the tool call when Channel is tool_arguments.
	ToolIndex int
}

func (e *DegenerateOutputError) Error() string {
	return fmt.Sprintf("llm: degenerate output in %s (%s) after %d bytes, keeping first %d",
		e.Channel, e.Reason, e.Total, e.Offset)
}

// repetitionGuard watches one streamed channel for non-progress. Checks run
// cheapest-first on every appended byte: an identical-byte run, then a
// compression ratio over a trailing window (which catches repeated *phrases*
// that a single-byte run misses), then an absolute cap.
//
// The window is deliberately trailing rather than whole-channel: the
// compression ratio of any long text drifts downward with length, so a
// whole-channel threshold would either fire on long legitimate answers or be
// too loose to fire at all. Over a fixed window the threshold stays stable and
// the entire window must be near-pure repetition to trip.
type repetitionGuard struct {
	cfg     GuardConfig
	channel string
	// compress enables the windowed ratio check. It is off for tool call
	// arguments, which are legitimately repetitive JSON and are already
	// bounded downstream by transform.MaxToolExchangeBytes.
	compress  bool
	toolIndex int

	total int

	runByte  byte
	runLen   int
	runStart int

	window     []byte
	windowLen  int
	windowPos  int
	sinceCheck int

	flate   *flate.Writer
	counter countingWriter
}

func newRepetitionGuard(cfg GuardConfig, channel string, compress bool) *repetitionGuard {
	cfg = cfg.withDefaults()
	g := &repetitionGuard{cfg: cfg, channel: channel, compress: compress}
	if !cfg.Disabled && compress {
		g.window = make([]byte, cfg.WindowBytes)
	}
	return g
}

// Append feeds the next delta for this channel and returns a non-nil error the
// moment the channel is judged degenerate. Once it returns an error the guard
// must not be reused.
func (g *repetitionGuard) Append(s string) *DegenerateOutputError {
	if g.cfg.Disabled || s == "" {
		return nil
	}
	for i := 0; i < len(s); i++ {
		b := s[i]
		if g.total > 0 && b == g.runByte {
			g.runLen++
		} else {
			g.runByte = b
			g.runLen = 1
			g.runStart = g.total
		}
		g.total++
		if g.compress {
			g.pushWindow(b)
			g.sinceCheck++
		}

		// Arm only once the channel carries enough text that repetition is
		// meaningful; short answers are never worth aborting.
		if g.total < g.cfg.MinBytes {
			continue
		}
		if g.runLen >= g.cfg.MaxCharRun {
			return g.trip(reasonCharRun, g.runStart)
		}
		if g.total >= g.cfg.MaxChannelBytes {
			return g.trip(reasonMaxBytes, g.cfg.MaxChannelBytes)
		}
		// Re-compressing on every byte would be wasteful; a quarter-window
		// cadence still bounds the overshoot to WindowBytes/4.
		if g.compress && g.windowLen == len(g.window) && g.sinceCheck >= g.cfg.WindowBytes/4 {
			g.sinceCheck = 0
			if g.windowDegenerate() {
				return g.trip(reasonLowEntropy, g.total-g.windowLen)
			}
		}
	}
	return nil
}

func (g *repetitionGuard) trip(reason string, offset int) *DegenerateOutputError {
	if offset < 0 {
		offset = 0
	}
	return &DegenerateOutputError{
		Channel:   g.channel,
		Reason:    reason,
		Offset:    offset,
		Total:     g.total,
		ToolIndex: g.toolIndex,
	}
}

func (g *repetitionGuard) pushWindow(b byte) {
	g.window[g.windowPos] = b
	g.windowPos++
	if g.windowPos == len(g.window) {
		g.windowPos = 0
	}
	if g.windowLen < len(g.window) {
		g.windowLen++
	}
}

// windowDegenerate reports whether the trailing window deflates to below the
// configured ratio. The writer is reused across checks to keep the per-chunk
// cost to a memset rather than an allocation.
func (g *repetitionGuard) windowDegenerate() bool {
	g.counter.n = 0
	if g.flate == nil {
		w, err := flate.NewWriter(&g.counter, flate.BestSpeed)
		if err != nil {
			// Only returned for an invalid level, which is a constant here.
			g.compress = false
			return false
		}
		g.flate = w
	} else {
		g.flate.Reset(&g.counter)
	}
	// The ring is full, so the oldest byte is the one about to be overwritten.
	_, _ = g.flate.Write(g.window[g.windowPos:])
	_, _ = g.flate.Write(g.window[:g.windowPos])
	if err := g.flate.Close(); err != nil {
		return false
	}
	return float64(g.counter.n)*100 < g.cfg.RatioPercent*float64(g.windowLen)
}

// countingWriter discards its input and records only how many bytes it saw.
type countingWriter struct{ n int }

func (w *countingWriter) Write(p []byte) (int, error) {
	w.n += len(p)
	return len(p), nil
}

// truncateChannel cuts s at offset, backing up to the nearest rune boundary so
// an aborted channel never persists as invalid UTF-8.
func truncateChannel(s string, offset int) string {
	if offset <= 0 {
		return ""
	}
	if offset >= len(s) {
		return s
	}
	for offset > 0 && !utf8.RuneStart(s[offset]) {
		offset--
	}
	return s[:offset]
}
