package spotnet

import "testing"

func TestDecodeHeaderText(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"ascii", "Plain title", "Plain title"},
		{"utf-8 kept", "PC Gamer is dé autoriteit", "PC Gamer is dé autoriteit"},
		{"latin-1 byte", "PC Gamer is d\xe9 wereldwijde", "PC Gamer is dé wereldwijde"},
		{"latin-1 umlaut", "DJ \xd6tzi - Sternstunden", "DJ Ötzi - Sternstunden"},
		{"cp1252 quotes and euro", "\x93Quoted\x94 \x80 5", "“Quoted” € 5"},
		{"rfc2047 utf-8 base64", "=?UTF-8?B?Q2Fmw6k=?= Noir", "Café Noir"},
		{"rfc2047 latin-1 q", "=?ISO-8859-1?Q?Carr=E9?=", "Carré"},
		{"rfc2047 windows-1252", "=?windows-1252?Q?=93Hi=94?=", "“Hi”"},
		{"broken rfc2047 left alone", "=?bogus", "=?bogus"},
	}
	for _, c := range cases {
		if got := DecodeHeaderText(c.in); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
