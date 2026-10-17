package sync

import (
	"context"
	"fmt"
	"time"

	"github.com/spottr/spottr/internal/config"
	"github.com/spottr/spottr/internal/spotnet"
)

// DisposeSweep applies the withdrawals of the past, once.
//
// Until 2026-10-17 withdrawals were dropped at ingest without being applied, so every spot a
// poster withdrew (a typo, a duplicate, a broken upload: 6.5 % of the articles in free.pt) is
// still listed. The backfill could not have applied them either: it walks backwards, so it reads
// a withdrawal before the older spot it names is indexed.
//
// The sweep walks the group FORWARD from the oldest indexed article up to the forward sync's
// watermark (the live sync applies everything after that), reads only overviews, and applies
// the withdrawals it finds with the same checks as the live sync. Progress is checkpointed after
// every chunk; once it reaches the watermark it is done for good.
type DisposeSweep struct {
	*Engine
}

const sweepPause = 2 * time.Second

// Start runs the sweep until it is done or ctx is cancelled. Nothing to do with moderation off.
func (d *DisposeSweep) Start(ctx context.Context) {
	if d.cfg.ModerationMode == config.ModerationOff {
		return
	}
	for _, group := range spotnet.SpotnetGroups() {
		if err := d.sweepGroup(ctx, group); err != nil && ctx.Err() == nil {
			d.log.Error("withdrawal sweep failed", "group", group, "err", err)
		}
	}
}

func (d *DisposeSweep) sweepGroup(ctx context.Context, group string) error {
	total := ModerationStats{}
	failures := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		done, st, err := d.sweepChunk(ctx, group)
		if err != nil {
			// A dropped connection over hours of reading is likely; the chunk was not
			// checkpointed, so retry it. Ten failures in a row is not a hiccup: stop, and the
			// next start resumes from the checkpoint.
			if failures++; failures >= 10 {
				return err
			}
			d.log.Warn("withdrawal sweep chunk failed, retrying", "group", group, "err", err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(30 * time.Second):
			}
			continue
		}
		failures = 0
		total.Applied += st.Applied
		total.Rejected += st.Rejected
		total.NoTarget += st.NoTarget
		total.OwnerLookups += st.OwnerLookups
		total.OwnerUnknown += st.OwnerUnknown
		if done {
			d.log.Info("withdrawal sweep complete", "group", group,
				"applied", total.Applied, "rejected", total.Rejected, "no_target", total.NoTarget,
				"owner_lookups", total.OwnerLookups, "owner_unknown", total.OwnerUnknown)
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(sweepPause):
		}
	}
}

func (d *DisposeSweep) sweepChunk(ctx context.Context, group string) (bool, ModerationStats, error) {
	var st ModerationStats

	end, err := d.db.GetSyncState(ctx, group)
	if err != nil || end == 0 {
		return end == 0, st, err // no forward sync yet: nothing indexed to sweep
	}
	pos, err := d.db.GetDisposeSweepState(ctx, group)
	if err != nil {
		return false, st, err
	}
	if pos == 0 {
		oldest, err := d.db.OldestArticle(ctx)
		if err != nil || oldest == 0 {
			return oldest == 0, st, err
		}
		pos = oldest - 1
	}
	if pos >= end {
		return true, st, nil
	}

	from := pos + 1
	to := from + overviewChunkSize - 1
	if to > end {
		to = end
	}

	conn, err := d.pool.Get(ctx)
	if err != nil {
		return false, st, fmt.Errorf("nntp: %w", err)
	}
	defer conn.Release()
	if _, err := conn.SelectGroup(group); err != nil {
		conn.MarkBroken()
		return false, st, fmt.Errorf("select group: %w", err)
	}
	articles, err := conn.Overview(from, to)
	if err != nil {
		conn.MarkBroken()
		return false, st, fmt.Errorf("overview %d-%d: %w", from, to, err)
	}

	var cmds []spotnet.ModerationCommand
	for _, ai := range articles {
		spot, _, err := spotnet.ParseFromOverview(ai)
		if err != nil {
			continue
		}
		if cmd, ok := spotnet.ParseDispose(
			spot.Title, spot.HeaderPart, spot.Poster, spot.HeaderSig,
			spot.SelfSignedKey, spot.MessageID, spot.KeyID, spot.PostedAt,
		); ok {
			cmds = append(cmds, cmd)
		}
	}

	st, err = d.applyModeration(ctx, cmds, conn.Head)
	if err != nil {
		// Not checkpointed: the chunk is read again after a pause, with a fresh connection.
		conn.MarkBroken()
		return false, st, fmt.Errorf("apply withdrawals %d-%d: %w", from, to, err)
	}
	if err := d.db.SetDisposeSweepState(ctx, group, to); err != nil {
		return false, st, err
	}
	d.log.Info("withdrawal sweep chunk", "group", group, "from", from, "to", to,
		"withdrawals", len(cmds), "applied", st.Applied, "rejected", st.Rejected,
		"no_target", st.NoTarget, "owner_lookups", st.OwnerLookups, "owner_unknown", st.OwnerUnknown,
		"remaining", end-to)
	return to >= end, st, nil
}
