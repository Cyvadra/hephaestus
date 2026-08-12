package toolkit

// Scope restricts where a Tool (or, via the same interface, a Plugin) may
// run. Chat turns use ScopeSession; workflow steps use ScopeWorkflow.
type Scope string

const (
	ScopeSession  Scope = "session"
	ScopeWorkflow Scope = "workflow"
)

// Scoped is an optional capability restricting the execution scopes a Tool
// is offered to. A Tool without Scoped runs in every scope.
type Scoped interface {
	Scopes() []Scope
}

// ScopeAllows reports whether value (anything implementing Scoped, such as a
// Tool or Plugin) may run in scope. Values without Scoped run everywhere.
func ScopeAllows(value any, scope Scope) bool {
	scoped, ok := value.(Scoped)
	if !ok {
		return true
	}
	for _, s := range scoped.Scopes() {
		if s == scope {
			return true
		}
	}
	return false
}

// FilterScope returns the tools allowed to run in scope, preserving order.
func FilterScope(tools []Tool, scope Scope) []Tool {
	out := make([]Tool, 0, len(tools))
	for _, tool := range tools {
		if ScopeAllows(tool, scope) {
			out = append(out, tool)
		}
	}
	return out
}
