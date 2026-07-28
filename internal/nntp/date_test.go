package nntp

import (
	"testing"
	"time"
)

// The header that started this: a two-digit year, which no previous layout
// matched. The old code answered time.Now() and the caller discarded the error,
// so historical spots were stored as if just posted.
func TestParseNNTPDateTwoDigitYear(t *testing.T) {
	got, err := parseNNTPDate("Thu, 12 May 22 21:11:49 UTC")
	if err != nil {
		t.Fatalf("real Date header failed to parse: %v", err)
	}
	want := time.Date(2022, time.May, 12, 21, 11, 49, 0, time.UTC)
	if !got.UTC().Equal(want) {
		t.Errorf("got %v, want %v", got.UTC(), want)
	}
}

func TestParseNNTPDateFormats(t *testing.T) {
	cases := map[string]time.Time{
		"Thu, 12 May 2022 21:11:49 +0000":       time.Date(2022, time.May, 12, 21, 11, 49, 0, time.UTC),
		"Thu, 12 May 2022 21:11:49 UTC":         time.Date(2022, time.May, 12, 21, 11, 49, 0, time.UTC),
		"12 May 2022 21:11:49 +0000":            time.Date(2022, time.May, 12, 21, 11, 49, 0, time.UTC),
		"Thu, 12 May 22 21:11 UTC":              time.Date(2022, time.May, 12, 21, 11, 0, 0, time.UTC),
		"Thu, 12 May 2022 21:11:49 +0000 (UTC)": time.Date(2022, time.May, 12, 21, 11, 49, 0, time.UTC),
		"Sat, 02 Jan 2021 08:04:05 +0000":       time.Date(2021, time.January, 2, 8, 4, 5, 0, time.UTC),
	}
	for in, want := range cases {
		got, err := parseNNTPDate(in)
		if err != nil {
			t.Errorf("parseNNTPDate(%q) error: %v", in, err)
			continue
		}
		if !got.UTC().Equal(want) {
			t.Errorf("parseNNTPDate(%q) = %v, want %v", in, got.UTC(), want)
		}
	}
}

// The critical behaviour change: failure must be visible, not disguised as the
// current time.
func TestParseNNTPDateFailsClosed(t *testing.T) {
	for _, in := range []string{"", "not a date", "yesterday"} {
		got, err := parseNNTPDate(in)
		if err == nil {
			t.Errorf("parseNNTPDate(%q) unexpectedly succeeded: %v", in, got)
			continue
		}
		if !got.IsZero() {
			t.Errorf("parseNNTPDate(%q) returned %v on failure, want the zero time", in, got)
		}
		// Specifically: it must not look like "now".
		if !got.IsZero() && time.Since(got) < time.Minute {
			t.Errorf("parseNNTPDate(%q) fell back to the current time", in)
		}
	}
}
