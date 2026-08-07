// Package fsutil provides small filesystem helpers shared across packages.
package fsutil

import (
	"os"
	"path/filepath"
	"strings"
)

// ExpandHome expands a leading "~/" (or a bare "~") to the current user's
// home directory, leaving any other path untouched.
func ExpandHome(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~"+string(filepath.Separator)) {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~"+string(filepath.Separator))), nil
}
