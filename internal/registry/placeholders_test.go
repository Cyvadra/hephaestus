package registry

import (
	"testing"
	"time"
)

func TestScanPlaceholders(t *testing.T) {
	if hasPlaceholders(map[string]any{"a": "literal"}) {
		t.Fatal("expected no placeholders in literal input")
	}
	if !hasPlaceholders(map[string]any{"a": "${job.goal}"}) {
		t.Fatal("expected placeholder detected")
	}
	if !hasPlaceholders(map[string]any{"nested": []any{map[string]any{"b": "x ${now} y"}}}) {
		t.Fatal("expected nested placeholder detected")
	}
	if hasPlaceholders(map[string]any{"n": 42, "b": true, "c": []any{}}) {
		t.Fatal("expected non-string leaves to not count")
	}
}

func TestValidatePlaceholders(t *testing.T) {
	valid := map[string]any{
		"topic": "${job.goal}",
		"count": 42,
		"list":  []any{"${now}", "plain"},
	}
	if err := validatePlaceholders(valid); err != nil {
		t.Fatalf("expected known placeholders valid: %v", err)
	}
	if err := validatePlaceholders(map[string]any{"a": "${unknown.var}"}); err == nil {
		t.Fatal("expected unknown placeholder rejected")
	}
	if err := validatePlaceholders(nil); err != nil {
		t.Fatalf("expected nil input valid: %v", err)
	}
}

func TestResolvePlaceholders_ExactPlaceholderAdoptsType(t *testing.T) {
	started := time.Date(2026, 8, 11, 9, 0, 0, 0, time.Local)
	vars := map[string]any{
		"job.name":                  "morning-brief",
		"job.goal":                  "summarize",
		"run.started_at":            started,
		"trigger.last_succeeded_at": time.Time{},
		"now":                       started,
	}
	out, err := ResolvePlaceholders(map[string]any{
		"name":    "${job.name}",
		"goal":    "${job.goal}",
		"started": "${run.started_at}",
		"never":   "${trigger.last_succeeded_at}",
		"now":     "${now}",
		"plain":   42,
	}, vars)
	if err != nil {
		t.Fatalf("ResolvePlaceholders: %v", err)
	}
	if out["name"] != "morning-brief" {
		t.Fatalf("expected name substitution, got %v", out["name"])
	}
	if out["started"] != started.Format(time.RFC3339) {
		t.Fatalf("expected RFC3339 started time, got %v", out["started"])
	}
	if out["never"] != "" {
		t.Fatalf("expected zero time to resolve to empty string, got %v", out["never"])
	}
	if out["plain"] != 42 {
		t.Fatalf("expected non-string leaf preserved, got %v", out["plain"])
	}
}

func TestResolvePlaceholders_EmbeddedPlaceholderInterpolates(t *testing.T) {
	out, err := ResolvePlaceholders(map[string]any{
		"topic": "About ${job.goal} today",
	}, map[string]any{"job.goal": "Hephaestus"})
	if err != nil {
		t.Fatalf("ResolvePlaceholders: %v", err)
	}
	if out["topic"] != "About Hephaestus today" {
		t.Fatalf("expected interpolation, got %v", out["topic"])
	}
}

func TestResolvePlaceholders_NestedStructures(t *testing.T) {
	out, err := ResolvePlaceholders(map[string]any{
		"nested": map[string]any{
			"list": []any{"${job.name}", "static", 7},
		},
	}, map[string]any{"job.name": "brief"})
	if err != nil {
		t.Fatalf("ResolvePlaceholders: %v", err)
	}
	list := out["nested"].(map[string]any)["list"].([]any)
	if list[0] != "brief" || list[1] != "static" || list[2] != 7 {
		t.Fatalf("unexpected nested resolution: %v", list)
	}
}

func TestResolvePlaceholders_UnknownPlaceholderRejected(t *testing.T) {
	if _, err := ResolvePlaceholders(map[string]any{"a": "${nope.var}"}, map[string]any{}); err == nil {
		t.Fatal("expected unknown placeholder rejected")
	}
	if _, err := ResolvePlaceholders(map[string]any{"a": "x ${nope.var} y"}, map[string]any{}); err == nil {
		t.Fatal("expected unknown embedded placeholder rejected")
	}
}
