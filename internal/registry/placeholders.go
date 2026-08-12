// The ${...} placeholders a Job binding may use in its workflow input.
// Placeholders are resolved by string substitution only, never by evaluating
// arbitrary expressions.

package registry

import (
	"fmt"
	"regexp"
	"time"
)

var placeholderVars = map[string]bool{
	"job.name":                  true,
	"job.title":                 true,
	"job.goal":                  true,
	"run.local_date":            true,
	"run.started_at":            true,
	"trigger.last_succeeded_at": true,
	"now":                       true,
}

var (
	placeholderRe      = regexp.MustCompile(`\$\{([^{}]+)\}`)
	exactPlaceholderRe = regexp.MustCompile(`^\$\{([^{}]+)\}$`)
)

// scanPlaceholders walks every string leaf of input, reporting whether any
// ${...} placeholder exists and the first placeholder outside the known set.
func scanPlaceholders(input map[string]any) (has bool, unknown string) {
	walkStringLeaves(input, func(s string) bool {
		for _, match := range placeholderRe.FindAllStringSubmatch(s, -1) {
			has = true
			if !placeholderVars[match[1]] {
				unknown = match[1]
				return false
			}
		}
		return true
	})
	return has, unknown
}

func hasPlaceholders(input map[string]any) bool {
	has, _ := scanPlaceholders(input)
	return has
}

func validatePlaceholders(input map[string]any) error {
	if _, unknown := scanPlaceholders(input); unknown != "" {
		return fmt.Errorf("registry: unknown placeholder ${%s}", unknown)
	}
	return nil
}

// walkStringLeaves visits every string leaf reachable from v; visit reports
// whether to continue.
func walkStringLeaves(v any, visit func(string) bool) bool {
	switch typed := v.(type) {
	case map[string]any:
		for _, child := range typed {
			if !walkStringLeaves(child, visit) {
				return false
			}
		}
	case []any:
		for _, child := range typed {
			if !walkStringLeaves(child, visit) {
				return false
			}
		}
	case string:
		return visit(typed)
	}
	return true
}

// ResolvePlaceholders substitutes the known ${...} placeholders in input
// using vars. A string leaf that is exactly one placeholder adopts that
// variable's native value; embedded placeholders interpolate as text.
// time.Time values format as RFC3339 and zero/nil values resolve to the
// empty string.
func ResolvePlaceholders(input map[string]any, vars map[string]any) (map[string]any, error) {
	out, err := resolvePlaceholderValue(input, vars)
	if err != nil {
		return nil, err
	}
	resolved, ok := out.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("registry: input must be an object")
	}
	return resolved, nil
}

func resolvePlaceholderValue(v any, vars map[string]any) (any, error) {
	switch typed := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			resolved, err := resolvePlaceholderValue(child, vars)
			if err != nil {
				return nil, err
			}
			out[key] = resolved
		}
		return out, nil
	case []any:
		out := make([]any, len(typed))
		for index, child := range typed {
			resolved, err := resolvePlaceholderValue(child, vars)
			if err != nil {
				return nil, err
			}
			out[index] = resolved
		}
		return out, nil
	case string:
		if match := exactPlaceholderRe.FindStringSubmatch(typed); match != nil {
			value, ok := vars[match[1]]
			if !ok {
				return nil, fmt.Errorf("registry: unknown placeholder ${%s}", match[1])
			}
			// Exact-match leaves adopt the variable's native value, except
			// time/nil which normalize to text.
			switch value.(type) {
			case time.Time, nil:
				return placeholderText(value), nil
			default:
				return value, nil
			}
		}
		if !placeholderRe.MatchString(typed) {
			return typed, nil
		}
		var firstErr error
		out := placeholderRe.ReplaceAllStringFunc(typed, func(match string) string {
			name := placeholderRe.FindStringSubmatch(match)[1]
			value, ok := vars[name]
			if !ok {
				if firstErr == nil {
					firstErr = fmt.Errorf("registry: unknown placeholder ${%s}", name)
				}
				return match
			}
			return placeholderText(value)
		})
		return out, firstErr
	default:
		return v, nil
	}
}

func placeholderText(v any) string {
	switch typed := v.(type) {
	case time.Time:
		if typed.IsZero() {
			return ""
		}
		return typed.Format(time.RFC3339)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", v)
	}
}
