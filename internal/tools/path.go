package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Cyvadra/hephaestus/internal/fsutil"
	"github.com/Cyvadra/hephaestus/internal/toolkit"
)

// FileAccessConfig controls the paths exposed through tools that operate on
// the filesystem. A nil SharedRoots uses the system temporary directory; a
// non-nil slice replaces that default, which is useful for tests and embedded
// callers.
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
