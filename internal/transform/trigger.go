package transform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Cyvadra/hephaestus/internal/llm"
	"github.com/Cyvadra/hephaestus/internal/registry"
)

// ErrTriggerDescriptionEmpty reports a trigger request with no description.
var ErrTriggerDescriptionEmpty = errors.New("transform: trigger: description is empty")

// ErrTriggerRejected reports that every model answer failed parsing or
// compilation; the wrapped error carries the last failure.
var ErrTriggerRejected = errors.New("transform: trigger: model output rejected")

// TriggerSuggestion is a generated, compile-checked Job trigger expression.
type TriggerSuggestion struct {
	Expression  string `json:"expression"`
	Explanation string `json:"explanation"`
}

const triggerMaxTokens = 1024

// triggerSystemPrompt turns a natural-language schedule into an expr-lang
// boolean expression over registry.TriggerEnv.
const triggerSystemPrompt = `You convert a user's desired run schedule for a background Job into a trigger condition written in the expr-lang/expr language.

How the scheduler works:
- About once per minute it evaluates the expression. When it is true, the Job starts (never two runs of the same Job at once).
- So the expression is a CONDITION, not a cron. A bare time window such as "Hour == 9" would fire on every tick in that hour. Always add a dedupe guard unless the user explicitly asks for continuous/repeated runs.
- All times are in the server's local timezone.

Available variables (case-sensitive; nothing else exists, no custom functions):
- Now time.Time - current local time
- Date string - today as "2006-01-02"
- Hour int (0-23), Minute int (0-59)
- Weekday int - 0=Sunday, 1=Monday ... 6=Saturday
- HasMessages bool - any chat message exists
- LastMessageAt time.Time - most recent chat message
- IdleSeconds float - seconds since LastMessageAt; -1 when there are no messages
- ExecutionsToday int - runs of this Job today (started today)
- HasLastStarted bool, LastStartedAt time.Time
- HasLastSucceeded bool, LastSucceededAt time.Time
- LastSucceededDate string - date of last success as "2006-01-02", "" if never

expr syntax reminders: && || ! == != < <= > >= in, e.g. "Weekday in [1,3,5]". Durations: "Now - LastStartedAt > duration(\"2h\")", "Now.Sub(LastStartedAt).Hours() >= 2". Do not use cron syntax.

Dedupe patterns:
- once per day: "ExecutionsToday == 0" (or "LastSucceededDate != Date" when the user wants retries until it succeeds)
- N times per day at given hours: compare ExecutionsToday with how many slots have passed, e.g. at 9 and 18: "(Hour >= 9 && ExecutionsToday == 0) || (Hour >= 18 && ExecutionsToday < 2)"
- every N hours/minutes: "!HasLastStarted || Now - LastStartedAt >= duration(\"3h\")"
- use ">=" for the start hour (not "==") so a run missed at the exact minute still happens later that day, unless the user restricts to a window.

Examples:
- "每天早上9点" -> Hour >= 9 && ExecutionsToday == 0
- "工作日早上9点" -> Weekday >= 1 && Weekday <= 5 && Hour >= 9 && ExecutionsToday == 0
- "每周一上午10点" -> Weekday == 1 && Hour >= 10 && ExecutionsToday == 0
- "every day at 8:30pm" -> (Hour > 20 || (Hour == 20 && Minute >= 30)) && ExecutionsToday == 0
- "工作时间每小时一次" -> Weekday >= 1 && Weekday <= 5 && Hour >= 9 && Hour < 18 && (!HasLastStarted || Now - LastStartedAt >= duration("1h"))
- "用户空闲30分钟后整理一次" -> HasMessages && IdleSeconds >= 1800 && (!HasLastStarted || LastStartedAt < LastMessageAt)
- "每天一次，失败了要重试，至少间隔一小时" -> LastSucceededDate != Date && (!HasLastStarted || Now - LastStartedAt >= duration("1h"))
- "每月1号" -> Now.Day() == 1 && ExecutionsToday == 0

If a current expression is given, treat the request as a modification of it when that makes sense.

Respond with ONLY a JSON object, no code fences:
{"expression": "<expr>", "explanation": "<one short sentence, in the same language as the Desired schedule text, describing when it runs>"}`

// GenerateTrigger asks the model to translate description into a trigger
// expression and verifies it compiles against registry.TriggerEnv, feeding a
// compile error back to the model once before giving up.
func GenerateTrigger(ctx context.Context, client *llm.Client, description, current string) (TriggerSuggestion, error) {
	description = strings.TrimSpace(description)
	if description == "" {
		return TriggerSuggestion{}, ErrTriggerDescriptionEmpty
	}
	base := triggerPrompt(description, current, time.Now())
	prompt := base
	var lastErr error
	for range 2 {
		content, err := client.RawCall(ctx, triggerSystemPrompt, prompt, triggerMaxTokens)
		if err != nil {
			return TriggerSuggestion{}, fmt.Errorf("transform: trigger: llm call: %w", err)
		}
		suggestion, err := validateTriggerSuggestion(content)
		if err == nil {
			return suggestion, nil
		}
		lastErr = err
		prompt = base +
			"\n\nYour previous answer was rejected:\n" + strings.TrimSpace(content) +
			"\nError: " + err.Error() + "\nFix it and answer again with only the JSON object."
	}
	return TriggerSuggestion{}, fmt.Errorf("%w: %w", ErrTriggerRejected, lastErr)
}

// validateTriggerSuggestion parses a model response and checks that its
// expression compiles against registry.TriggerEnv.
func validateTriggerSuggestion(content string) (TriggerSuggestion, error) {
	suggestion, err := parseTriggerSuggestion(content)
	if err != nil {
		return TriggerSuggestion{}, err
	}
	if _, err := registry.CompileTrigger(suggestion.Expression); err != nil {
		return TriggerSuggestion{}, err
	}
	return suggestion, nil
}

func triggerPrompt(description, current string, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Current local time: %s (Weekday=%d)\n", now.Format("2006-01-02 15:04 Monday"), int(now.Weekday()))
	if current = strings.TrimSpace(current); current != "" {
		fmt.Fprintf(&b, "Current expression: %s\n", current)
	}
	fmt.Fprintf(&b, "Desired schedule: %s", description)
	return b.String()
}

func parseTriggerSuggestion(content string) (TriggerSuggestion, error) {
	text := content
	if start := strings.Index(text, "{"); start >= 0 {
		text = text[start:]
	}
	// Decode a single object so trailing commentary after it is ignored.
	var suggestion TriggerSuggestion
	if err := json.NewDecoder(strings.NewReader(text)).Decode(&suggestion); err != nil {
		return TriggerSuggestion{}, fmt.Errorf("invalid JSON response: %w", err)
	}
	suggestion.Expression = strings.TrimSpace(suggestion.Expression)
	suggestion.Explanation = strings.TrimSpace(suggestion.Explanation)
	if suggestion.Expression == "" {
		return TriggerSuggestion{}, errors.New("response has an empty expression")
	}
	return suggestion, nil
}
