package session

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Cyvadra/hephaestus/internal/textvariant"
)

// SearchMode selects how a search query is matched.
type SearchMode string

const (
	// SearchModePhrase matches the whole query as one substring.
	SearchModePhrase SearchMode = "phrase"
	// SearchModeKeywords requires every keyword to appear, in any order.
	SearchModeKeywords SearchMode = "keywords"
)

const (
	keywordQueryMaxRunes      = 50
	keywordQueryMaxDelimiters = 3
)

var keywordDelimiters = regexp.MustCompile(`[, ]+`)

// SearchQuery is a parsed search query. Each term matches when any of its
// Simplified/Traditional Chinese variants appears.
type SearchQuery struct {
	Mode  SearchMode
	Terms [][]string
}

// ParseSearchQuery switches to keyword mode when the trimmed query is short
// (< 50 runes), contains spaces or ASCII commas, contains no other
// punctuation, and has at most three delimiter runs; it then splits on
// "[, ]+". Any other query is a single phrase. Each term expands to its
// script variants.
func ParseSearchQuery(raw string) SearchQuery {
	query := strings.TrimSpace(raw)
	terms := []string{query}
	mode := SearchModePhrase
	if keywords, ok := splitKeywords(query); ok {
		terms, mode = keywords, SearchModeKeywords
	}
	parsed := SearchQuery{Mode: mode, Terms: make([][]string, len(terms))}
	for i, term := range terms {
		parsed.Terms[i] = textvariant.Variants(term)
	}
	return parsed
}

func splitKeywords(query string) ([]string, bool) {
	if query == "" || utf8.RuneCountInString(query) >= keywordQueryMaxRunes || !strings.ContainsAny(query, " ,") {
		return nil, false
	}
	for _, r := range query {
		if r != ',' && (unicode.IsPunct(r) || unicode.IsSymbol(r) || (unicode.IsSpace(r) && r != ' ')) {
			return nil, false
		}
	}
	keywords := keywordDelimiters.Split(strings.Trim(query, ", "), -1)
	if len(keywords) < 2 || len(keywords)-1 > keywordQueryMaxDelimiters {
		return nil, false
	}
	return keywords, true
}

// allVariants flattens every term's variants, for snippet highlighting.
func (q SearchQuery) allVariants() []string {
	var variants []string
	for _, term := range q.Terms {
		variants = append(variants, term...)
	}
	return variants
}
