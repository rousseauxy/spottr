package sync

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/spottr/spottr/internal/config"
	"github.com/spottr/spottr/internal/db"
	"github.com/spottr/spottr/internal/nntp"
	"github.com/spottr/spottr/internal/spotnet"
)

// A spot's owner is unknown from the overview; the withdrawal is applied only after the spot's
// own headers name the same key that signed it.
func TestApplyModerationLooksUpUnknownOwners(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(filepath.Join(t.TempDir(), "spottr.db"), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	posted := time.Date(2026, 4, 28, 17, 11, 0, 0, time.UTC)
	spots := []db.Spot{
		{MessageID: "<own@spot.net>", Title: "own", PostedAt: posted},
		{MessageID: "<theirs@spot.net>", Title: "theirs", PostedAt: posted},
		{MessageID: "<gone@spot.net>", Title: "gone", PostedAt: posted},
		{MessageID: "<late@spot.net>", Title: "late", PostedAt: posted},
	}
	if _, err := database.InsertSpots(ctx, spots); err != nil {
		t.Fatal(err)
	}

	modulus := base64.StdEncoding.EncodeToString([]byte("the poster's modulus"))
	poster := spotnet.SpotterID(modulus)
	key := "<RSAKeyValue><Modulus>" + modulus + "</Modulus><Exponent>AQAB</Exponent></RSAKeyValue>"
	otherKey := "<RSAKeyValue><Modulus>" + base64.StdEncoding.EncodeToString([]byte("someone else")) + "</Modulus><Exponent>AQAB</Exponent></RSAKeyValue>"

	lookups := 0
	head := func(id string) (map[string]string, error) {
		lookups++
		switch id {
		case "<own@spot.net>":
			return map[string]string{"x-user-key": key}, nil
		case "<theirs@spot.net>":
			return map[string]string{"x-user-key": otherKey}, nil
		}
		return nil, nntp.ErrNoSuchArticle
	}

	e := New(&config.Config{ModerationMode: config.ModerationMark}, database, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	cmd := func(target string, after time.Duration) spotnet.ModerationCommand {
		return spotnet.ModerationCommand{Action: "dispose", TargetID: target, Spotter: poster, Verified: true, Stamp: posted.Add(after)}
	}
	st, err := e.applyModeration(ctx, []spotnet.ModerationCommand{
		cmd("<own@spot.net>", 43*time.Minute),
		cmd("<own@spot.net>", 45*time.Minute), // the same withdrawal sent twice
		cmd("<theirs@spot.net>", time.Hour),   // signed by our poster, but not their spot
		cmd("<gone@spot.net>", time.Hour),     // the spot's article has expired
		cmd("<late@spot.net>", 6*24*time.Hour),
		cmd("<unknown@spot.net>", time.Hour),
		{TargetID: "<own@spot.net>", Verified: false},
	}, head)
	if err != nil {
		t.Fatal(err)
	}
	if st.Applied != 1 || st.Rejected != 2 || st.NoTarget != 1 || st.OwnerUnknown != 1 || st.Unverified != 1 {
		t.Errorf("stats %+v", st)
	}
	if lookups != 3 { // own, theirs, gone; the repeat and the late one need no lookup
		t.Errorf("lookups %d, want 3", lookups)
	}
	if info, _, _ := database.DisposeTarget(ctx, "<own@spot.net>"); !info.Moderated || info.Owner != poster {
		t.Errorf("own spot: %+v", info)
	}
	if info, _, _ := database.DisposeTarget(ctx, "<theirs@spot.net>"); info.Moderated {
		t.Error("someone else's spot was withdrawn")
	}

	// A moderator command applies to a spot with an unknown owner, years later, without a lookup.
	if _, err := database.InsertSpots(ctx, []db.Spot{{MessageID: "<spam@spot.net>", Title: "spam", PostedAt: posted}}); err != nil {
		t.Fatal(err)
	}
	lookups = 0
	st, err = e.applyModeration(ctx, []spotnet.ModerationCommand{
		{Action: "dispose", TargetID: "<spam@spot.net>", Verified: true, Moderator: true, Stamp: posted.AddDate(2, 0, 0)},
	}, head)
	if err != nil || st.Applied != 1 || st.ByModerator != 1 || lookups != 0 {
		t.Errorf("moderator command: stats %+v, lookups %d, err %v", st, lookups, err)
	}

	// A broken connection is reported, and no further lookups are made on it.
	if _, err := database.ExecContext(ctx, `UPDATE spots SET spotter_id = '' WHERE message_id = '<theirs@spot.net>'`); err != nil {
		t.Fatal(err)
	}
	broken := errors.New("read: connection reset")
	_, err = e.applyModeration(ctx, []spotnet.ModerationCommand{cmd("<theirs@spot.net>", time.Hour), cmd("<gone@spot.net>", time.Hour)},
		func(string) (map[string]string, error) { return nil, broken })
	if !errors.Is(err, broken) {
		t.Errorf("err %v, want the connection error", err)
	}
}
