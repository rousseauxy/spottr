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
	// Moderator: signed with the network's moderator key (master key 2) rather than by a poster.
	// Such a command applies to any spot, at any time, as in Spotweb.
	Moderator bool
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

// ParseDispose recognises a withdrawal request and verifies its signature. Two kinds exist, both
// under keyid 2, as in Spotweb (Services_Format_Parsing::parseHeader):
//
//   - A moderator command carries no key of its own and is signed with the network's moderator
//     key (master key 2): the moderator bots of the old network (BenBot*, spotWEGnet) removing
//     spam and mislabelled spots. Verified, it applies to any spot (Moderator).
//   - A personal withdrawal carries its poster's own key inline, so a valid signature proves only
//     that whoever posted it holds that keypair, and anyone can mint one. It is meaningful once
//     the caller confirms that key's spotter id owns the target spot (ValidateDispose).
//
// Deliberately NOT copied from Spotweb: it marks a personal withdrawal verified on the hashcash
// alone and leaves the spotter id empty when the signature fails, after which
// removeInvalidDisposes treats it like a moderator command and applies it to any spot. Here a
// personal withdrawal whose signature fails is never verified.
func ParseDispose(title, header, poster, signatureB64, selfSignedPubKey, messageID string,
	keyID int, stamp time.Time) (ModerationCommand, bool) {

	cmd, ok := LooksLikeModerationCommand(title)
	if !ok {
		return ModerationCommand{}, false
	}
	cmd.Stamp = stamp

	if keyID != 2 {
		return cmd, true // recognised, but not a withdrawal Spotnet honours: caller must not act on it
	}
	if len(selfSignedPubKey) <= minSelfSignedKeyLen {
		if VerifyHeader(title, header, poster, signatureB64, 2) {
			cmd.Verified = true
			cmd.Moderator = true
		}
		return cmd, true
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
	if cmd.Verified && cmd.Moderator {
		return true // the network's moderator key: any spot, any time, as in Spotweb
	}
	if !cmd.Verified || cmd.Spotter == "" {
		return false
	}
	if spotSpotterID == "" || spotSpotterID != cmd.Spotter {
		return false
	}
	return WithinDisposeWindow(cmd, spotStamp)
}

// WithinDisposeWindow reports whether a withdrawal came after the spot and within disposeMaxAge
// of it; the cheap check, done before anyone fetches the spot's owner from Usenet.
func WithinDisposeWindow(cmd ModerationCommand, spotStamp time.Time) bool {
	if cmd.Stamp.Before(spotStamp) {
		return false
	}
	return cmd.Stamp.Sub(spotStamp) <= disposeMaxAge
}
