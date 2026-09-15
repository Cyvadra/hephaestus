package builtin

import (
	"github.com/Cyvadra/hephaestus/internal/transform"
)

func clampRunes(s string, max int) string {
	clamped, _ := transform.TruncateRunes(s, max)
	return clamped
}
