package tools

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/Cyvadra/hephaestus/internal/toolkit"
)

type localShellBackend struct {
	access FileAccessConfig
}

func (b localShellBackend) workingDirectory(ctx context.Context, requested string) (string, error) {
	if requested == "" {
		workspace, ok := toolkit.WorkspaceFromContext(ctx)
		if !ok {
			return "", fmt.Errorf("requires a Project-bound session or an allowed working_directory")
		}
		return workspace, nil
	}
	return projectPath(ctx, requested, b.access)
}

func (localShellBackend) command(ctx context.Context, command, dir string) (*exec.Cmd, error) {
	if runtime.GOOS == "windows" {
		cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", command)
		cmd.Dir = dir
		return cmd, nil
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = dir
	return cmd, nil
}

func (b localShellBackend) hostInfo() string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd, err := b.command(ctx, "uname -a", "")
	if err != nil {
		return ""
	}
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
