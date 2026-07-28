package sync

import (
	"context"
	"fmt"
	"time"

	"github.com/spottr/spottr/internal/db"
	"github.com/spottr/spottr/internal/nntp"
	"github.com/spottr/spottr/internal/spotnet"
)

type nntpArticle = nntp.ArticleInfo

func validMessageID(id string) bool { return nntp.ValidMessageID(id) }

// Backfill walks a group's history backwards, from the oldest article we hold
// towards the group's first article.
//
// The forward sync only ever moves ahead of last_article_num, and on a first
// run it starts at (newest - SYNC_LOOKBACK). Everything older than that window
// was therefore permanently out of reach: with the default 500k lookback on a
// 4.4M-article group, roughly the first 3.9M articles — over a decade of spots
// — could never be indexed no matter how long the instance ran.
//
// Progress is checkpointed after every chunk so a restart resumes rather than
// starting over, and the worker runs at a deliberately gentle pace: this is a
// long catch-up competing with live traffic for the same bounded NNTP pool.
type Backfill struct {
	*Engine
}

// backfillPause is the gap between chunks. History is not urgent, and the NNTP
// pool is shared with user-facing requests.
const backfillPause = 2 * time.Second

// Start runs the backfill until history is exhausted or ctx is cancelled.
func (b *Backfill) Start(ctx context.Context) {
	if !b.cfg.BackfillEnabled {
		return
	}
	b.log.Info("backfill worker started", "chunk", overviewChunkSize)

	for _, group := range spotnet.SpotnetGroups() {
		if err := b.backfillGroup(ctx, group); err != nil && ctx.Err() == nil {
			b.log.Error("backfill failed", "group", group, "err", err)
		}
	}
}

func (b *Backfill) backfillGroup(ctx context.Context, group string) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		done, err := b.backfillChunk(ctx, group)
		if err != nil {
			return err
		}
		if done {
			b.log.Info("backfill complete — full history retrieved", "group", group)
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backfillPause):
		}
	}
}

// backfillChunk retrieves one chunk older than the current watermark.
// Returns done=true once the group's first article has been reached.
func (b *Backfill) backfillChunk(ctx context.Context, group string) (bool, error) {
	conn, err := b.pool.Get(ctx)
	if err != nil {
		return false, fmt.Errorf("nntp: %w", err)
	}
	defer conn.Release()

	gi, err := conn.SelectGroup(group)
	if err != nil {
		conn.MarkBroken()
		return false, fmt.Errorf("select group: %w", err)
	}

	oldest, err := b.db.GetBackfillState(ctx, group)
	if err != nil {
		return false, err
	}
	if oldest == 0 {
		// Nothing indexed yet — let the forward sync establish a watermark first.
		return true, nil
	}
	if oldest <= gi.First {
		return true, nil
	}

	to := oldest - 1
	from := to - overviewChunkSize + 1
	if from < gi.First {
		from = gi.First
	}

	articles, err := conn.Overview(from, to)
	if err != nil {
		conn.MarkBroken()
		return false, fmt.Errorf("overview %d-%d: %w", from, to, err)
	}

	inserted, control := b.ingest(ctx, articles)

	// Checkpoint even when the range held nothing usable, otherwise a stretch
	// of non-Spotnet articles would be re-fetched forever.
	if err := b.db.SetBackfillState(ctx, group, from); err != nil {
		return false, err
	}

	b.log.Info("backfill chunk",
		"group", group, "from", from, "to", to,
		"inserted", inserted, "control_messages", control,
		"remaining", from-gi.First,
	)
	return from <= gi.First, nil
}

// ingest parses and stores a batch, applying the same filtering as the forward
// sync. Withdrawal requests are dropped rather than indexed.
func (b *Backfill) ingest(ctx context.Context, articles []nntpArticle) (inserted, control int) {
	var spots []db.Spot
	for _, ai := range articles {
		spot, headerVerified, err := spotnet.ParseFromOverview(ai)
		if err != nil {
			continue
		}
		if _, ok := spotnet.ParseDispose(
			spot.Title, spot.HeaderPart, spot.Poster, spot.HeaderSig,
			spot.SelfSignedKey, spot.MessageID, spot.KeyID, spot.PostedAt,
		); ok {
			control++
			continue
		}
		if b.cfg.VerifySignatures && !headerVerified {
			continue
		}
		if !validMessageID(spot.MessageID) {
			continue
		}
		spots = append(spots, *spot)
	}

	if len(spots) == 0 {
		return 0, control
	}
	n, err := b.db.InsertSpots(ctx, spots)
	if err != nil {
		b.log.Error("backfill insert", "err", err)
		return 0, control
	}
	return n, control
}
