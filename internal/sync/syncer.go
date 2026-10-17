package sync

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/spottr/spottr/internal/config"
	"github.com/spottr/spottr/internal/db"
	"github.com/spottr/spottr/internal/nntp"
	"github.com/spottr/spottr/internal/spotnet"
)

const (
	// overviewChunkSize is the max range per OVER request.
	// Large ranges can cause server timeouts.
	overviewChunkSize = 5000
)

// Engine handles periodic retrieval of new Spotnet articles.
type Engine struct {
	cfg  *config.Config
	db   *db.DB
	log  *slog.Logger
	pool *nntp.Pool
}

// Stats is returned after a sync run.
type Stats struct {
	ArticlesChecked int
	SpotsInserted   int
	SpotsRejected   int
	SpotsModerated  int
	ControlMessages int
	Duration        time.Duration
	Group           string
}

func New(cfg *config.Config, database *db.DB, log *slog.Logger, pool *nntp.Pool) *Engine {
	return &Engine{cfg: cfg, db: database, log: log, pool: pool}
}

// Start runs the sync loop, triggering immediately and then on the configured interval.
func (e *Engine) Start(ctx context.Context) {
	e.log.Info("sync engine started", "interval", e.cfg.SyncInterval)

	if err := e.run(ctx); err != nil {
		e.log.Error("initial sync failed", "err", err)
	}

	ticker := time.NewTicker(e.cfg.SyncInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			e.log.Info("sync engine stopped")
			return
		case <-ticker.C:
			if err := e.run(ctx); err != nil {
				e.log.Error("sync failed", "err", err)
			}
		}
	}
}

// RunOnce performs a single sync pass over all configured groups.
func (e *Engine) RunOnce(ctx context.Context) error {
	return e.run(ctx)
}

func (e *Engine) run(ctx context.Context) error {
	for _, group := range spotnet.SpotnetGroups() {
		stats, err := e.syncGroup(ctx, group)
		if err != nil {
			e.log.Error("group sync failed", "group", group, "err", err)
			continue
		}
		e.log.Info("sync complete",
			"group", stats.Group,
			"checked", stats.ArticlesChecked,
			"inserted", stats.SpotsInserted,
			"rejected", stats.SpotsRejected,
			"duration", stats.Duration,
		)
	}
	return nil
}

func (e *Engine) syncGroup(ctx context.Context, group string) (*Stats, error) {
	start := time.Now()

	conn, err := e.pool.Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("nntp: %w", err)
	}
	defer conn.Release()

	gi, err := conn.SelectGroup(group)
	if err != nil {
		conn.MarkBroken()
		return nil, fmt.Errorf("select group %s: %w", group, err)
	}

	lastSeen, err := e.db.GetSyncState(ctx, group)
	if err != nil {
		return nil, err
	}

	// On first run, start from (last - lookback) to avoid fetching millions of old articles
	fromArticle := lastSeen + 1
	if lastSeen == 0 {
		fromArticle = gi.Last - int64(e.cfg.SyncLookback)
		if fromArticle < gi.First {
			fromArticle = gi.First
		}
		e.log.Info("first sync, starting from lookback",
			"group", group,
			"from", fromArticle,
			"last", gi.Last,
		)
	}

	if fromArticle > gi.Last {
		return &Stats{Group: group, Duration: time.Since(start)}, nil
	}

	stats := &Stats{Group: group}

	for chunkFrom := fromArticle; chunkFrom <= gi.Last; chunkFrom += overviewChunkSize {
		if ctx.Err() != nil {
			break
		}

		chunkTo := chunkFrom + overviewChunkSize - 1
		if chunkTo > gi.Last {
			chunkTo = gi.Last
		}

		articles, err := conn.Overview(chunkFrom, chunkTo)
		if err != nil {
			// An overview failure leaves unread data on the wire; the
			// connection's protocol state is no longer trustworthy.
			conn.MarkBroken()
			e.log.Warn("overview chunk failed", "from", chunkFrom, "to", chunkTo, "err", err)
			break
		}

		stats.ArticlesChecked += len(articles)

		var spots []db.Spot
		var disposes []spotnet.ModerationCommand
		for _, ai := range articles {
			spot, headerVerified, err := spotnet.ParseFromOverview(ai)
			if err != nil {
				// Not a Spotnet article — skip silently
				continue
			}

			// Withdrawal requests are control messages, not content. They are
			// dropped from the index regardless of MODERATION_MODE — Spotweb
			// never lists them either, and showing them as spots is just noise.
			if cmd, ok := spotnet.ParseDispose(
				spot.Title, spot.HeaderPart, spot.Poster, spot.HeaderSig,
				spot.SelfSignedKey, spot.MessageID, spot.KeyID, spot.PostedAt,
			); ok {
				stats.ControlMessages++
				if e.cfg.ModerationMode != config.ModerationOff && cmd.Verified {
					disposes = append(disposes, cmd)
				}
				continue // never store the command itself as a spot
			}
			// Optional strict mode: only accept spots whose header signature
			// validates against a known Spotnet master key. Off by default
			// because legacy spots (keyid 1) are unsigned by design and would
			// all be discarded.
			if e.cfg.VerifySignatures && !headerVerified {
				stats.SpotsRejected++
				continue
			}
			// The message-id is interpolated into NNTP commands later on, so
			// reject anything malformed at the point of ingestion rather than
			// storing it and hoping every consumer validates.
			if !nntp.ValidMessageID(spot.MessageID) {
				stats.SpotsRejected++
				continue
			}
			spots = append(spots, *spot)
		}

		if len(spots) > 0 {
			n, err := e.db.InsertSpots(ctx, spots)
			if err != nil {
				e.log.Error("insert spots", "err", err)
			} else {
				stats.SpotsInserted += n
			}
		}

		// Applied after the inserts: a spot can be published and withdrawn
		// within the same chunk, and the ring buffer entry must outlive the
		// row so a later pass does not resurrect it.
		if len(disposes) > 0 {
			ms, err := e.applyModeration(ctx, disposes, conn.Head)
			stats.SpotsModerated += int(ms.Applied)
			if err != nil {
				// A failed owner lookup means the connection is no longer trustworthy. Stop
				// before the checkpoint, so the next run reads this chunk (and its
				// withdrawals) again; re-inserting its spots is a no-op.
				conn.MarkBroken()
				e.log.Warn("apply moderation", "from", chunkFrom, "to", chunkTo, "err", err)
				break
			}
		}

		// Update progress checkpoint after each chunk
		if len(articles) > 0 {
			lastNum := articles[len(articles)-1].ArticleNum
			if err := e.db.SetSyncState(ctx, group, lastNum); err != nil {
				e.log.Warn("set sync state", "err", err)
			}
		}
	}

	stats.Duration = time.Since(start)
	return stats, nil
}
