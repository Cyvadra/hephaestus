package fsutil

import (
	"path/filepath"
	"testing"
)

func TestExpandHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := ExpandHome("~/Documents/hephaestus-projects")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "Documents", "hephaestus-projects")
	if got != want {
		t.Fatalf("ExpandHome() = %q, want %q", got, want)
	}
}
