package tools

import "regexp"

// execDenyPatterns is a tripwire against destructive or delegating shell
// invocations, NOT a security boundary. This platform runs for a single
// trusted local user, and any pattern list can be bypassed (e.g. via
// python -c ...); the list exists to make the most common foot-guns fail
// loudly and to stop accidental destructive commands. It must not be
// relied on to protect against untrusted users.
//
// Patterns are case-insensitive and cover the main rm/del variants (short
// and long flags, including combined -rf and -r -f forms), host-affecting
// commands, disk tools, and shell delegation (`$(...)`, backticks,
// `| sh`, `curl ... | sh`).
var execDenyPatterns = []*regexp.Regexp{
	// TODO: 这肯定要让用户确认啊，规则是限不死的，这里缺的不是 rule repo，是 user interaction
	regexp.MustCompile(`(?i)\brm\b[^;\n|]*\s(?:-(?:r|f|rf|fr|R|Rf|fR)|--(?:recursive|force))`),
	regexp.MustCompile(`(?i)\brmdir\b[^;\n|]*\s(?:-r|--recursive)`),
	regexp.MustCompile(`(?i)\b(del|rd)\s+/[sfq]`),
	regexp.MustCompile(`(?i)\b(shutdown|reboot|poweroff|sudo)\b`),
	regexp.MustCompile(`(?i)\bmkfs\b|\bchown\b|\bchmod\s+-R\b`),
	regexp.MustCompile(`(?i)\bdd\s+[^\n|;]*\bof=`),
	regexp.MustCompile(`(?i)\b(killall|pkill)\b`),
	regexp.MustCompile(`:\(\)\s*\{.*\};\s*:`), // fork bomb
	regexp.MustCompile(`\$\s*\(|` + "`"),      // command substitution
	regexp.MustCompile(`(?i)\|\s*(sh|bash|zsh|dash|cmd|powershell)\b`),
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
