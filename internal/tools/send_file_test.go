package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Cyvadra/hephaestus/internal/toolkit"
)

func TestSendFileToolDeliversProjectFile(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "report.md")
	if err := os.WriteFile(path, []byte("# Report\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := NewSendFileTool(true).Execute(toolkit.WithWorkspace(context.Background(), workspace), map[string]any{"path": "report.md"})
	if result.IsError || len(result.Deliveries) != 1 {
		t.Fatalf("result = %+v, want one delivery", result)
	}
	delivery := result.Deliveries[0]
	if delivery.Path != "report.md" || delivery.Name != "report.md" || delivery.Size != 9 || delivery.MIME != "text/markdown; charset=utf-8" {
		t.Fatalf("delivery = %+v", delivery)
	}
}

func TestSendFileToolRejectsUnsafePaths(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(workspace, "directory"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := toolkit.WithWorkspace(context.Background(), workspace)
	tool := NewSendFileTool(true)
	for _, path := range []string{"../secret.txt", outside, "escape", "directory", "missing.txt"} {
		result := tool.Execute(ctx, map[string]any{"path": path})
		if !result.IsError || len(result.Deliveries) != 0 {
			t.Fatalf("path %q result = %+v, want error without delivery", path, result)
		}
	}
}

func TestSendFileToolRejectsUnavailableBackend(t *testing.T) {
	result := NewSendFileTool(false).Execute(context.Background(), map[string]any{"path": "report.md"})
	if !result.IsError {
		t.Fatalf("result = %+v, want unavailable error", result)
	}
}
