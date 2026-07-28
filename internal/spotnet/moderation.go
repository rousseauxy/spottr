package spotnet

import (
	"strings"
	"time"
)

// disposeMaxAge bounds how long after a spot a withdrawal may still be
// honoured. Mirrors Spotweb's 5-day window, and limits the damage if a
// spotter's key is ever compromised.
const disposeMaxAge = 5 * 24 * time.Hour

// ModerationCommand is a request to withdraw a spot. It is carried on the
// network as an ordinary article whose title reads
// "<command> <target-message-id> - <original title>".
type ModerationCommand struct {
	Action   string    // delete | dispose | remove
	TargetID string    // message-id of the spot to withdraw
	Spotter  string    // spotter id that signed the request, "" if unverified
	Verified bool      // signature and hashcash both checked out
	Stamp    time.Time // when the request was posted
}

var validModerationCommands = map[string]bool{
	"delete":  true,
	"dispose": true,
	"remove":  true,
}

// LooksLikeModerationCommand reports whether a title has the shape of a
// withdrawal request, without any cryptographic checking.
//
// Used to keep these articles out of the index entirely. They are control
// messages, not content: Spotweb never lists them, and before this check ~18k
// of them were being shown to users as if they were spots.
func LooksLikeModerationCommand(title string) (ModerationCommand, bool) {
	fields := strings.Fields(title)
	if len(fields) < 2 {
		return ModerationCommand{}, false
	}
	action := strings.ToLower(fields[0])
	if !validModerationCommands[action] {
		return ModerationCommand{}, false
	}
	target := strings.TrimSpace(fields[1])
	if target == "" || !strings.Contains(target, "@") {
		return ModerationCommand{}, false
	}
	if !strings.HasPrefix(target, "<") {
		target = "<" + target + ">"
	}
	return ModerationCommand{Action: action, TargetID: target}, true
}

// ParseDispose recognises a withdrawal request and verifies its signature.
//
// Authorisation here is NOT "a moderator said so". A withdrawal is self-signed:
// the article carries its own public key, so a valid signature proves only that
// whoever posted it holds that keypair — and anyone can mint a keypair. What
// makes it meaningful is the spotter id derived from that key, which the caller
// must compare against the spot being withdrawn (ValidateDispose). Acting on a
// verified signature alone would let anybody delete anything.
func ParseDispose(title, header, poster, signatureB64, selfSignedPubKey, messageID string,
	keyID int, stamp time.Time) (ModerationCommand, bool) {

	cmd, ok := LooksLikeModerationCommand(title)
	if !ok {
		return ModerationCommand{}, false
	}
	cmd.Stamp = stamp

	// Withdrawals are self-signed under keyid 2 with the public key inline.
	if keyID != 2 || len(selfSignedPubKey) <= minSelfSignedKeyLen {
		return cmd, true // recognised, but not verifiable — caller must not act on it
	}

	if spotter, verified := VerifySelfSigned(
		title, header, poster, signatureB64, selfSignedPubKey, messageID,
	); verified {
		cmd.Spotter = spotter
		cmd.Verified = true
	}
	return cmd, true
}

// ValidateDispose decides whether a withdrawal may be applied to a spot.
//
// Both conditions are required and neither is cosmetic:
//
//   - the request must be signed by the same spotter that posted the spot, so
//     it is a retraction rather than an attack on someone else's content; and
//   - it must arrive within disposeMaxAge of the spot.
//
// spotSpotterID is frequently empty, because spotter identity only becomes
// known once a spot has been enriched from its full article. An unknown owner
// means the ownership test cannot be performed, so the answer is no. Failing
// closed here costs an un-applied withdrawal; failing open would let a stranger
// delete arbitrary spots.
func ValidateDispose(cmd ModerationCommand, spotSpotterID string, spotStamp time.Time) bool {
	if !cmd.Verified || cmd.Spotter == "" {
		return false
	}
	if spotSpotterID == "" || spotSpotterID != cmd.Spotter {
		return false
	}
	if cmd.Stamp.Before(spotStamp) {
		return false
	}
	return cmd.Stamp.Sub(spotStamp) <= disposeMaxAge
}
