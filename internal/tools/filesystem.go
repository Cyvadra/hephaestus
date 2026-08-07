package tools

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Cyvadra/hephaestus/internal/fsutil"
	"github.com/Cyvadra/hephaestus/internal/toolkit"
)

const maxReadFileSize = 64 * 1024

// maxReadFileLinesBytes caps how much of a file the line-numbered reader
// will pull into memory at once; beyond this it suggests read_file instead.
const maxReadFileLinesBytes = 8 * 1024 * 1024

// FileAccessConfig controls the paths exposed through filesystem tools.
// A nil SharedRoots uses the system temporary directory; a non-nil slice
// replaces that default, which is useful for tests and embedded callers.
type FileAccessConfig struct {
	AllowOutsideProject bool
	SharedRoots         []string
}

func (c FileAccessConfig) sharedRoots() []string {
	if c.SharedRoots == nil {
		return []string{os.TempDir()}
	}
	return c.SharedRoots
}

// projectPath resolves a tool path against the bound Project when relative.
// Absolute paths are accepted under the Project or shared roots. Symlinks
// cannot escape an allowed root unless the explicit override is enabled.
func projectPath(ctx context.Context, path string, config FileAccessConfig) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path must be non-empty")
	}

	workspace, hasWorkspace := toolkit.WorkspaceFromContext(ctx)
	var projectRoot string
	if hasWorkspace {
		resolved, err := filepath.EvalSymlinks(workspace)
		if err != nil {
			return "", fmt.Errorf("resolve project root: %w", err)
		}
		projectRoot = resolved
	}

	expanded, err := fsutil.ExpandHome(path)
	if err != nil {
		return "", err
	}
	var candidate string
	if filepath.IsAbs(expanded) {
		candidate = filepath.Clean(expanded)
	} else {
		if !hasWorkspace {
			return "", fmt.Errorf("relative paths require a Project-bound session")
		}
		candidate = filepath.Clean(filepath.Join(projectRoot, expanded))
	}

	allowedRoots := config.sharedRoots()
	if !config.AllowOutsideProject && !pathAllowed(candidate, projectRoot, allowedRoots) {
		return "", fmt.Errorf("access denied: path is outside the project and shared roots")
	}
	if resolved, err := filepath.EvalSymlinks(candidate); err == nil {
		if !config.AllowOutsideProject && !pathAllowed(resolved, projectRoot, allowedRoots) {
			return "", fmt.Errorf("access denied: symlink resolves outside allowed roots")
		}
		return resolved, nil
	}
	parent, err := existingParent(filepath.Dir(candidate))
	if err != nil {
		return "", fmt.Errorf("resolve path parent: %w", err)
	}
	if !config.AllowOutsideProject && !pathAllowed(parent, projectRoot, allowedRoots) {
		return "", fmt.Errorf("access denied: symlink resolves outside allowed roots")
	}
	return candidate, nil
}

func pathAllowed(path, projectRoot string, sharedRoots []string) bool {
	if projectRoot != "" && !escapesRoot(projectRoot, path) {
		return true
	}
	for _, root := range sharedRoots {
		resolvedRoot, err := filepath.EvalSymlinks(root)
		if err == nil && !escapesRoot(resolvedRoot, path) {
			return true
		}
	}
	return false
}

func escapesRoot(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	return err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func existingParent(path string) (string, error) {
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			return resolved, nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		if filepath.Dir(current) == current {
			return "", os.ErrNotExist
		}
	}
}

// ReadFileTool reads a UTF-8 text file from an allowed path.
type ReadFileTool struct{ access FileAccessConfig }

func NewReadFileTool(access FileAccessConfig) ReadFileTool { return ReadFileTool{access: access} }

func (ReadFileTool) Name() string { return "read_file" }
func (ReadFileTool) Description() string {
	return "Reads a UTF-8 text file from the current Project or a shared temporary path. Supports byte offset and length."
}
func (ReadFileTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"path":   map[string]any{"type": "string"},
		"offset": map[string]any{"type": "integer", "minimum": 0},
		"length": map[string]any{"type": "integer", "minimum": 0},
	}, "required": []string{"path"}}
}
func (t ReadFileTool) Execute(ctx context.Context, args map[string]any) *toolkit.ToolResult {
	path, _ := args["path"].(string)
	resolved, err := projectPath(ctx, path, t.access)
	if err != nil {
		return toolkit.ErrorResult("read_file: " + err.Error())
	}
	offset, _ := args["offset"].(float64)
	length, _ := args["length"].(float64)
	if offset < 0 {
		offset = 0
	}
	if length <= 0 || length > maxReadFileSize {
		length = maxReadFileSize
	}

	file, err := os.Open(resolved)
	if err != nil {
		return toolkit.ErrorResult("read_file: " + err.Error())
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return toolkit.ErrorResult("read_file: " + err.Error())
	}
	if int64(offset) > info.Size() {
		return toolkit.ErrorResult("read_file: offset is outside the file")
	}

	buf := make([]byte, int(length))
	n, err := file.ReadAt(buf, int64(offset))
	if err != nil && err != io.EOF {
		return toolkit.ErrorResult("read_file: " + err.Error())
	}
	return toolkit.SilentResult(string(buf[:n]))
}

// WriteFileTool writes a text file to an allowed path.
type WriteFileTool struct{ access FileAccessConfig }

func NewWriteFileTool(access FileAccessConfig) WriteFileTool { return WriteFileTool{access: access} }

func (WriteFileTool) Name() string { return "write_file" }
func (WriteFileTool) Description() string {
	return "Writes a text file in the current Project or a shared temporary path, creating parent directories when needed."
}
func (WriteFileTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"path": map[string]any{"type": "string"}, "content": map[string]any{"type": "string"},
	}, "required": []string{"path", "content"}}
}
func (t WriteFileTool) Execute(ctx context.Context, args map[string]any) *toolkit.ToolResult {
	path, _ := args["path"].(string)
	content, _ := args["content"].(string)
	resolved, err := projectPath(ctx, path, t.access)
	if err != nil {
		return toolkit.ErrorResult("write_file: " + err.Error())
	}
	if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
		return toolkit.ErrorResult("write_file: " + err.Error())
	}
	if err := os.WriteFile(resolved, []byte(content), 0o644); err != nil {
		return toolkit.ErrorResult("write_file: " + err.Error())
	}
	return toolkit.SilentResult("Wrote " + path)
}

// ReadFileLinesTool reads a UTF-8 text file with one-indexed line numbers.
type ReadFileLinesTool struct{ access FileAccessConfig }

func NewReadFileLinesTool(access FileAccessConfig) ReadFileLinesTool {
	return ReadFileLinesTool{access: access}
}

func (ReadFileLinesTool) Name() string { return "read_file_lines" }
func (ReadFileLinesTool) Description() string {
	return "Reads a UTF-8 text file from the current Project with one-indexed line numbers."
}
func (ReadFileLinesTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"path":       map[string]any{"type": "string"},
		"start_line": map[string]any{"type": "integer", "minimum": 1},
		"max_lines":  map[string]any{"type": "integer", "minimum": 0},
	}, "required": []string{"path"}}
}
func (t ReadFileLinesTool) Execute(ctx context.Context, args map[string]any) *toolkit.ToolResult {
	path, _ := args["path"].(string)
	resolved, err := projectPath(ctx, path, t.access)
	if err != nil {
		return toolkit.ErrorResult("read_file_lines: " + err.Error())
	}
	content, err := os.ReadFile(resolved)
	if err != nil {
		return toolkit.ErrorResult("read_file_lines: " + err.Error())
	}
	if len(content) > maxReadFileLinesBytes {
		return toolkit.ErrorResult("read_file_lines: file is too large to read by lines; use read_file with offset/length instead")
	}
	start, _ := args["start_line"].(float64)
	maxLines, _ := args["max_lines"].(float64)
	if start < 1 {
		start = 1
	}
	if maxLines < 0 {
		maxLines = 0
	}
	lines := strings.Split(string(content), "\n")
	if int(start) > len(lines) {
		return toolkit.SilentResult("[END OF FILE]")
	}
	end := len(lines)
	if maxLines > 0 && int(start)-1+int(maxLines) < end {
		end = int(start) - 1 + int(maxLines)
	}
	var output strings.Builder
	for i := int(start) - 1; i < end; i++ {
		if output.Len() > 0 {
			output.WriteByte('\n')
		}
		fmt.Fprintf(&output, "%d|%s", i+1, lines[i])
	}
	return toolkit.SilentResult(output.String())
}

// EditFileTool replaces one exact occurrence of old_text with new_text.
type EditFileTool struct{ access FileAccessConfig }

func NewEditFileTool(access FileAccessConfig) EditFileTool { return EditFileTool{access: access} }

func (EditFileTool) Name() string { return "edit_file" }
func (EditFileTool) Description() string {
	return "Replaces one exact occurrence of old_text with new_text in a Project file."
}
func (EditFileTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"path": map[string]any{"type": "string"}, "old_text": map[string]any{"type": "string"}, "new_text": map[string]any{"type": "string"},
	}, "required": []string{"path", "old_text", "new_text"}}
}
func (t EditFileTool) Execute(ctx context.Context, args map[string]any) *toolkit.ToolResult {
	path, _ := args["path"].(string)
	oldText, _ := args["old_text"].(string)
	newText, _ := args["new_text"].(string)
	resolved, err := projectPath(ctx, path, t.access)
	if err != nil {
		return toolkit.ErrorResult("edit_file: " + err.Error())
	}
	content, err := os.ReadFile(resolved)
	if err != nil {
		return toolkit.ErrorResult("edit_file: " + err.Error())
	}
	if !strings.Contains(string(content), oldText) {
		return toolkit.ErrorResult("edit_file: old_text not found in file")
	}
	if strings.Count(string(content), oldText) != 1 {
		return toolkit.ErrorResult("edit_file: old_text must occur exactly once")
	}
	if err := os.WriteFile(resolved, []byte(strings.Replace(string(content), oldText, newText, 1)), 0o644); err != nil {
		return toolkit.ErrorResult("edit_file: " + err.Error())
	}
	return toolkit.SilentResult("Edited " + path)
}

// AppendFileTool appends text to a file in an allowed path.
type AppendFileTool struct{ access FileAccessConfig }

func NewAppendFileTool(access FileAccessConfig) AppendFileTool { return AppendFileTool{access: access} }

func (AppendFileTool) Name() string        { return "append_file" }
func (AppendFileTool) Description() string { return "Appends text to a file in the current Project." }
func (AppendFileTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"path": map[string]any{"type": "string"}, "content": map[string]any{"type": "string"},
	}, "required": []string{"path", "content"}}
}
func (t AppendFileTool) Execute(ctx context.Context, args map[string]any) *toolkit.ToolResult {
	path, _ := args["path"].(string)
	appendContent, _ := args["content"].(string)
	resolved, err := projectPath(ctx, path, t.access)
	if err != nil {
		return toolkit.ErrorResult("append_file: " + err.Error())
	}
	file, err := os.OpenFile(resolved, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return toolkit.ErrorResult("append_file: " + err.Error())
	}
	defer file.Close()
	if _, err := file.WriteString(appendContent); err != nil {
		return toolkit.ErrorResult("append_file: " + err.Error())
	}
	return toolkit.SilentResult("Appended to " + path)
}

// ListDirTool lists entries in an allowed directory.
type ListDirTool struct{ access FileAccessConfig }

func NewListDirTool(access FileAccessConfig) ListDirTool { return ListDirTool{access: access} }

func (ListDirTool) Name() string { return "list_dir" }
func (ListDirTool) Description() string {
	return "Lists entries in a directory in the current Project."
}
func (ListDirTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}
}
func (t ListDirTool) Execute(ctx context.Context, args map[string]any) *toolkit.ToolResult {
	path, _ := args["path"].(string)
	if path == "" {
		path = "."
	}
	resolved, err := projectPath(ctx, path, t.access)
	if err != nil {
		return toolkit.ErrorResult("list_dir: " + err.Error())
	}
	entries, err := os.ReadDir(resolved)
	if err != nil {
		return toolkit.ErrorResult("list_dir: " + err.Error())
	}
	var lines []string
	for _, entry := range entries {
		suffix := ""
		if entry.IsDir() {
			suffix = "/"
		}
		lines = append(lines, entry.Name()+suffix)
	}
	return toolkit.SilentResult(strings.Join(lines, "\n"))
}
