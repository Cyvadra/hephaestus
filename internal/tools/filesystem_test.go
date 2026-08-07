package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cyvadra/hephaestus/internal/toolkit"
)

func projectTestContext(t *testing.T) (context.Context, string) {
	t.Helper()
	root := t.TempDir()
	return toolkit.WithWorkspace(context.Background(), root), root
}

func TestReadFileToolRequiresProject(t *testing.T) {
	result := ReadFileTool{}.Execute(context.Background(), map[string]any{"path": "note.txt"})
	if !result.IsError || !strings.Contains(result.ForLLM, "Project-bound") {
		t.Fatalf("expected Project binding error, got %+v", result)
	}
}

func TestFilesystemToolsRejectPathsOutsideProject(t *testing.T) {
	ctx, root := projectTestContext(t)
	outside := filepath.Join(filepath.Dir(root), "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewReadFileTool(FileAccessConfig{SharedRoots: []string{}})
	result := tool.Execute(ctx, map[string]any{"path": "../outside.txt"})
	if !result.IsError || !strings.Contains(result.ForLLM, "outside the project") {
		t.Fatalf("expected traversal rejection, got %+v", result)
	}

	if err := os.Symlink(outside, filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	result = tool.Execute(ctx, map[string]any{"path": "link.txt"})
	if !result.IsError || !strings.Contains(result.ForLLM, "symlink resolves outside") {
		t.Fatalf("expected symlink rejection, got %+v", result)
	}
}

func TestFilesystemToolsRejectWriteThroughEscapingParentSymlink(t *testing.T) {
	ctx, root := projectTestContext(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "outside")); err != nil {
		t.Fatal(err)
	}
	tool := NewWriteFileTool(FileAccessConfig{SharedRoots: []string{}})
	result := tool.Execute(ctx, map[string]any{"path": "outside/new.txt", "content": "secret"})
	if !result.IsError || !strings.Contains(result.ForLLM, "outside allowed roots") {
		t.Fatalf("expected escaping parent symlink rejection, got %+v", result)
	}
}

func TestFilesystemToolsAllowSystemTempWithoutProject(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.txt")
	write := WriteFileTool{}.Execute(context.Background(), map[string]any{"path": path, "content": "shared"})
	if write.IsError {
		t.Fatalf("write to system temp failed: %+v", write)
	}
	read := ReadFileTool{}.Execute(context.Background(), map[string]any{"path": path})
	if read.IsError || read.ForLLM != "shared" {
		t.Fatalf("unexpected temp read result: %+v", read)
	}
}

func TestFilesystemOverrideAllowsPathOutsideProject(t *testing.T) {
	ctx, root := projectTestContext(t)
	outside := filepath.Join(filepath.Dir(root), "override.txt")
	tool := NewWriteFileTool(FileAccessConfig{AllowOutsideProject: true, SharedRoots: []string{}})
	result := tool.Execute(ctx, map[string]any{"path": outside, "content": "allowed"})
	if result.IsError {
		t.Fatalf("override write failed: %+v", result)
	}
	content, err := os.ReadFile(outside)
	if err != nil || string(content) != "allowed" {
		t.Fatalf("override file = %q, %v", content, err)
	}
}

func TestFilesystemToolsWriteEditAppendAndList(t *testing.T) {
	ctx, _ := projectTestContext(t)
	write := WriteFileTool{}.Execute(ctx, map[string]any{"path": "notes/today.txt", "content": "first\n"})
	if write.IsError {
		t.Fatalf("write failed: %+v", write)
	}
	edit := EditFileTool{}.Execute(ctx, map[string]any{
		"path": "notes/today.txt", "old_text": "first", "new_text": "second",
	})
	if edit.IsError {
		t.Fatalf("edit failed: %+v", edit)
	}
	appendResult := AppendFileTool{}.Execute(ctx, map[string]any{"path": "notes/today.txt", "content": "third\n"})
	if appendResult.IsError {
		t.Fatalf("append failed: %+v", appendResult)
	}
	read := ReadFileTool{}.Execute(ctx, map[string]any{"path": "notes/today.txt"})
	if read.IsError || read.ForLLM != "second\nthird\n" {
		t.Fatalf("unexpected read result: %+v", read)
	}
	list := ListDirTool{}.Execute(ctx, map[string]any{"path": "notes"})
	if list.IsError || list.ForLLM != "today.txt" {
		t.Fatalf("unexpected directory listing: %+v", list)
	}
}

func TestReadFileLinesTool(t *testing.T) {
	ctx, _ := projectTestContext(t)
	if result := (WriteFileTool{}).Execute(ctx, map[string]any{"path": "lines.txt", "content": "one\ntwo\nthree"}); result.IsError {
		t.Fatalf("write failed: %+v", result)
	}
	result := ReadFileLinesTool{}.Execute(ctx, map[string]any{"path": "lines.txt", "start_line": float64(2), "max_lines": float64(2)})
	if result.IsError || result.ForLLM != "2|two\n3|three" {
		t.Fatalf("unexpected line result: %+v", result)
	}
}

func TestReadFileToolOffsetAndLength(t *testing.T) {
	ctx, _ := projectTestContext(t)
	if result := (WriteFileTool{}).Execute(ctx, map[string]any{"path": "data.txt", "content": "0123456789"}); result.IsError {
		t.Fatalf("write failed: %+v", result)
	}
	result := ReadFileTool{}.Execute(ctx, map[string]any{"path": "data.txt", "offset": float64(3), "length": float64(4)})
	if result.IsError || result.ForLLM != "3456" {
		t.Fatalf("unexpected offset read: %+v", result)
	}
	result = ReadFileTool{}.Execute(ctx, map[string]any{"path": "data.txt", "offset": float64(50)})
	if !result.IsError || !strings.Contains(result.ForLLM, "outside the file") {
		t.Fatalf("expected out-of-range offset error, got %+v", result)
	}
}
