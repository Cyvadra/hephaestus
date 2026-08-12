package tools

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/Cyvadra/hephaestus/internal/toolkit"
)

var ErrDeliveryFileNotFound = errors.New("send_file: file does not exist")

// SendFileTool explicitly delivers a local Project file to the user.
// Delivery records a project-relative reference; the host revalidates the
// file when it persists and serves the attachment.
type SendFileTool struct {
	enabled bool
}

func NewSendFileTool(enabled bool) *SendFileTool {
	return &SendFileTool{enabled: enabled}
}

func (SendFileTool) Name() string       { return "send_file" }
func (t *SendFileTool) Available() bool { return t.enabled }
func (SendFileTool) Description() string {
	return "Sends one existing file from the current local Project to the user. After creating a file, call this tool with its path; mentioning a path in text does not send the file."
}
func (SendFileTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"path": map[string]any{"type": "string", "description": "Path to an existing regular file under the current Project."},
	}, "required": []string{"path"}}
}

func (t *SendFileTool) Execute(ctx context.Context, args map[string]any) *toolkit.ToolResult {
	if !t.enabled {
		return toolkit.ErrorResult("send_file: unavailable because the configured shell backend is not local")
	}
	path, _ := args["path"].(string)
	delivery, err := resolveFileDelivery(ctx, path)
	if err != nil {
		return toolkit.ErrorResult("send_file: " + err.Error())
	}
	return &toolkit.ToolResult{
		ForLLM:     fmt.Sprintf("Sent file %q to the user.", delivery.Name),
		Deliveries: []toolkit.FileDelivery{delivery},
	}
}

func resolveFileDelivery(ctx context.Context, path string) (toolkit.FileDelivery, error) {
	if strings.TrimSpace(path) == "" {
		return toolkit.FileDelivery{}, fmt.Errorf("path is required")
	}
	workspace, ok := toolkit.WorkspaceFromContext(ctx)
	if !ok {
		return toolkit.FileDelivery{}, fmt.Errorf("requires a Project-bound session")
	}
	root, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return toolkit.FileDelivery{}, fmt.Errorf("resolve project root: %w", err)
	}
	if filepath.IsAbs(path) {
		return toolkit.FileDelivery{}, fmt.Errorf("path must be relative to the current Project")
	}
	candidate := filepath.Clean(filepath.Join(root, path))
	if escapesRoot(root, candidate) {
		return toolkit.FileDelivery{}, fmt.Errorf("access denied: path is outside the Project")
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		if os.IsNotExist(err) {
			return toolkit.FileDelivery{}, ErrDeliveryFileNotFound
		}
		return toolkit.FileDelivery{}, fmt.Errorf("resolve file: %w", err)
	}
	if escapesRoot(root, resolved) {
		return toolkit.FileDelivery{}, fmt.Errorf("access denied: symlink resolves outside the Project")
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return toolkit.FileDelivery{}, fmt.Errorf("inspect file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return toolkit.FileDelivery{}, fmt.Errorf("path must identify a regular file")
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil {
		return toolkit.FileDelivery{}, fmt.Errorf("resolve project-relative path: %w", err)
	}
	name := filepath.Base(resolved)
	return toolkit.FileDelivery{
		Path: filepath.ToSlash(relative),
		Name: name,
		Size: info.Size(),
		MIME: detectMIME(resolved),
	}, nil
}

// ResolveProjectFile revalidates a persisted project-relative delivery path
// and returns its resolved absolute filename and current file metadata.
func ResolveProjectFile(projectDir, path string) (string, toolkit.FileDelivery, error) {
	delivery, err := resolveFileDelivery(toolkit.WithWorkspace(context.Background(), projectDir), path)
	if err != nil {
		return "", toolkit.FileDelivery{}, err
	}
	root, err := filepath.EvalSymlinks(projectDir)
	if err != nil {
		return "", toolkit.FileDelivery{}, fmt.Errorf("resolve project root: %w", err)
	}
	return filepath.Join(root, filepath.FromSlash(delivery.Path)), delivery, nil
}

func detectMIME(path string) string {
	if value := mime.TypeByExtension(filepath.Ext(path)); value != "" {
		return value
	}
	file, err := os.Open(path)
	if err != nil {
		return "application/octet-stream"
	}
	defer file.Close()
	buffer := make([]byte, 512)
	count, _ := file.Read(buffer)
	return http.DetectContentType(buffer[:count])
}
