package nntp

import (
	"fmt"
	"strings"
)

// maxMessageIDLen is the RFC 5536 ceiling for a message-id including brackets.
const maxMessageIDLen = 250

// NormalizeMessageID validates an untrusted message-id and returns it wrapped in
// angle brackets, ready to be interpolated into a BODY/HEAD command.
//
// This is a security boundary, not a convenience. Message-ids reach us from
// Spotnet article XML, which any Usenet poster can write. Commands are framed
// as "%s\r\n", so an id containing CR or LF would terminate the command early
// and let the poster inject arbitrary NNTP commands into our authenticated
// session — including POST on a posting-enabled account.
func NormalizeMessageID(id string) (string, error) {
	id = strings.TrimSpace(id)
	id = strings.TrimPrefix(id, "<")
	id = strings.TrimSuffix(id, ">")

	if id == "" {
		return "", fmt.Errorf("empty message-id")
	}
	if len(id)+2 > maxMessageIDLen {
		return "", fmt.Errorf("message-id too long (%d bytes)", len(id)+2)
	}

	for i := 0; i < len(id); i++ {
		c := id[i]
		// Printable US-ASCII only, excluding the framing and delimiter
		// characters. This is stricter than RFC 5536 permits in theory and
		// matches what Spotnet actually emits in practice.
		if c <= 0x20 || c >= 0x7f {
			return "", fmt.Errorf("message-id contains illegal byte 0x%02x at %d", c, i)
		}
		switch c {
		case '<', '>':
			return "", fmt.Errorf("message-id contains %q", c)
		}
	}

	return "<" + id + ">", nil
}

// ValidMessageID reports whether id is usable without returning the normalized
// form. Useful for filtering lists.
func ValidMessageID(id string) bool {
	_, err := NormalizeMessageID(id)
	return err == nil
}
