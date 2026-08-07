package tools

import (
	"fmt"
	"strings"
)

// namedKeys maps human-readable key names to terminal escape/control
// sequences used by send-keys on PTY sessions.
var namedKeys = map[string]string{
	"enter": "\r", "tab": "\t", "escape": "\x1b",
	"up": "\x1b[A", "down": "\x1b[B", "left": "\x1b[D", "right": "\x1b[C",
	"ctrl-c": "\x03", "ctrl-d": "\x04",
}

// encodeKeys expands a space-separated list of named keys into a single
// control sequence.
func encodeKeys(keys []string) (string, error) {
	var output strings.Builder
	for _, key := range keys {
		sequence, ok := namedKeys[strings.ToLower(key)]
		if !ok {
			return "", fmt.Errorf("unknown key %q", key)
		}
		output.WriteString(sequence)
	}
	return output.String(), nil
}
