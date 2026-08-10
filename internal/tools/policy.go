package tools

import "regexp"

// execDenyPatterns is a small tripwire against a few highly destructive or
// delegating shell invocations, NOT a security boundary. This platform runs
// for a single trusted local user, and any pattern list can be bypassed
// (e.g. via `python -c ...` or by rephrasing the command), so the list only
// catches the most common foot-guns and must never be relied on to protect
// against untrusted users. Matched commands require user confirmation when
// they run through the interactive agent pipeline.
//
// Current patterns (case-insensitive) are intentionally few:
//   - host-affecting commands: shutdown, reboot, poweroff, sudo
//   - fork bombs, e.g. `:(){ :|:& };:`
//   - command substitution: `$(...)` and backticks
//   - pipe-to-shell delegation: curl|wget|nc|ncat ... | sh|bash|zsh|dash|cmd|powershell
var execDenyPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(shutdown|reboot|poweroff|sudo)\b`),
	regexp.MustCompile(`:\(\)\s*\{.*\};\s*:`), // fork bomb
	regexp.MustCompile(`\$\s*\(|` + "`"),      // command substitution
	regexp.MustCompile(`(?i)\b(curl|wget|nc|ncat)\b.*\|\s*(sh|bash|zsh|dash|cmd|powershell)\b`),
}

// deniedCommand reports whether command trips the deny policy, returning
// the matching pattern for diagnostics.
func deniedCommand(command string) (bool, string) {
	for _, pattern := range execDenyPatterns {
		if pattern.MatchString(command) {
			return true, pattern.String()
		}
	}
	return false, ""
}
