package tools

import (
	"context"
	"fmt"
)

// RunTool validates args against tool's schema, executes it with panic
// isolation so a misbehaving tool can't crash the turn, and guarantees a
// non-nil *ToolResult even on failure.
func RunTool(ctx context.Context, tool Tool, args map[string]any) (result *ToolResult) {
	if err := validateArgs(tool.Parameters(), args); err != nil {
		return ErrorResult(fmt.Sprintf("invalid arguments for tool %q: %s", tool.Name(), err))
	}

	defer func() {
		if r := recover(); r != nil {
			result = ErrorResult(fmt.Sprintf("tool %q panicked: %v", tool.Name(), r))
		}
	}()

	result = tool.Execute(ctx, args)
	if result == nil {
		result = ErrorResult(fmt.Sprintf("tool %q returned a nil result", tool.Name()))
	}
	return result
}
