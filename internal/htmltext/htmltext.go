package htmltext

import (
	"html"
	"regexp"
	"strings"
)

var tags = regexp.MustCompile(`(?s)<[^>]+>`)

func CleanFragment(value string) string {
	return strings.TrimSpace(html.UnescapeString(tags.ReplaceAllString(value, "")))
}
