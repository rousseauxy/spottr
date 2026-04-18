package spotnet

// Spotnet header format reference:
// A Spotnet spot is posted as a Usenet article in free.pt or similar groups.
//
// The spot metadata is encoded in the From header email address:
//   From: [Nickname] <[KEY]@[CAT][KEYID][SUBCAT].[SIZE].[RANDOM].[DATE].[CUSTOMID].[CUSTOMVAL].[SIG]>
//
// After the @, fields are dot-separated:
//   fields[0] = [CAT1char][KEYID1char][SUBCAT...] e.g. "1a2b04c00d05"
//   fields[1] = filesize in bytes
//   fields[2] = random
//   fields[3] = date (unix timestamp)
//   fields[4] = custom-id
//   fields[5] = custom-value
//   fields[6] = signature
//
// Subject format (newer spots, keyid != 1):
//   TITLE|TAG
// Subject format (legacy spots, keyid == 1):
//   TITLE|...|TAG
//
// The NZB segment message-ids are in the full article body (X-XML header),
// not available from OVER/XOVER - requires fetching the full article.

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spottr/spottr/internal/db"
	"github.com/spottr/spottr/internal/nntp"
)

// SpotXML mirrors the XML structure embedded in a Spotnet article body.
type SpotXML struct {
	XMLName xml.Name `xml:"Spotnet"`
	Posting Posting  `xml:"Posting"`
}

type Posting struct {
	Title       string   `xml:"Title"`
	Description string   `xml:"Description"`
	Image       SpotImage `xml:"Image"`
	Size        string   `xml:"Size"`
	Tag         string   `xml:"Tag"`
	NZB         NZBList  `xml:"NZB"`
}

type SpotImage struct {
	Segment []string `xml:"Segment"`
	Width   string   `xml:"Width,attr"`
	Height  string   `xml:"Height,attr"`
	URL     string   `xml:",chardata"`
}

type NZBList struct {
	Segments []string `xml:"Segment"`
}

// ParseFromOverview creates a partial Spot from NNTP OVER data (fast path,
// no body fetch required). Returns an error if the From header doesn't look like Spotnet.
func ParseFromOverview(ai nntp.ArticleInfo) (*db.Spot, error) {
	from := ai.From

	// Extract everything inside < > from the From header
	ltIdx := strings.LastIndex(from, "<")
	gtIdx := strings.LastIndex(from, ">")
	if ltIdx < 0 || gtIdx <= ltIdx {
		return nil, fmt.Errorf("no angle brackets in From: %q", from)
	}
	addr := from[ltIdx+1 : gtIdx] // e.g. "KEY@1a2b.12345.rand.date.cid.cval.sig"

	atIdx := strings.LastIndex(addr, "@")
	if atIdx < 0 {
		return nil, fmt.Errorf("no @ in From address: %q", addr)
	}

	headerPart := addr[atIdx+1:] // after @
	fields := strings.Split(headerPart, ".")
	if len(fields) < 6 {
		return nil, fmt.Errorf("too few dot-fields in From address (%d): %q", len(fields), headerPart)
	}

	// fields[0]: [CAT][KEYID][SUBCAT...]
	// CAT is 1-based in wire format (1=Image, 2=Audio, 3=Game, 4=App), subtract 1
	if len(fields[0]) < 2 {
		return nil, fmt.Errorf("short cat/keyid field: %q", fields[0])
	}
	category := int(fields[0][0]-'0') - 1
	if category < 0 || category > 3 {
		return nil, fmt.Errorf("invalid category %d from %q", category+1, fields[0])
	}
	keyID := int(fields[0][1] - '0')
	isLegacy := keyID == 1

	subCatStr := strings.ToLower(fields[0][2:]) + "!!!" // padding so last subcat is parsed
	subA, subB, subC, subD := parseSubCats(subCatStr)

	filesize, _ := strconv.ParseInt(fields[1], 10, 64)

	posterName := strings.TrimSpace(from[:ltIdx])

	// Parse title and tag from subject
	title, tag := parseSubject(ai.Subject, isLegacy)

	spot := &db.Spot{
		MessageID:  ai.MessageID,
		ArticleNum: ai.ArticleNum,
		Title:      title,
		Poster:     posterName,
		PostedAt:   ai.Date,
		Tag:        tag,
		Category:   category,
		SubCatA:    subA,
		SubCatB:    subB,
		SubCatC:    subC,
		SubCatD:    subD,
		Size:       filesize,
	}

	return spot, nil
}

// parseSubject extracts title and tag from the Spotnet article subject line.
// For newer spots (isLegacy=false): "TITLE|TAG"
// For legacy spots (isLegacy=true): "title|...|tag" (last two parts)
func parseSubject(subj string, isLegacy bool) (title, tag string) {
	// Handle RFC 2047 encoded subjects - just strip the encoding markers for now
	if strings.Contains(subj, "=?") {
		subj = stripRFC2047(subj)
	}

	parts := strings.Split(subj, "|")
	if isLegacy {
		if len(parts) >= 2 {
			tag = strings.TrimSpace(parts[len(parts)-1])
			title = strings.TrimSpace(strings.Join(parts[:len(parts)-2], "|"))
			if title == "" {
				title = strings.TrimSpace(parts[0])
			}
		} else {
			title = strings.TrimSpace(subj)
		}
	} else {
		title = strings.TrimSpace(parts[0])
		if len(parts) > 1 {
			tag = strings.TrimSpace(parts[1])
		}
	}
	title = unescapePipe(title)
	tag = unescapePipe(tag)
	return
}

// stripRFC2047 does a best-effort strip of =?charset?encoding?text?= markers.
func stripRFC2047(s string) string {
	// Simple approach: remove =?...?= wrappers
	for strings.Contains(s, "=?") && strings.Contains(s, "?=") {
		start := strings.Index(s, "=?")
		end := strings.Index(s[start:], "?=")
		if end < 0 {
			break
		}
		s = s[:start] + s[start+end+2:]
	}
	return strings.TrimSpace(s)
}

// parseSubCats parses the subcategory string from fields[0][2:].
// Format: sequence of [letter][number(s)] e.g. "a2b4c0d5" or "a02b04c00d05"
// Returns comma-separated values for each subcat type (a/b/c/d).
func parseSubCats(s string) (subA, subB, subC, subD string) {
	valid := map[byte]bool{'a': true, 'b': true, 'c': true, 'd': true, 'z': true}
	tmp := ""
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !isDigit(c) && len(tmp) > 0 {
			if valid[tmp[0]] {
				key := tmp[0]
				num, _ := strconv.Atoi(tmp[1:])
				val := fmt.Sprintf("%c%d|", key, num)
				switch key {
				case 'a':
					subA += val
				case 'b':
					subB += val
				case 'c':
					subC += val
				case 'd', 'z':
					subD += val
				}
			}
			tmp = ""
		}
		if c != '!' {
			tmp += string(c)
		}
	}
	return
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// latin1ToUTF8 converts a Latin-1 (ISO-8859-1) byte slice to valid UTF-8.
// Latin-1 codepoints map 1:1 to Unicode, so each byte becomes its rune.
func latin1ToUTF8(b []byte) []byte {
	runes := make([]rune, len(b))
	for i, c := range b {
		runes[i] = rune(c)
	}
	return []byte(string(runes))
}

// sanitizeXML ensures the XML is valid UTF-8 and strips any encoding
// declaration that would confuse Go's XML parser (which only handles UTF-8/16).
func sanitizeXML(b []byte) []byte {
	if !utf8.Valid(b) {
		b = latin1ToUTF8(b)
	}
	// Strip <?xml ... encoding="..."?> declaration so Go doesn't reject it
	s := string(b)
	if idx := strings.Index(s, "?>"); strings.HasPrefix(s, "<?xml") && idx != -1 {
		s = strings.TrimSpace(s[idx+2:])
		b = []byte(s)
	}
	return b
}

// EnrichFromXML parses the raw Spotnet XML (from X-XML headers) into the spot.
// This is the correct path for modern Spotnet articles where the XML lives in
// article headers (X-XML:), not the body.
func EnrichFromXML(spot *db.Spot, rawXML string) error {
	xmlData := sanitizeXML([]byte(rawXML))
	trimmed := strings.TrimSpace(string(xmlData))
	if trimmed == "" {
		return fmt.Errorf("empty xml")
	}
	var s SpotXML
	if err := xml.Unmarshal([]byte(trimmed), &s); err != nil {
		return fmt.Errorf("xml unmarshal: %w", err)
	}
	if s.Posting.Description != "" {
		spot.Description = s.Posting.Description
	}
	if len(s.Posting.Image.Segment) > 0 {
		spot.ImageURL = "<nntp:" + strings.Join(s.Posting.Image.Segment, "|") + ">"
	} else if s.Posting.Image.URL != "" {
		spot.ImageURL = s.Posting.Image.URL
	}
	if len(s.Posting.NZB.Segments) > 0 {
		spot.NzbID = s.Posting.NZB.Segments[0]
	}
	if s.Posting.Size != "" && spot.Size == 0 {
		spot.Size, _ = strconv.ParseInt(s.Posting.Size, 10, 64)
	}
	return nil
}

// EnrichFromBody parses the yEnc+XML body of a Spotnet article and fills in
// Description and ImageURL on an existing Spot.
func EnrichFromBody(spot *db.Spot, body []byte) error {
	xmlData, err := DecodeBody(body)
	if err != nil {
		// Fall back to treating body as raw text/XML (some posters skip yEnc)
		xmlData = body
	}
	xmlData = sanitizeXML(xmlData)

	// Detect whether content is XML or plain text
	trimmed := strings.TrimSpace(string(xmlData))
	if strings.HasPrefix(trimmed, "<") {
		// XML path
		var s SpotXML
		if err := xml.Unmarshal([]byte(trimmed), &s); err != nil {
			return fmt.Errorf("xml unmarshal: %w", err)
		}
		if s.Posting.Description != "" {
			spot.Description = s.Posting.Description
		}
		if len(s.Posting.Image.Segment) > 0 {
			spot.ImageURL = "<nntp:" + strings.Join(s.Posting.Image.Segment, "|") + ">"
		} else if s.Posting.Image.URL != "" {
			spot.ImageURL = s.Posting.Image.URL
		}
		if len(s.Posting.NZB.Segments) > 0 {
			spot.NzbID = s.Posting.NZB.Segments[0]
		}
		if s.Posting.Size != "" && spot.Size == 0 {
			spot.Size, _ = strconv.ParseInt(s.Posting.Size, 10, 64)
		}
	} else if trimmed != "" {
		// Plain-text body format: "<release title>\n___separator___\n<description>"
		// Strip the first separator line and anything before it, keep what follows.
		lines := strings.Split(trimmed, "\n")
		start := -1
		for i, l := range lines {
			stripped := strings.Trim(l, "_ \t\r-=")
			if stripped == "" && i > 0 {
				start = i + 1
				break
			}
		}
		if start > 0 && start < len(lines) {
			spot.Description = strings.TrimSpace(strings.Join(lines[start:], "\n"))
		} else {
			spot.Description = trimmed
		}
	}

	return nil
}

// unspecialZipStr undoes Spotnet's custom byte-escape encoding used in binary articles.
// =C → \n, =B → \r, =A → \0, =D → =
func unspecialZipStr(s string) string {
	s = strings.ReplaceAll(s, "=C", "\n")
	s = strings.ReplaceAll(s, "=B", "\r")
	s = strings.ReplaceAll(s, "=A", "\x00")
	s = strings.ReplaceAll(s, "=D", "=")
	return s
}

// DecodeImageBody decodes a Spotnet image article body.
// Image articles use the same custom escape encoding as NZBs but are NOT gzip-compressed.
func DecodeImageBody(body []byte) []byte {
	return []byte(unspecialZipStr(string(body)))
}

// DecodeNZBBody decodes a Spotnet NZB article body.
// The encoding scheme (same as Spotweb's unspecialZipStr + gzinflate):
//  1. Un-escape: =C→\n, =B→\r, =A→\0, =D→=
//  2. raw deflate-inflate the result
//
// If inflate fails and the data looks like XML, it is returned as-is.
func DecodeNZBBody(body []byte) ([]byte, error) {
	compressed := []byte(unspecialZipStr(string(body)))

	// raw deflate (matches PHP gzinflate — no zlib or gzip header)
	r := flate.NewReader(bytes.NewReader(compressed))
	defer r.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		// inflate failed — maybe already plain text/NZB
		if bytes.HasPrefix(bytes.TrimSpace(compressed), []byte("<")) {
			return compressed, nil
		}
		return nil, fmt.Errorf("flate inflate: %w", err)
	}
	return out, nil
}

// DecodeBody extracts the base64/yEnc payload from a Spotnet article body.
// Spotnet uses a simplified encoding: the XML is base64-encoded inside a
// =ybegin ... =yend block.
func DecodeBody(body []byte) ([]byte, error) {
	s := string(body)

	begin := strings.Index(s, "=ybegin")
	end := strings.Index(s, "=yend")
	if begin == -1 || end == -1 {
		return nil, fmt.Errorf("no yenc markers")
	}

	// Find line after =ybegin header
	startData := strings.Index(s[begin:], "\n")
	if startData == -1 {
		return nil, fmt.Errorf("no ybegin newline")
	}
	startData += begin + 1

	payload := strings.TrimSpace(s[startData:end])
	// Spotnet uses base64, not actual yEnc encoding
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(payload, "\n", ""))
	if err != nil {
		// Try raw decode if base64 fails
		return nil, fmt.Errorf("base64: %w", err)
	}
	return decoded, nil
}

// sanitizePoster extracts a clean display name from a NNTP From header.
func sanitizePoster(from string) string {
	if idx := strings.Index(from, "<"); idx > 0 {
		return strings.TrimSpace(from[:idx])
	}
	return from
}

// unescapePipe undoes pipe-escaping used in Spotnet subjects.
func unescapePipe(s string) string {
	return strings.ReplaceAll(s, "&#124;", "|")
}

// IsSpotnetGroup returns true if the given newsgroup name is a Spotnet group.
func IsSpotnetGroup(group string) bool {
	return strings.HasPrefix(group, "free.pt") ||
		strings.Contains(group, "spotnet")
}

// SpotnetGroups returns the standard list of Spotnet newsgroups.
func SpotnetGroups() []string {
	return []string{
		"free.pt",
	}
}

// AgeFromPostedAt returns a human-friendly age string.
func AgeFromPostedAt(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
