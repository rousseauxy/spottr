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
	cfg *config.Config
	db  *db.DB
	log *slog.Logger
}

// Stats is returned after a sync run.
type Stats struct {
	ArticlesChecked int
	SpotsInserted   int
	Duration        time.Duration
	Group           string
}

func New(cfg *config.Config, database *db.DB, log *slog.Logger) *Engine {
	return &Engine{cfg: cfg, db: database, log: log}
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

// RunOnce performs a single sync pass (used by Start and can be triggered manually).
func (e *Engine) RunOnce(ctx context.Context) (*Stats, error) {
	return e.syncGroup(ctx, "free.pt")
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
			"duration", stats.Duration,
		)
	}
	return nil
}

func (e *Engine) syncGroup(ctx context.Context, group string) (*Stats, error) {
	start := time.Now()

	client, err := nntp.Dial(
		e.cfg.NNTPHost,
		e.cfg.NNTPPort,
		e.cfg.NNTPTLS,
		e.cfg.NNTPUser,
		e.cfg.NNTPPass,
	)
	if err != nil {
		return nil, fmt.Errorf("nntp dial: %w", err)
	}
	defer client.Close()

	gi, err := client.SelectGroup(group)
	if err != nil {
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

		articles, err := client.Overview(chunkFrom, chunkTo)
		if err != nil {
			e.log.Warn("overview chunk failed", "from", chunkFrom, "to", chunkTo, "err", err)
			continue
		}

		stats.ArticlesChecked += len(articles)

		var spots []db.Spot
		for _, ai := range articles {
			spot, err := spotnet.ParseFromOverview(ai)
			if err != nil {
				// Not a Spotnet article — skip silently
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
