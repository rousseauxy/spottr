package spotnet

import (
	"io"
	"mime"
	"strings"
	"unicode/utf8"
)

// cp1252High maps bytes 0x80-0x9F of Windows-1252 to Unicode; Latin-1 has control characters
// there. Windows-1252 is what 8-bit Usenet posting tools on Windows write, and above 0x9F it is
// identical to Latin-1. Unassigned bytes (0x81, 0x8D, 0x8F, 0x90, 0x9D) keep their code point.
var cp1252High = [32]rune{
	0x20AC, 0x81, 0x201A, 0x0192, 0x201E, 0x2026, 0x2020, 0x2021,
	0x02C6, 0x2030, 0x0160, 0x2039, 0x0152, 0x8D, 0x017D, 0x8F,
	0x90, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014,
	0x02DC, 0x2122, 0x0161, 0x203A, 0x0153, 0x9D, 0x017E, 0x0178,
}

// Windows1252ToUTF8 decodes Windows-1252 (and so Latin-1) bytes to UTF-8.
func Windows1252ToUTF8(b []byte) string {
	var sb strings.Builder
	sb.Grow(len(b) + len(b)/4)
	for _, c := range b {
		switch {
		case c < 0x80:
			sb.WriteByte(c)
		case c < 0xA0:
			sb.WriteRune(cp1252High[c-0x80])
		default:
			sb.WriteRune(rune(c))
		}
	}
	return sb.String()
}

var wordDecoder = &mime.WordDecoder{
	// mime handles UTF-8, US-ASCII and ISO-8859-1 itself; Windows-1252 is the other one posters use.
	CharsetReader: func(charset string, input io.Reader) (io.Reader, error) {
		switch strings.ToLower(charset) {
		case "windows-1252", "cp1252", "iso-8859-15", "latin1":
			b, err := io.ReadAll(input)
			if err != nil {
				return nil, err
			}
			return strings.NewReader(Windows1252ToUTF8(b)), nil
		}
		return nil, io.EOF
	},
}

// DecodeHeaderText turns a header value (a Subject, a From display name) into UTF-8 for display:
// RFC 2047 encoded words are decoded, and text that is not valid UTF-8 is read as Windows-1252.
// Measured 2026-10-17: 27,657 of 2.49 M stored titles were raw 8-bit (1.1 %), shown as "d�".
func DecodeHeaderText(s string) string {
	if strings.Contains(s, "=?") {
		if d, err := wordDecoder.DecodeHeader(s); err == nil {
			s = d
		}
	}
	if !utf8.ValidString(s) {
		s = Windows1252ToUTF8([]byte(s))
	}
	return s
}
