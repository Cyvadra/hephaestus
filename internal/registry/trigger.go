// Job trigger conditions: expr-lang/expr boolean expressions evaluated in
// the host's local timezone.

package registry

import (
	"fmt"
	"strings"
	"time"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
)

// TriggerEnv is the typed environment available to a trigger expression.
type TriggerEnv struct {
	// Now is the current time in the host's local timezone.
	Now time.Time
	// Date is Now's local calendar date as "2006-01-02".
	Date string
	// Hour and Minute describe Now in the host's local timezone.
	Hour   int
	Minute int
	// Weekday is Now's local weekday, 0 = Sunday through 6 = Saturday.
	Weekday int

	// HasMessages reports whether any chat message has been persisted.
	HasMessages bool
	// LastMessageAt is the most recent persisted chat message time.
	LastMessageAt time.Time
	// IdleSeconds is the duration since LastMessageAt; -1 when there are
	// no messages at all.
	IdleSeconds float64

	// ExecutionsToday is how many times this Job has run so far today.
	ExecutionsToday int
	// HasLastStarted reports whether this Job has ever started.
	HasLastStarted bool
	// LastStartedAt is when this Job last started.
	LastStartedAt time.Time
	// HasLastSucceeded reports whether this Job has ever succeeded.
	HasLastSucceeded bool
	// LastSucceededAt is when this Job last succeeded.
	LastSucceededAt time.Time
	// LastSucceededDate is LastSucceededAt's local calendar date as
	// "2006-01-02", or "" when the Job has never succeeded. It exists for
	// the canonical once-per-day dedupe: `LastSucceededDate != Date`.
	LastSucceededDate string
}

// Trigger is a compiled trigger condition.
type Trigger struct {
	program *vm.Program
	src     string
}

// CompileTrigger parses src and type-checks it against TriggerEnv, requiring
// a boolean result. Unknown names and non-boolean expressions are rejected
// here, not at scheduler runtime.
func CompileTrigger(src string) (*Trigger, error) {
	if strings.TrimSpace(src) == "" {
		return nil, fmt.Errorf("trigger: empty trigger expression")
	}
	program, err := expr.Compile(src, expr.Env(TriggerEnv{}), expr.AsBool())
	if err != nil {
		return nil, fmt.Errorf("trigger: compile %q: %w", src, err)
	}
	return &Trigger{program: program, src: src}, nil
}

// Evaluate runs the compiled trigger against env.
func (t *Trigger) Evaluate(env TriggerEnv) (bool, error) {
	out, err := expr.Run(t.program, env)
	if err != nil {
		return false, fmt.Errorf("trigger: evaluate %q: %w", t.src, err)
	}
	result, ok := out.(bool)
	if !ok {
		return false, fmt.Errorf("trigger: %q did not evaluate to a boolean", t.src)
	}
	return result, nil
}
