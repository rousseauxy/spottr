package db

import "strings"

// Kind narrows the Image category (Spotnet's "Beeld", category 0) by its type code in sub_cat_d:
// z0 film, z1 series, z2 book, z3 erotic; a book with genre d31 is a comic. Measured 2026-10-17:
// of 1.86 M image spots 291 k films, 304 k series, 141 k books (29 k comics), 766 k erotic, and
// 361 k older spots without a type code (KindNone).
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

// KindOf derives a spot's kind from its main category and sub_cat_d ("d31|z2|").
func KindOf(category int, subCatD string) int {
	if category != 0 {
		return KindNone
	}
	has := func(code string) bool {
		for _, c := range strings.Split(subCatD, "|") {
			if c == code {
				return true
			}
		}
		return false
	}
	switch {
	case has("z3"):
		return KindErotic
	case has("z2") && has("d31"):
		return KindComic
	case has("z2"):
		return KindBook
	case has("z1"):
		return KindSeries
	case has("z0"):
		return KindMovie
	}
	return KindNone
}

// kindCaseSQL is KindOf as SQL, for the migration that fills the column; a test keeps the two
// equal. sub_cat_d is a list of codes each followed by "|", so "|code|" in "|" || sub_cat_d is an
// exact match ("d31" does not match "d131").
const kindCaseSQL = `CASE
	WHEN category <> 0 THEN 0
	WHEN instr('|' || sub_cat_d, '|z3|') > 0 THEN 5
	WHEN instr('|' || sub_cat_d, '|z2|') > 0 AND instr('|' || sub_cat_d, '|d31|') > 0 THEN 4
	WHEN instr('|' || sub_cat_d, '|z2|') > 0 THEN 3
	WHEN instr('|' || sub_cat_d, '|z1|') > 0 THEN 2
	WHEN instr('|' || sub_cat_d, '|z0|') > 0 THEN 1
	ELSE 0 END`
