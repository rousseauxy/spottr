package db

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestKindOfMatchesMigrationSQL(t *testing.T) {
	cases := []struct {
		category int
		subD     string
		want     int
	}{
		{0, "d31|z2|", KindComic}, // Suske en Wiske (CBR)
		{0, "d44|z2|", KindBook},
		{0, "z2|", KindBook},
		{0, "d7|d16|z0|", KindMovie},
		{0, "d11|z1|", KindSeries},
		{0, "d23|d75|z3|", KindErotic},
		{0, "d131|z2|", KindBook}, // d131 is not d31
		{0, "d31|", KindNone},     // a genre without a type code
		{0, "", KindNone},
		{1, "z0|", KindNone}, // only the Image category has kinds
	}
	sqldb, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer sqldb.Close()
	for _, c := range cases {
		if got := KindOf(c.category, c.subD); got != c.want {
			t.Errorf("KindOf(%d, %q) = %d, want %d", c.category, c.subD, got, c.want)
		}
		var got int
		q := `SELECT ` + kindCaseSQL + ` FROM (SELECT ? AS category, ? AS sub_cat_d)`
		if err := sqldb.QueryRow(q, c.category, c.subD).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("SQL kind(%d, %q) = %d, want %d", c.category, c.subD, got, c.want)
		}
	}
}
