package api

import (
	"testing"

	"github.com/spottr/spottr/internal/config"
	"github.com/spottr/spottr/internal/db"
)

// 18+ spots show in a listing only when the server allows them AND the viewer asks.
func TestIncludeAdult(t *testing.T) {
	cases := []struct {
		allows    bool
		requested string
		want      bool
	}{
		{false, "", false},
		{false, "1", false}, // asking does not override the server
		{true, "", false},   // allowed, but hidden until asked for
		{true, "0", false},
		{true, "1", true},
	}
	for _, c := range cases {
		if got := includeAdult(c.allows, c.requested); got != c.want {
			t.Errorf("includeAdult(%v, %q) = %v, want %v", c.allows, c.requested, got, c.want)
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
