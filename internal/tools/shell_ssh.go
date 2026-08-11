package tools

import (
	"context"
	"fmt"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"strings"
	"time"

	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/Cyvadra/hephaestus/internal/toolkit"
)

type sshShellBackend struct {
	destination  string
	projectsRoot string
	access       FileAccessConfig
}

func newSSHShellBackend(destination, projectsRoot string, access FileAccessConfig) (sshShellBackend, error) {
	if !validShellSSHDestination(destination) {
		return sshShellBackend{}, fmt.Errorf("invalid SSH destination")
	}
	if !strings.HasPrefix(projectsRoot, "/") {
		return sshShellBackend{}, fmt.Errorf("SSH projects root must be an absolute POSIX path")
	}
	if _, err := exec.LookPath("ssh"); err != nil {
		return sshShellBackend{}, fmt.Errorf("OpenSSH client unavailable: %w", err)
	}
	return sshShellBackend{destination: destination, projectsRoot: pathpkg.Clean(projectsRoot), access: access}, nil
}

func validShellSSHDestination(destination string) bool {
	return destination != "" && !strings.HasPrefix(destination, "-") && strings.IndexFunc(destination, func(r rune) bool {
		return r <= ' ' || r == 0x7f
	}) == -1
}

func (b sshShellBackend) workingDirectory(ctx context.Context, requested string) (string, error) {
	projectRoot, err := b.projectRoot(ctx)
	if err != nil {
		return "", err
	}
	if requested == "" {
		return projectRoot, nil
	}
	if strings.HasPrefix(requested, "/") {
		return pathpkg.Clean(requested), nil
	}
	return pathpkg.Join(projectRoot, requested), nil
}

func (b sshShellBackend) command(ctx context.Context, command, dir string) (*exec.Cmd, error) {
	projectRoot, err := b.projectRoot(ctx)
	if err != nil {
		return nil, err
	}
	wrapper := b.wrapper(command, dir, projectRoot)
	return exec.CommandContext(ctx, "ssh", "-T", "-o", "BatchMode=yes", "--", b.destination, wrapper), nil
}

func (b sshShellBackend) projectRoot(ctx context.Context) (string, error) {
	workspace, ok := toolkit.WorkspaceFromContext(ctx)
	if !ok {
		return "", fmt.Errorf("requires a Project-bound session")
	}
	return pathpkg.Join(b.projectsRoot, filepath.Base(workspace)), nil
}

func (b sshShellBackend) hostInfo() string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := b.sshCommand(ctx, "exec uname -a").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (b sshShellBackend) probe() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := b.sshCommand(ctx, "cd "+shellQuote(b.projectsRoot)+" && exec uname -a")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("SSH backend probe %q: %w: %s", b.destination, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (b sshShellBackend) sshCommand(ctx context.Context, remoteCommand string) *exec.Cmd {
	return exec.CommandContext(ctx, "ssh", "-T", "-o", "BatchMode=yes", "--", b.destination, remoteCommand)
}

func (b sshShellBackend) wrapper(command, dir, projectRoot string) string {
	root := shellQuote(projectRoot)
	directory := shellQuote(dir)
	createDefaultProject := ""
	if pathpkg.Base(projectRoot) == store.DefaultProjectName && dir == projectRoot {
		createDefaultProject = "mkdir -p " + root + " || exit; "
	}
	allowed := ""
	if !b.access.AllowOutsideProject {
		allowed = `case "$dir" in "$root"|"$root"/*|/tmp|/tmp/*) ;; *) echo "shell: working_directory: access denied: path is outside the project and shared roots" >&2; exit 126;; esac; `
	}
	return createDefaultProject + `root=$(cd ` + root + ` && pwd -P) || exit; dir=$(cd ` + directory + ` && pwd -P) || exit; ` + allowed + `cd "$dir" || exit; exec sh -c ` + shellQuote(command)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\\"'\\\"'") + "'"
}
