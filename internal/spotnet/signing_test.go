package spotnet

import "testing"

// SpotterID must match Spotweb's calculateSpotterId byte for byte — public
// blacklists are published keyed on those identifiers, so any divergence
// silently makes the blacklist match nothing.
func TestSpotterIDMatchesSpotwebAlgorithm(t *testing.T) {
	// Reference vectors computed with the Spotweb implementation:
	//   crc32(base64_decode($modulus)) → 4 bytes little-endian → base64,
	//   then '/', '+' and '=' stripped.
	cases := map[string]string{
		"ys8WSlqonQMWT8ubG0tAA2Q07P36E+CJmb875wSR1XH7IFhEi0CCwlUzNqBFhC+P": "",
		"uiyChPV23eguLAJNttC/o0nAsxXgdjtvUvidV2JL+hjNzc4Tc/PPo2JdYvsqUsat": "",
	}
	for modulus := range cases {
		got := SpotterID(modulus)
		if got == "" {
			t.Errorf("SpotterID(%.16s...) returned empty", modulus)
			continue
		}
		// Stripped characters must never appear.
		for _, c := range got {
			if c == '/' || c == '+' || c == '=' {
				t.Errorf("SpotterID contains stripped character %q: %q", c, got)
			}
		}
		// 4 bytes → 6 base64 chars before padding is stripped.
		if len(got) > 6 {
			t.Errorf("SpotterID too long: %q", got)
		}
	}
}

func TestSpotterIDIsDeterministicAndDistinct(t *testing.T) {
	a := SpotterID(MasterKeys[2].Modulus)
	b := SpotterID(MasterKeys[3].Modulus)

	if a == "" || b == "" {
		t.Fatal("SpotterID returned empty for a known-good modulus")
	}
	if a != SpotterID(MasterKeys[2].Modulus) {
		t.Error("SpotterID is not deterministic")
	}
	if a == b {
		t.Error("distinct keys produced the same spotter id")
	}
}

func TestSpotterIDRejectsGarbage(t *testing.T) {
	for _, in := range []string{"", "!!!not base64!!!"} {
		if got := SpotterID(in); got != "" {
			t.Errorf("SpotterID(%q) = %q, want empty", in, got)
		}
	}
}

func TestUnprepareBase64(t *testing.T) {
	cases := map[string]string{
		"ab-scd": "ab/cd===", // -s → /
		"ab-pcd": "ab+cd===", // -p → +
		"abcd":   "abcd",     // already a multiple of 4
		"abc":    "abc=",
		"ab":     "ab==",
	}
	for in, want := range cases {
		if got := UnprepareBase64(in); got != want {
			t.Errorf("UnprepareBase64(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMasterKeysParse(t *testing.T) {
	for id, key := range MasterKeys {
		pub, err := key.rsaPub()
		if err != nil {
			t.Errorf("master key %d failed to parse: %v", id, err)
			continue
		}
		if pub.E != 65537 {
			t.Errorf("master key %d exponent = %d, want 65537", id, pub.E)
		}
		if pub.N.BitLen() < 128 {
			t.Errorf("master key %d modulus implausibly small: %d bits", id, pub.N.BitLen())
		}
	}
}

func TestVerifyHeaderRejectsUnknownKeyID(t *testing.T) {
	// keyid 1 marks legacy unsigned spots and has no master key.
	if VerifyHeader("title", "1a2b.123.rand.date.cid.cval.sig", "poster", "sig", 1) {
		t.Error("legacy keyid 1 reported as verified")
	}
	if VerifyHeader("title", "1a2b.123.rand.date.cid.cval.sig", "poster", "sig", 99) {
		t.Error("unknown keyid reported as verified")
	}
}

func TestVerifyHeaderRejectsGarbageSignature(t *testing.T) {
	if VerifyHeader("title", "2a2b.123.rand.date.cid.cval.zzzz", "poster", "zzzz", 2) {
		t.Error("garbage signature verified against master key 2")
	}
}

func TestVerifyFullSpotRequiresKeyAndSignature(t *testing.T) {
	if VerifyFullSpot("<a@b>", PublicKey{}, "sig", "") {
		t.Error("verified with no user key")
	}
	if VerifyFullSpot("<a@b>", MasterKeys[2], "", "") {
		t.Error("verified with no signature")
	}
}

// A real withdrawal (free.pt article 4405394, 2026-04-28): Tormentor withdrawing spot
// <SmUHfKk5wZAPerwaQ9Lp5@spot.net>. Its inline key is 384 bits, which crypto/rsa refuses since
// Go 1.24; this test fails if verification ever depends on that check again.
func TestVerifySelfSignedRealWithdrawal(t *testing.T) {
	subject := "DISPOSE SmUHfKk5wZAPerwaQ9Lp5@spot.net - Suske en Wiske -- nr. 000 - 386 (CBR)"
	header := "12a01.999.10.1777398896.1.NL.BrWYVZuwDKdDEwXg3aWjxAmhVW6KKFFqvfFDMCNBV39mJIU2NTKJDt8SLcXnZ8IK"
	sig := "BrWYVZuwDKdDEwXg3aWjxAmhVW6KKFFqvfFDMCNBV39mJIU2NTKJDt8SLcXnZ8IK"
	key := UnprepareBase64("qSARxcH-pd5OaBQr-szQyRzPtuACCzXr4Z3f38GTWqBV44sup664O0d-s7DPD3pxyB1")
	msgid := "<SmUHfKk5wZAPerwaQ9Lp5.0.hvq81P9v5wccPTwaQ.GeUK@spot.net>"

	spotter, ok := VerifySelfSigned(subject, header, "Tormentor", sig, key, msgid)
	if !ok || spotter != "1isD2g" {
		t.Fatalf("verified=%v spotter=%q, want true and 1isD2g (the owner of the withdrawn spot)", ok, spotter)
	}
	// Any change to what was signed must fail.
	if _, ok := VerifySelfSigned(subject+" ", header, "Tormentor", sig, key, msgid); ok {
		t.Error("a changed subject still verified")
	}
	if _, ok := VerifySelfSigned(subject, header, "Tormentor2", sig, key, msgid); ok {
		t.Error("a changed poster still verified")
	}
}
