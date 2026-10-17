package api

import (
	"testing"

	"github.com/spottr/spottr/internal/config"
	"github.com/spottr/spottr/internal/db"
)

// 18+ spots show only on the 18+ filter, and only while the server allows them.
func TestIncludeAdult(t *testing.T) {
	cases := []struct {
		allows bool
		kinds  []int
		want   bool
	}{
		{true, []int{db.KindErotic}, true},
		{false, []int{db.KindErotic}, false}, // the filter does not override the server
		{true, nil, false},                   // All, and any search outside the 18+ filter
		{true, []int{db.KindMovie}, false},
		{true, []int{db.KindMovie, db.KindErotic}, false}, // never mixed into another kind
	}
	for _, c := range cases {
		if got := includeAdult(c.allows, c.kinds); got != c.want {
			t.Errorf("includeAdult(%v, %v) = %v, want %v", c.allows, c.kinds, got, c.want)
		}
	}
}

func TestHiddenAdult(t *testing.T) {
	adult, plain := &db.Spot{IsAdult: true}, &db.Spot{}
	off := &Handler{cfg: &config.Config{AllowAdult: false}}
	on := &Handler{cfg: &config.Config{AllowAdult: true}}
	if !off.hiddenAdult(adult) || off.hiddenAdult(plain) || on.hiddenAdult(adult) {
		t.Error("an 18+ spot must be hidden by id exactly when the server does not allow 18+ spots")
	}
}
