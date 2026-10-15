package nntp

import "testing"

func TestNormalizeMessageIDRejectsInjection(t *testing.T) {
	// Each of these would previously have been interpolated straight into a
	// "BODY %s\r\n" command. Anything carrying CR or LF ends the command early
	// and injects a second one on an authenticated session.
	hostile := []string{
		"abc\r\nQUIT",
		"abc\nPOST",
		"abc\rDATA",
		"abc def",          // bare space splits the command
		"abc<nested>@spot", // bracket confusion
		"",                 // empty
		"   ",              // whitespace only
		"abc\x00def",       // NUL
		"abc\tdef",         // tab
		"héllo@spot.net",   // non-ASCII
	}
	for _, in := range hostile {
		if got, err := NormalizeMessageID(in); err == nil {
			t.Errorf("NormalizeMessageID(%q) = %q, want error", in, got)
		}
	}
}

func TestNormalizeMessageIDAcceptsRealIDs(t *testing.T) {
	cases := map[string]string{
		"qnOg1ep92rI0X2caQoTH4@spot.net":   "<qnOg1ep92rI0X2caQoTH4@spot.net>",
		"<sQbcTfpsjtwdxlpagy8xI@spot.net>": "<sQbcTfpsjtwdxlpagy8xI@spot.net>",
		"  <a@b.c>  ":                      "<a@b.c>",
	}
	for in, want := range cases {
		got, err := NormalizeMessageID(in)
		if err != nil {
			t.Errorf("NormalizeMessageID(%q) unexpected error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("NormalizeMessageID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeMessageIDLengthCap(t *testing.T) {
	long := make([]byte, maxMessageIDLen+10)
	for i := range long {
		long[i] = 'a'
	}
	if _, err := NormalizeMessageID(string(long)); err == nil {
		t.Error("over-long message-id was accepted")
	}
}
