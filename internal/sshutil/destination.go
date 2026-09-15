// Package sshutil holds the small SSH helpers shared by configuration
// loading and the SSH shell backend. It is a leaf package so that
// internal/bootstrap can validate SSH settings without importing the
// application graph.
package sshutil

import "strings"

// ValidDestination reports whether destination is safe to pass to the
// OpenSSH client as a host argument.
func ValidDestination(destination string) bool {
	return destination != "" && !strings.HasPrefix(destination, "-") && strings.IndexFunc(destination, func(r rune) bool {
		return r <= ' ' || r == 0x7f
	}) == -1
}
