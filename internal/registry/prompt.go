package registry

import (
	"fmt"
	"regexp"
	"time"
)

var (
	promptPlaceholderPattern = regexp.MustCompile(`\{\{([^{}]*)\}\}`)
	promptVariablePattern    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	builtinPromptVariables   = map[string]bool{
		"now":           true,
		"date":          true,
		"time":          true,
		"project":       true,
		"workspace":     true,
		"session_id":    true,
		"session_title": true,
	}
)

// PromptVars provides dynamic values available during one prompt rendering.
type PromptVars map[string]string

// TimePromptVars returns the standard time values for a single prompt
// rendering. now is kept in RFC3339 form so its offset is explicit.
func TimePromptVars(now time.Time) PromptVars {
	return PromptVars{
		"now":  now.Format(time.RFC3339),
		"date": now.Format("2006-01-02"),
		"time": now.Format("15:04:05Z07:00"),
	}
}

func validPromptVariable(name string) bool {
	return promptVariablePattern.MatchString(name)
}

// RenderPrompt replaces every prompt placeholder with its configured constant
// or a dynamic value for this rendering. Constants take precedence over
// built-ins so an explicit deployment setting may override a default.
func (r *Registry) RenderPrompt(text string, vars ...PromptVars) (string, error) {
	dynamic := PromptVars{}
	for _, values := range vars {
		for name, value := range values {
			dynamic[name] = value
		}
	}
	var missing string
	var invalid string
	rendered := promptPlaceholderPattern.ReplaceAllStringFunc(text, func(placeholder string) string {
		name := promptPlaceholderPattern.FindStringSubmatch(placeholder)[1]
		if !validPromptVariable(name) {
			if invalid == "" {
				invalid = placeholder
			}
			return placeholder
		}
		if constant, ok := r.Constants[name]; ok {
			return constant.Value
		}
		if value, ok := dynamic[name]; ok {
			return value
		}
		if missing == "" {
			missing = name
		}
		return placeholder
	})
	if invalid != "" {
		return "", fmt.Errorf("invalid placeholder %q", invalid)
	}
	if missing != "" {
		return "", fmt.Errorf("undefined constant %q", missing)
	}
	if placeholder := promptPlaceholderPattern.FindString(rendered); placeholder != "" {
		return "", fmt.Errorf("constant value contains placeholder %q", placeholder)
	}
	return rendered, nil
}

// RenderIdentity returns an independent Identity with every preset prompt
// rendered. The registry snapshot remains immutable.
func (r *Registry) RenderIdentity(identity Identity, vars ...PromptVars) (Identity, error) {
	renderedSystemPrompt, err := r.RenderPrompt(identity.SystemPrompt, vars...)
	if err != nil {
		return Identity{}, fmt.Errorf("render identity %q system prompt: %w", identity.Name, err)
	}
	identity.SystemPrompt = renderedSystemPrompt
	identity.InjectedMessages = append([]Message(nil), identity.InjectedMessages...)
	for index := range identity.InjectedMessages {
		identity.InjectedMessages[index].Content, err = r.RenderPrompt(identity.InjectedMessages[index].Content, vars...)
		if err != nil {
			return Identity{}, fmt.Errorf("render identity %q injected message %d: %w", identity.Name, index+1, err)
		}
	}
	return identity, nil
}

func (r *Registry) validatePrompt(owner, text string) error {
	for _, placeholder := range promptPlaceholderPattern.FindAllString(text, -1) {
		name := promptPlaceholderPattern.FindStringSubmatch(placeholder)[1]
		if !validPromptVariable(name) {
			return fmt.Errorf("registry: %s references invalid placeholder %q", owner, placeholder)
		}
		if _, ok := r.Constants[name]; !ok && !builtinPromptVariables[name] {
			return fmt.Errorf("registry: %s references undefined constant %q", owner, name)
		}
	}
	return nil
}
