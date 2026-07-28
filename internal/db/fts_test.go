package db

import "testing"

func TestSanitizeFTSQueryStripsOperators(t *testing.T) {
	cases := map[string]string{
		// Plain input is quoted as literal phrases.
		"spongebob":       `"spongebob"`,
		"sponge bob 2025": `"sponge" AND "bob" AND "2025"`,

		// FTS5 operators are neutralised rather than honoured. `a* OR b* OR c*`
		// is valid FTS5 and very expensive to evaluate.
		"a* OR b* OR c*": `"a" AND "or" AND "b" AND "or" AND "c"`,
		"foo OR bar":     `"foo" AND "or" AND "bar"`,
		"foo NEAR bar":   `"foo" AND "near" AND "bar"`,
		`"quoted"`:       `"quoted"`,
		"title:secret":   `"titlesecret"`,
		"foo -bar":       `"foo" AND "bar"`,
		"(a)":            `"a"`,

		// A long enough stem keeps its prefix search; a short one does not,
		// since "ab*" approaches a full index scan.
		"spong*": `"spong"*`,
		"ab*":    `"ab"`,

		// Nothing usable.
		"":      "",
		"   ":   "",
		`"*()"`: "",
	}

	for in, want := range cases {
		if got := SanitizeFTSQuery(in); got != want {
			t.Errorf("SanitizeFTSQuery(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitizeFTSQueryBoundsTermCount(t *testing.T) {
	in := ""
	for i := 0; i < 100; i++ {
		in += "term "
	}
	got := SanitizeFTSQuery(in)

	// maxFTSTerms terms → maxFTSTerms-1 separators.
	wantSeparators := maxFTSTerms - 1
	count := 0
	for i := 0; i+5 <= len(got); i++ {
		if got[i:i+5] == " AND " {
			count++
		}
	}
	if count != wantSeparators {
		t.Errorf("got %d AND separators, want %d (query: %q)", count, wantSeparators, got)
	}
}

func TestSanitizeFTSQueryBoundsTermLength(t *testing.T) {
	long := ""
	for i := 0; i < 200; i++ {
		long += "x"
	}
	got := SanitizeFTSQuery(long)
	// Quoted, so length is the cap plus the two quote characters.
	if len(got) != maxFTSTermLen+2 {
		t.Errorf("term not truncated: got length %d, want %d", len(got), maxFTSTermLen+2)
	}
}
