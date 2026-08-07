package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Cyvadra/hephaestus/internal/project"
)

// CreateProjectTool lets the LLM create a new Project: an on-disk folder
// (with a skeleton AGENTS.md) that file/exec tools can be scoped to. It is
// deliberately excluded from every default ToolGroup; a session only gains
// it once the user explicitly activates the group that includes it, which
// is this platform's stand-in for "explicit user authorization" since it
// has no interactive approval flow.
type CreateProjectTool struct {
	projects *project.Service
}

// NewCreateProjectTool creates the tool.
func NewCreateProjectTool(projects *project.Service) *CreateProjectTool {
	return &CreateProjectTool{projects: projects}
}

func (CreateProjectTool) Name() string { return "create_project" }
func (CreateProjectTool) Description() string {
	return "Creates a new Project: a named on-disk folder (with a skeleton AGENTS.md) that file and " +
		"exec tools can be scoped to. Only call this when the user has explicitly asked for a new project."
}
func (CreateProjectTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "Unique slug: lowercase letters, digits, hyphens.",
			},
			"description": map[string]any{
				"type":        "string",
				"description": "Short overview of the project's purpose, written into AGENTS.md.",
			},
		},
		"required": []string{"name"},
	}
}

func (t CreateProjectTool) Execute(_ context.Context, args map[string]any) *ToolResult {
	name, _ := args["name"].(string)
	if strings.TrimSpace(name) == "" {
		return ErrorResult("create_project: name is required")
	}
	description, _ := args["description"].(string)

	p, err := t.projects.Create(name, description)
	if err != nil {
		return ErrorResult(fmt.Sprintf("create_project: %s", err))
	}
	return UserResult(fmt.Sprintf(
		"Created project %q (id %d). The user can bind a session to it with /switch project %s.",
		p.Name, p.ID, p.Name,
	))
}

// ListProjectsTool lets the LLM see which Projects already exist, so it
// can suggest reusing one instead of creating a duplicate.
type ListProjectsTool struct {
	projects *project.Service
}

// NewListProjectsTool creates the tool.
func NewListProjectsTool(projects *project.Service) *ListProjectsTool {
	return &ListProjectsTool{projects: projects}
}

func (ListProjectsTool) Name() string { return "list_projects" }
func (ListProjectsTool) Description() string {
	return "Lists every existing Project by name and description."
}
func (ListProjectsTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

func (t ListProjectsTool) Execute(_ context.Context, _ map[string]any) *ToolResult {
	projects, err := t.projects.List()
	if err != nil {
		return ErrorResult(fmt.Sprintf("list_projects: %s", err))
	}
	out, err := json.Marshal(projects)
	if err != nil {
		return ErrorResult(fmt.Sprintf("list_projects: marshal: %s", err))
	}
	return SilentResult(string(out))
}
