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
	"errors"
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
	Title       string    `xml:"Title"`
	Description string    `xml:"Description"`
	Image       SpotImage `xml:"Image"`
	Size        string    `xml:"Size"`
	Tag         string    `xml:"Tag"`
	NZB         NZBList   `xml:"NZB"`
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

// ParseFromOverview creates a partial Spot from NNTP OVER data (fast path, no
// body fetch required). Returns an error if the From header doesn't look like
// Spotnet.
//
// The second return value reports whether the header signature checked out
// against the well-known Spotnet master key named by the header's keyid. It is
// deliberately not folded into Spot.Verified: this proves the header was
// produced by a genuine Spotnet client, whereas Spot.Verified (set later during
// enrichment) proves which spotter posted it. Legacy spots use keyid 1 and are
// unverifiable by design, so callers must treat false as "unknown", not "bad".
func ParseFromOverview(ai nntp.ArticleInfo) (*db.Spot, bool, error) {
	from := ai.From

	// Extract everything inside < > from the From header
	ltIdx := strings.LastIndex(from, "<")
	gtIdx := strings.LastIndex(from, ">")
	if ltIdx < 0 || gtIdx <= ltIdx {
		return nil, false, fmt.Errorf("no angle brackets in From: %q", from)
	}
	addr := from[ltIdx+1 : gtIdx] // e.g. "KEY@1a2b.12345.rand.date.cid.cval.sig"

	atIdx := strings.LastIndex(addr, "@")
	if atIdx < 0 {
		return nil, false, fmt.Errorf("no @ in From address: %q", addr)
	}

	headerPart := addr[atIdx+1:] // after @
	fields := strings.Split(headerPart, ".")
	if len(fields) < 6 {
		return nil, false, fmt.Errorf("too few dot-fields in From address (%d): %q", len(fields), headerPart)
	}

	// The part before the @ is dot-separated too. On self-signed articles its
	// first element is the poster's own RSA modulus; on ordinary spots it is
	// just a signature. Length tells them apart (see minSelfSignedKeyLen).
	selfSigned := ""
	if before := addr[:atIdx]; before != "" {
		selfSigned = UnprepareBase64(strings.Split(before, ".")[0])
	}

	// fields[0]: [CAT][KEYID][SUBCAT...]
	// CAT is 1-based in wire format (1=Image, 2=Audio, 3=Game, 4=App), subtract 1
	if len(fields[0]) < 2 {
		return nil, false, fmt.Errorf("short cat/keyid field: %q", fields[0])
	}
	category := int(fields[0][0]-'0') - 1
	if category < 0 || category > 3 {
		return nil, false, fmt.Errorf("invalid category %d from %q", category+1, fields[0])
	}
	keyID := int(fields[0][1] - '0')
	isLegacy := keyID == 1

	subCatStr := strings.ToLower(fields[0][2:]) + "!!!" // padding so last subcat is parsed
	subA, subB, subC, subD := parseSubCats(subCatStr)

	filesize, _ := strconv.ParseInt(fields[1], 10, 64)

	posterName := strings.TrimSpace(from[:ltIdx])

	// Parse title and tag from subject
	title, tag := parseSubject(ai.Subject, isLegacy)

	postedAt, err := resolvePostedAt(fields[3], ai.Date)
	if err != nil {
		return nil, false, err
	}

	spot := &db.Spot{
		MessageID:  ai.MessageID,
		ArticleNum: ai.ArticleNum,
		Title:      title,
		Poster:     posterName,
		PostedAt:   postedAt,
		Tag:        tag,
		Category:   category,
		SubCatA:    subA,
		SubCatB:    subB,
		SubCatC:    subC,
		SubCatD:    subD,
		Size:       filesize,
		// Resolved once at parse time into an indexable column. The old
		// `sub_cat_d NOT LIKE '%z3%'` filter ran on every browse query and
		// could not use an index.
		IsAdult: IsAdultSubCat(subD),
	}

	// fields[len-1] is the header signature; the master key is chosen by keyid.
	headerVerified := VerifyHeader(title, headerPart, posterName, fields[len(fields)-1], keyID)
	spot.KeyID = keyID
	spot.HeaderPart = headerPart
	spot.HeaderSig = fields[len(fields)-1]
	spot.SelfSignedKey = selfSigned

	return spot, headerVerified, nil
}

// spotnetEpoch predates Spotnet itself; anything earlier is a parsing artifact
// rather than a real post date.
var spotnetEpoch = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// maxClockSkew allows for posters with a slightly fast clock while still
// rejecting timestamps far in the future, which would otherwise pin a spot to
// the top of every date-sorted listing forever.
const maxClockSkew = 48 * time.Hour

// resolvePostedAt determines when a spot was posted.
//
// The Spotnet From header carries its own unix timestamp, and it sits inside
// the portion covered by the header signature — so on signed spots it is
// tamper-evident, and unlike the free-form Date header it needs no format
// guessing. It is therefore preferred, with the NNTP Date header as fallback.
//
// If neither yields a plausible time we return an error rather than inventing
// one. Defaulting to "now" is what made historical spots appear as if they had
// just been posted.
func resolvePostedAt(stampField string, headerDate time.Time) (time.Time, error) {
	if unix, err := strconv.ParseInt(strings.TrimSpace(stampField), 10, 64); err == nil && unix > 0 {
		if t := time.Unix(unix, 0).UTC(); plausibleSpotTime(t) {
			return t, nil
		}
	}
	if plausibleSpotTime(headerDate) {
		return headerDate.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("no usable post date (stamp=%q, header=%v)", stampField, headerDate)
}

func plausibleSpotTime(t time.Time) bool {
	if t.IsZero() {
		return false
	}
	return t.After(spotnetEpoch) && t.Before(time.Now().Add(maxClockSkew))
}

// IsAdultSubCat reports whether a parsed sub-category D string carries the
// Spotnet 18+ marker (z3).
func IsAdultSubCat(subD string) bool {
	for _, part := range strings.Split(subD, "|") {
		if strings.EqualFold(strings.TrimSpace(part), "z3") {
			return true
		}
	}
	return false
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

// EnrichFromHeaders fills a spot from a full NNTP header block.
//
// Beyond the spot XML this also resolves the spotter's identity: X-User-Key
// carries the poster's RSA public key, from which SpotterID is derived and
// against which X-User-Signature is checked. Both are needed before any
// blacklist can mean anything — without a verified key, a spotter id is just a
// string the poster chose.
func EnrichFromHeaders(spot *db.Spot, headers map[string]string) error {
	if userKeyHdr := headers["x-user-key"]; userKeyHdr != "" {
		if key, err := ParseUserKey(userKeyHdr); err == nil {
			spot.SpotterID = SpotterID(key.Modulus)
			spot.Verified = VerifyFullSpot(
				spot.MessageID,
				key,
				headers["x-user-signature"],
				headers["x-xml-signature"],
			)
		}
	}

	rawXML, ok := headers["x-xml"]
	if !ok || strings.TrimSpace(rawXML) == "" {
		return fmt.Errorf("no x-xml header")
	}
	return EnrichFromXML(spot, rawXML)
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

// DefaultMaxNZBBytes caps the decompressed size of an NZB when no explicit
// limit is given.
const DefaultMaxNZBBytes = 64 << 20 // 64 MiB

// ErrNZBTooLarge is returned when an NZB inflates past the configured cap.
var ErrNZBTooLarge = errors.New("spotnet: decompressed NZB exceeds size limit")

// DecodeNZBBody decodes a Spotnet NZB article body using the default limit.
func DecodeNZBBody(body []byte) ([]byte, error) {
	return DecodeNZBBodyLimit(body, DefaultMaxNZBBytes)
}

// DecodeNZBBodyLimit decodes a Spotnet NZB article body.
// The encoding scheme (same as Spotweb's unspecialZipStr + gzinflate):
//  1. Un-escape: =C→\n, =B→\r, =A→\0, =D→=
//  2. raw deflate-inflate the result
//
// If inflate fails and the data looks like XML, it is returned as-is.
//
// The inflate is bounded. A Spotnet NZB is a deflate stream fetched from a
// public newsgroup that anyone may post to, so its expansion ratio is
// attacker-controlled: an unbounded io.ReadAll here turned a single anonymous
// GET into an out-of-memory kill (a zip bomb).
func DecodeNZBBodyLimit(body []byte, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxNZBBytes
	}
	compressed := []byte(unspecialZipStr(string(body)))

	// raw deflate (matches PHP gzinflate — no zlib or gzip header)
	r := flate.NewReader(bytes.NewReader(compressed))
	defer r.Close()

	// Read one byte past the ceiling so hitting it is distinguishable from a
	// stream that merely ends exactly at the limit.
	out, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if int64(len(out)) > maxBytes {
		return nil, ErrNZBTooLarge
	}
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
