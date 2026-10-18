package api

import (
	"reflect"
	"testing"

	"github.com/spottr/spottr/internal/db"
	"github.com/spottr/spottr/internal/spotnet"
)

func TestNewznabFilter(t *testing.T) {
	img := func(k int) db.CatKind { return db.CatKind{Category: spotnet.CatImage, Kind: k} }
	anyCat := func(c int) db.CatKind { return db.CatKind{Category: c, Kind: -1} }
	nothing := []db.CatKind{{Category: -1, Kind: -1}}
	cases := []struct {
		cat       string
		allow     bool
		want      []db.CatKind
		adultOnly bool
	}{
		{"", true, nil, false}, // everything but 18+
		{"5000,5040", true, []db.CatKind{img(db.KindSeries), img(db.KindSeries)}, false}, // Sonarr
		{"2000,2040", false, []db.CatKind{img(db.KindMovie), img(db.KindMovie)}, false},  // Radarr
		{"7030", false, []db.CatKind{img(db.KindComic)}, false},
		{"7000", false, []db.CatKind{img(db.KindBook), img(db.KindComic)}, false},
		{"3000", false, []db.CatKind{anyCat(spotnet.CatAudio)}, false},
		{"6000", true, []db.CatKind{img(db.KindErotic)}, true},      // 18+ alone, allowed
		{"6000", false, nothing, false},                             // 18+ alone, not allowed
		{"2000,6000", true, []db.CatKind{img(db.KindMovie)}, false}, // never mixed in
	}
	for _, c := range cases {
		got, adultOnly := newznabFilter(c.cat, c.allow)
		if !reflect.DeepEqual(got, c.want) || adultOnly != c.adultOnly {
			t.Errorf("newznabFilter(%q, %v) = %v, %v; want %v, %v", c.cat, c.allow, got, adultOnly, c.want, c.adultOnly)
		}
	}
}
