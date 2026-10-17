package sync

import (
	"context"
	"errors"

	"github.com/spottr/spottr/internal/config"
	"github.com/spottr/spottr/internal/nntp"
	"github.com/spottr/spottr/internal/spotnet"
)

// headFunc reads an article's headers by message-id, on a connection the caller already holds.
type headFunc func(messageID string) (map[string]string, error)

// ModerationStats counts what happened to a batch of withdrawals.
type ModerationStats struct {
	Applied      int64 // spots hidden (mark) or removed (delete)
	Rejected     int   // signed by someone other than the owner, or outside the time window
	NoTarget     int   // the named spot is not indexed
	Unverified   int   // the withdrawal's own signature did not check out
	OwnerLookups int   // spots whose owner was read from Usenet for this
	OwnerUnknown int   // owner could not be learned (article gone, no X-User-Key)
	ByModerator  int   // applied on the network moderator's signature (counted in Applied too)
}

// applyModeration honours withdrawal requests whose ownership checks out.
//
// A verified signature is not sufficient on its own: Spotnet withdrawals are self-signed, so
// anyone can produce one that verifies. The request is only applied when the signing spotter
// also owns the target spot and the request is within the allowed window (spotnet.ValidateDispose).
//
// A spot's owner is the key in its X-User-Key header, which the overview does not carry; until
// 2026-10-17 an unknown owner meant every withdrawal was skipped (6.5 % of the articles in free.pt
// are withdrawals). Now the target's headers are read once, on the caller's connection, and the
// owner is stored. Trusting that header is safe: only the holder of that key can sign the
// withdrawal, so nobody can withdraw a spot that is not theirs.
//
// MODERATION_MODE picks the consequence: "mark" hides the spot but keeps the row (reversible),
// "delete" removes it outright as Spotweb does. Either way the message-id is recorded so a later
// pass does not re-insert it. The returned error is a connection failure during an owner lookup;
// the caller should drop the connection.
func (e *Engine) applyModeration(ctx context.Context, cmds []spotnet.ModerationCommand, head headFunc) (ModerationStats, error) {
	var st ModerationStats
	if e.cfg.ModerationMode == config.ModerationOff {
		return st, nil
	}

	var targets []string
	queued := map[string]bool{}
	var lookupErr error
	for _, cmd := range cmds {
		if !cmd.Verified {
			st.Unverified++
			continue
		}
		if queued[cmd.TargetID] {
			continue // the same withdrawal twice in one batch (posters do send it twice)
		}
		t, found, err := e.db.DisposeTarget(ctx, cmd.TargetID)
		if err != nil {
			return st, err
		}
		if !found {
			st.NoTarget++
			continue
		}
		if t.Moderated {
			continue // already withdrawn (posters often send the same withdrawal twice)
		}
		if cmd.Moderator {
			// Signed with the network's moderator key: no owner, no time limit (Spotweb).
			targets = append(targets, cmd.TargetID)
			queued[cmd.TargetID] = true
			st.ByModerator++
			continue
		}
		if !spotnet.WithinDisposeWindow(cmd, t.Posted) {
			st.Rejected++
			continue
		}

		owner := t.Owner
		if owner == "" && head != nil && lookupErr == nil {
			st.OwnerLookups++
			owner, lookupErr = e.lookupOwner(ctx, cmd.TargetID, head)
			if lookupErr != nil && errors.Is(lookupErr, nntp.ErrNoSuchArticle) {
				lookupErr = nil // the spot's article has expired: owner stays unknown
			}
		}
		if owner == "" {
			st.OwnerUnknown++
			continue
		}
		if !spotnet.ValidateDispose(cmd, owner, t.Posted) {
			st.Rejected++
			e.log.Warn("dispose rejected: not the spot's owner",
				"target", cmd.TargetID, "signer", cmd.Spotter, "owner", owner)
			continue
		}
		targets = append(targets, cmd.TargetID)
		queued[cmd.TargetID] = true
	}

	if len(targets) > 0 {
		var err error
		switch e.cfg.ModerationMode {
		case config.ModerationDelete:
			if st.Applied, err = e.db.DeleteSpotsByMessageID(ctx, targets); err == nil {
				_, err = e.db.MarkModerated(ctx, targets)
			}
		case config.ModerationMark:
			st.Applied, err = e.db.MarkModerated(ctx, targets)
		}
		if err != nil {
			return st, err
		}
	}
	return st, lookupErr
}

// lookupOwner reads a spot's headers and stores the spotter id they carry. "" when the article
// has no usable X-User-Key (older spots); an error only when reading failed.
func (e *Engine) lookupOwner(ctx context.Context, messageID string, head headFunc) (string, error) {
	headers, err := head(messageID)
	if err != nil {
		return "", err
	}
	key, err := spotnet.ParseUserKey(headers["x-user-key"])
	if err != nil {
		return "", nil
	}
	owner := spotnet.SpotterID(key.Modulus)
	verified := spotnet.VerifyFullSpot(messageID, key, headers["x-user-signature"], headers["x-xml-signature"])
	if err := e.db.SetSpotOwner(ctx, messageID, owner, verified); err != nil {
		return "", err
	}
	return owner, nil
}
