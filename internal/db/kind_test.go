package db

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestKindOfMatchesMigrationSQL(t *testing.T) {
	cases := []struct {
		category int
		a, b, d  string
		want     int
	}{
		{0, "a5|", "b3|", "d31|z2|", KindComic}, // Suske en Wiske (CBR)
		{0, "", "", "d44|z2|", KindBook},
		{0, "", "", "z2|", KindBook},
		{0, "", "", "d7|d16|z0|", KindMovie},
		{0, "", "", "d11|z1|", KindSeries},
		{0, "", "", "d23|d75|z3|", KindErotic},
		{0, "", "", "d131|z2|", KindBook}, // d131 is not d31
		{0, "a12|", "", "z4|", KindNone},  // pictures
		// Older spots without a type code, derived as Spotweb does:
		{0, "a2|", "", "d23|", KindErotic}, // a 2016 erotic spot that showed in every listing
		{0, "", "", "d89|", KindErotic},
		{0, "", "b4|", "d23|", KindErotic}, // an erotic genre wins over the TV source
		{0, "a9|", "b4|", "", KindSeries},
		{0, "", "", "d11|", KindSeries},
		{0, "a11|", "", "d31|", KindComic},
		{0, "a5|", "", "", KindBook},
		{0, "a13|", "", "", KindNone},
		{0, "a9|", "", "d2|", KindMovie},
		{0, "", "", "", KindMovie},
		{1, "a0|", "", "z3|", KindNone}, // Audio: z3 is the audiobook
	}
	sqldb, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer sqldb.Close()
	for _, c := range cases {
		if got := KindOf(c.category, c.a, c.b, c.d); got != c.want {
			t.Errorf("KindOf(%d, %q, %q, %q) = %d, want %d", c.category, c.a, c.b, c.d, got, c.want)
		}
		var got int
		q := `SELECT ` + kindCaseSQL + ` FROM (SELECT ? AS category, ? AS sub_cat_a, ? AS sub_cat_b, ? AS sub_cat_d)`
		if err := sqldb.QueryRow(q, c.category, c.a, c.b, c.d).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("SQL kind(%d, %q, %q, %q) = %d, want %d", c.category, c.a, c.b, c.d, got, c.want)
		}
	}
}
