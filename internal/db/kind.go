package db

import (
	"fmt"
	"strings"
)

// Kind narrows the Image category (Spotnet's "Beeld", category 0) by its type code in sub_cat_d:
// z0 film, z1 series, z2 book, z3 erotic; a book with genre d31 is a comic. Measured 2026-10-17:
// of 1.86 M image spots 291 k films, 304 k series, 141 k books (29 k comics), 766 k erotic, and
// 361 k older spots without a type code, whose kind is derived from their other codes.
const (
	KindNone   = 0
	KindMovie  = 1
	KindSeries = 2
	KindBook   = 3
	KindComic  = 4
	KindErotic = 5
)

// KindNames are the names the API accepts in ?kind=.
var KindNames = map[string]int{
	"movie": KindMovie, "series": KindSeries, "book": KindBook, "comic": KindComic, "erotic": KindErotic,
}

// Older spots carry no type code. Spotweb derives one from the other codes
// (SpotCategories::createSubcatZ), and so does this:
var (
	// erotic genres: d23-d26 (the old ones) and d72-d89
	eroticGenres = codeRange('d', 23, 26, 72, 89)
	seriesCodes  = []string{"b4", "d11"}  // source TV, genre series
	bookCodes    = []string{"a5", "a11"}  // ePub, PDF
	pictureCodes = []string{"a12", "a13"} // pictures: no kind of their own here
)

func codeRange(letter byte, bounds ...int) []string {
	var out []string
	for i := 0; i+1 < len(bounds); i += 2 {
		for n := bounds[i]; n <= bounds[i+1]; n++ {
			out = append(out, fmt.Sprintf("%c%d", letter, n))
		}
	}
	return out
}

// KindOf derives a spot's kind from its main category and subcategory codes (sub_cat_d holds the
// genres and the type code, "d31|z2|"; the format and source codes come from sub_cat_a/b).
//
// One deliberate difference from Spotweb: there a later code overwrites an earlier one, so
// "d23|b4|" ends up a series. Here an erotic genre always wins, because the kind decides what is
// 18+ and hiding too much is the safer error.
func KindOf(category int, subCatA, subCatB, subCatD string) int {
	if category != 0 {
		return KindNone
	}
	codes := map[string]bool{}
	for _, s := range []string{subCatA, subCatB, subCatD} {
		for _, c := range strings.Split(s, "|") {
			if c != "" {
				codes[c] = true
			}
		}
	}
	any := func(list []string) bool {
		for _, c := range list {
			if codes[c] {
				return true
			}
		}
		return false
	}
	switch {
	case codes["z3"]:
		return KindErotic
	case codes["z2"] && codes["d31"]:
		return KindComic
	case codes["z2"]:
		return KindBook
	case codes["z1"]:
		return KindSeries
	case codes["z0"]:
		return KindMovie
	case codes["z4"]:
		return KindNone // pictures
	}
	// No type code: derive it.
	switch {
	case any(eroticGenres):
		return KindErotic
	case any(seriesCodes):
		return KindSeries
	case any(bookCodes):
		if codes["d31"] {
			return KindComic
		}
		return KindBook
	case any(pictureCodes):
		return KindNone
	}
	return KindMovie
}

// kindCaseSQL is KindOf as SQL, for the migration that fills the column; a test keeps the two
// equal. Codes are stored as "a5|c2|", so "|code|" in "|" || column is an exact match ("d31"
// does not match "d131").
var kindCaseSQL = func() string {
	has := func(col, code string) string {
		return fmt.Sprintf("instr('|' || %s, '|%s|') > 0", col, code)
	}
	anyOf := func(col string, list []string) string {
		parts := make([]string, len(list))
		for i, c := range list {
			parts[i] = has(col, c)
		}
		return "(" + strings.Join(parts, " OR ") + ")"
	}
	books := "(" + anyOf("sub_cat_a", bookCodes) + ")"
	series := "(" + has("sub_cat_b", "b4") + " OR " + has("sub_cat_d", "d11") + ")"
	return `CASE
	WHEN category <> 0 THEN 0
	WHEN ` + has("sub_cat_d", "z3") + ` THEN 5
	WHEN ` + has("sub_cat_d", "z2") + ` AND ` + has("sub_cat_d", "d31") + ` THEN 4
	WHEN ` + has("sub_cat_d", "z2") + ` THEN 3
	WHEN ` + has("sub_cat_d", "z1") + ` THEN 2
	WHEN ` + has("sub_cat_d", "z0") + ` THEN 1
	WHEN ` + has("sub_cat_d", "z4") + ` THEN 0
	WHEN ` + anyOf("sub_cat_d", eroticGenres) + ` THEN 5
	WHEN ` + series + ` THEN 2
	WHEN ` + books + ` AND ` + has("sub_cat_d", "d31") + ` THEN 4
	WHEN ` + books + ` THEN 3
	WHEN ` + anyOf("sub_cat_a", pictureCodes) + ` THEN 0
	ELSE 1 END`
}()
