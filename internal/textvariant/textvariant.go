// Package textvariant converts between Simplified and Traditional Chinese
// character by character, so searches can match either script.
package textvariant

import (
	_ "embed"
	"strings"
	"sync"
	"unicode/utf8"
)

var (
	//go:embed STCharacters.txt
	stCharacters string
	//go:embed TSCharacters.txt
	tsCharacters string

	loadOnce        sync.Once
	toTraditional   map[rune][]rune
	toSimplifiedMap map[rune][]rune
)

// parseTable reads an OpenCC character table ("from<TAB>to [alt ...]"),
// keeping every candidate of one-to-many mappings, preferred first.
func parseTable(table string) map[rune][]rune {
	mapping := make(map[rune][]rune, 4096)
	for _, line := range strings.Split(table, "\n") {
		from, to, ok := strings.Cut(line, "\t")
		if !ok || utf8.RuneCountInString(from) != 1 {
			continue
		}
		source, _ := utf8.DecodeRuneInString(from)
		var targets []rune
		changes := false
		for _, field := range strings.Fields(to) {
			if utf8.RuneCountInString(field) != 1 {
				continue
			}
			target, _ := utf8.DecodeRuneInString(field)
			targets = append(targets, target)
			changes = changes || target != source
		}
		if changes {
			mapping[source] = targets
		}
	}
	return mapping
}

func load() {
	loadOnce.Do(func() {
		toTraditional = parseTable(stCharacters)
		toSimplifiedMap = parseTable(tsCharacters)
	})
}

func convert(text string, mapping map[rune][]rune) string {
	return strings.Map(func(r rune) rune {
		if mapped, ok := mapping[r]; ok {
			return mapped[0]
		}
		return r
	}, text)
}

// ToTraditional converts Simplified characters in text to Traditional,
// choosing the preferred candidate of ambiguous characters.
func ToTraditional(text string) string {
	load()
	return convert(text, toTraditional)
}

// ToSimplified converts Traditional characters in text to Simplified,
// choosing the preferred candidate of ambiguous characters.
func ToSimplified(text string) string {
	load()
	return convert(text, toSimplifiedMap)
}

// maxVariants bounds the expansion of ambiguous characters (e.g. 发 → 發/髮)
// so a long query cannot explode into a huge set of search patterns.
const maxVariants = 16

// Variants returns text followed by its distinct Traditional and Simplified
// forms, expanding one-to-many characters into alternative spellings up to
// maxVariants. Text without convertible characters yields just itself.
func Variants(text string) []string {
	load()
	variants := []string{text}
	seen := map[string]bool{text: true}
	add := func(candidate string) {
		if !seen[candidate] && len(variants) < maxVariants {
			seen[candidate] = true
			variants = append(variants, candidate)
		}
	}
	for _, mapping := range []map[rune][]rune{toTraditional, toSimplifiedMap} {
		expanded, truncated := []string{""}, false
		for _, r := range text {
			options := mapping[r]
			if len(options) == 0 {
				options = []rune{r}
			}
			next := make([]string, 0, len(expanded)*len(options))
			for _, prefix := range expanded {
				for _, option := range options {
					if len(next) < maxVariants {
						next = append(next, prefix+string(option))
					} else {
						truncated = true
					}
				}
			}
			expanded = next
		}
		if truncated {
			// Single-character alternates of the preferred conversion come
			// first so a long query still gets every character's other
			// spelling before the cap cuts off the full combinations.
			preferred := convert(text, mapping)
			runes, preferredRunes := []rune(text), []rune(preferred)
			if len(runes) == len(preferredRunes) {
				for i, r := range runes {
					for _, option := range mapping[r][min(1, len(mapping[r])):] {
						alt := append([]rune(nil), preferredRunes...)
						alt[i] = option
						add(string(alt))
					}
				}
			}
		}
		for _, candidate := range expanded {
			add(candidate)
		}
	}
	return variants
}
