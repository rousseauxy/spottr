package spotnet

import (
	"testing"
	"time"

	"github.com/spottr/spottr/internal/nntp"
)

// Captured verbatim from free.pt. Withdrawal requests are posted as ordinary
// articles under keyid 2, so this is the exact input the moderation gate has to
// classify correctly.
var realDisposeArticle = nntp.ArticleInfo{
	ArticleNum: 4427064,
	MessageID:  "<462CMkeagTkNbJoagLwkq.0.4abUyHLMusMhbJoag.SCQ9@spot.net>",
	Subject:    "DISPOSE 462CMkeagTkNbJoagLwkq@spot.net - Jennifer Donnely - De Rozen Trilogie 01 tm 03 (NL ebook)",
	From:       "solem <pizlw58KFSC94SUdwAIMBzRNxmCZrhEnZb4ihCLAX9p6ViN9s2vf-pwFFZqPKwzFF.VbHcC99Uqt-sFfq0U1q6A6zZTzUv0s-pYps9b4I77sbziqnT-pc2bpqvqa5UURvZ9BN@12a01.999.10.1785246341.1.NL.VMjv4f94bw-ph81Ir2d0KtYBZnb5rx6TXP25xg7fv18hb-sPinslyuVxOiKRdX17Gm>",
	Date:       time.Unix(1785246341, 0),
}

func TestParseRealDisposeArticle(t *testing.T) {
	spot, _, err := ParseFromOverview(realDisposeArticle)
	if err != nil {
		t.Fatalf("failed to parse a real article: %v", err)
	}

	if spot.KeyID != 2 {
		t.Errorf("KeyID = %d, want 2", spot.KeyID)
	}
	if spot.Poster != "solem" {
		t.Errorf("Poster = %q, want %q", spot.Poster, "solem")
	}
	// Self-signed articles carry the poster's own modulus before the '@'.
	if len(spot.SelfSignedKey) <= minSelfSignedKeyLen {
		t.Errorf("SelfSignedKey too short to be a public key: %d bytes", len(spot.SelfSignedKey))
	}

	cmd, ok := ParseDispose(
		spot.Title, spot.HeaderPart, spot.Poster, spot.HeaderSig,
		spot.SelfSignedKey, spot.MessageID, spot.KeyID, spot.PostedAt,
	)
	if !ok {
		t.Fatalf("real DISPOSE article not recognised (title=%q)", spot.Title)
	}
	if cmd.Action != "dispose" {
		t.Errorf("Action = %q, want dispose", cmd.Action)
	}
	if cmd.TargetID != "<462CMkeagTkNbJoagLwkq@spot.net>" {
		t.Errorf("TargetID = %q, want <462CMkeagTkNbJoagLwkq@spot.net>", cmd.TargetID)
	}
}

// Recognition must not depend on the signature: these are control messages and
// must be kept out of the index whether or not they verify.
func TestDisposeIsRecognisedRegardlessOfSignature(t *testing.T) {
	forged := realDisposeArticle
	forged.From = "attacker <AAAA@12a01.999.10.1785246341.1.NL.bogussignature>"

	spot, _, err := ParseFromOverview(forged)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cmd, ok := ParseDispose(
		spot.Title, spot.HeaderPart, spot.Poster, spot.HeaderSig,
		spot.SelfSignedKey, spot.MessageID, spot.KeyID, spot.PostedAt,
	)
	if !ok {
		t.Fatal("forged dispose was not recognised as a control message")
	}
	if cmd.Verified {
		t.Error("a forged signature verified")
	}
	// Unverified means it can never be acted upon.
	if ValidateDispose(cmd, "somebody", forged.Date.Add(-time.Hour)) {
		t.Error("unverified dispose passed validation")
	}
}

// The heart of the authorisation model: a validly self-signed withdrawal is
// still only allowed against the signer's OWN spots. Anyone can mint a keypair,
// so accepting a verified signature alone would let a stranger delete anything.
func TestDisposeOnlyAppliesToOwnSpots(t *testing.T) {
	now := time.Unix(1785246341, 0)
	cmd := ModerationCommand{
		Action:   "dispose",
		TargetID: "<victim@spot.net>",
		Spotter:  "aaaaaa",
		Verified: true,
		Stamp:    now,
	}

	if !ValidateDispose(cmd, "aaaaaa", now.Add(-24*time.Hour)) {
		t.Error("owner's own withdrawal was rejected")
	}
	if ValidateDispose(cmd, "bbbbbb", now.Add(-24*time.Hour)) {
		t.Error("withdrawal accepted against a DIFFERENT spotter's spot")
	}
	// Unknown owner (spot never enriched) must fail closed.
	if ValidateDispose(cmd, "", now.Add(-24*time.Hour)) {
		t.Error("withdrawal accepted against a spot with unknown ownership")
	}
	// Outside the 5-day window.
	if ValidateDispose(cmd, "aaaaaa", now.Add(-6*24*time.Hour)) {
		t.Error("withdrawal accepted outside the time window")
	}
}

// Captured from free.pt. Its Date header uses a two-digit year, which the old
// parser could not read — so this spot was stored dated to the moment it was
// indexed instead of May 2022.
var realMisdatedArticle = nntp.ArticleInfo{
	ArticleNum: 3748251,
	MessageID:  "<VB4BP33chO8FXh9YgLRJy@spot.net>",
	Subject:    "Suske en Wiske Parodie 18+",
	From:       "Rob3333 <7WyCZSFoFvZbKnfppweXf80H2jKE89f9YfnpBzTxo3dO6rh5JYCPoTeX5eFGJjO9.H2jzbrG9wYVBnapaLr-pYaoh-stBdqxM9LN-s-sJlO3-pEl993IptHkioetJTYlydOeGC@17a05b03b10c02d31z02.144078290.20.1652389911.1.NL.1zcw-s-sNEOl8MD64yWqdEmZWzJU0l-pNuplIS6O45M-sT-pZXBkWfsndb-p4QRIEBESKL>",
	// Deliberately zero: simulates the Date header failing to parse, which is
	// exactly what happened in production.
	Date: time.Time{},
}

func TestPostedAtComesFromSignedHeaderStamp(t *testing.T) {
	spot, _, err := ParseFromOverview(realMisdatedArticle)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// 1652389911 is the timestamp embedded in the From header.
	want := time.Unix(1652389911, 0).UTC()
	if !spot.PostedAt.UTC().Equal(want) {
		t.Errorf("PostedAt = %v, want %v", spot.PostedAt.UTC(), want)
	}
	if spot.PostedAt.Year() != 2022 {
		t.Errorf("PostedAt year = %d, want 2022", spot.PostedAt.Year())
	}
	// The bug's signature: dated to roughly now.
	if time.Since(spot.PostedAt) < 24*time.Hour {
		t.Error("spot was dated to approximately now")
	}
}

func TestResolvePostedAtPrefersStampThenHeader(t *testing.T) {
	headerDate := time.Date(2021, time.March, 4, 5, 6, 7, 0, time.UTC)

	// Valid stamp wins.
	got, err := resolvePostedAt("1652389911", headerDate)
	if err != nil || !got.Equal(time.Unix(1652389911, 0).UTC()) {
		t.Errorf("stamp not preferred: got %v, err %v", got, err)
	}

	// Unusable stamp falls back to the header date.
	for _, bad := range []string{"", "0", "abc", "-5"} {
		got, err := resolvePostedAt(bad, headerDate)
		if err != nil || !got.Equal(headerDate) {
			t.Errorf("stamp %q: got %v err %v, want fallback %v", bad, got, err, headerDate)
		}
	}

	// Neither usable → error, never a fabricated time.
	if _, err := resolvePostedAt("abc", time.Time{}); err == nil {
		t.Error("expected an error when no date is available")
	}
}

func TestResolvePostedAtRejectsImplausibleTimes(t *testing.T) {
	// Far future would pin the spot to the top of every listing forever.
	future := time.Now().Add(365 * 24 * time.Hour)
	if _, err := resolvePostedAt("", future); err == nil {
		t.Error("far-future header date accepted")
	}
	// Pre-Spotnet.
	ancient := time.Date(1970, 1, 2, 0, 0, 0, 0, time.UTC)
	if _, err := resolvePostedAt("", ancient); err == nil {
		t.Error("pre-2000 header date accepted")
	}
}

func TestHashcashGate(t *testing.T) {
	// The real dispose article satisfies Spotnet's proof-of-work.
	if !HasValidHashcash(realDisposeArticle.MessageID) {
		t.Error("real dispose article failed the hashcash check")
	}
	if HasValidHashcash("<definitely-not-ground@spot.net>") {
		t.Error("arbitrary message-id passed the hashcash check")
	}
}
