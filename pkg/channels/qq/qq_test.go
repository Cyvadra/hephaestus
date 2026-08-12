package qq

import (
	"slices"
	"testing"

	"github.com/Cyvadra/hephaestus/internal/command"
)

func TestSupportedCommands(t *testing.T) {
	want := []string{
		"help", "ping", "stop", "status", "list", "detail",
		"switch", "activate", "deactivate", "clear", "new", "interact",
	}
	if !slices.Equal(command.Names(), want) {
		t.Fatalf("command.Names() = %v, want %v", command.Names(), want)
	}
}
