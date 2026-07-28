package db

import "strings"

const (
	// maxFTSTerms bounds how many terms a single query may contain. Each term
	// is a separate index probe, so an unbounded term count is a cheap way to
	// make one request expensive.
	maxFTSTerms = 12
	// maxFTSTermLen bounds a single term.
	maxFTSTermLen = 64
	// minPrefixLen is the shortest prefix a wildcard search may use. "a*" would
	// match a large share of the index and is close to a full scan.
	minPrefixLen = 3
)

// SanitizeFTSQuery converts free-form user input into a safe FTS5 MATCH
// expression.
//
// Raw user input cannot be passed to MATCH directly for two reasons:
//
//  1. FTS5 has its own query syntax (AND/OR/NOT/NEAR, column filters,
//     parentheses, quotes). Malformed input raises a SQL error, which surfaced
//     as a 500; well-formed but hostile input such as `a* OR b* OR c*` is
//     legitimate syntax that is very expensive to evaluate.
//  2. Operators let a caller reshape the query in ways the UI never intended.
//
// Every term is therefore quoted as a literal phrase and joined with AND. A
// single trailing `*` is preserved as a prefix search when the stem is long
// enough to stay selective. The result is always a valid FTS5 expression, or
// "" when nothing usable remains — in which case the caller should skip the
// MATCH clause entirely rather than search for nothing.
func SanitizeFTSQuery(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}

	var terms []string
	for _, field := range strings.Fields(raw) {
		if len(terms) >= maxFTSTerms {
			break
		}

		prefix := strings.HasSuffix(field, "*")
		// Strip every FTS5 metacharacter. Anything left is literal text.
		cleaned := strings.Map(func(r rune) rune {
			switch r {
			case '"', '\'', '(', ')', '*', ':', '^', '-', '+', '\\':
				return -1
			}
			return r
		}, field)

		cleaned = strings.TrimSpace(cleaned)
		if cleaned == "" {
			continue
		}
		if len(cleaned) > maxFTSTermLen {
			cleaned = cleaned[:maxFTSTermLen]
		}

		// A bare AND/OR/NOT/NEAR would still be read as an operator once
		// requoted, so fold them to lowercase literals.
		switch strings.ToUpper(cleaned) {
		case "AND", "OR", "NOT", "NEAR":
			cleaned = strings.ToLower(cleaned)
		}

		term := `"` + cleaned + `"`
		if prefix && len([]rune(cleaned)) >= minPrefixLen {
			term += "*"
		}
		terms = append(terms, term)
	}

	if len(terms) == 0 {
		return ""
	}
	return strings.Join(terms, " AND ")
}
