package sync

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/spottr/spottr/internal/config"
	"github.com/spottr/spottr/internal/db"
)

// maintenanceInterval is how often retention and cache eviction run. These are
// housekeeping sweeps, not latency-sensitive work.
const maintenanceInterval = 1 * time.Hour

// maxListBytes caps how much of an external blacklist feed we will read. The
// feed is a third-party URL, so its size is not under our control.
const maxListBytes = 8 << 20 // 8 MiB

// Maintainer runs periodic database housekeeping: retention, blob-cache
// eviction, and refreshing external spotter lists.
type Maintainer struct {
	cfg  *config.Config
	db   *db.DB
	log  *slog.Logger
	http *http.Client
}

func NewMaintainer(cfg *config.Config, database *db.DB, log *slog.Logger) *Maintainer {
	return &Maintainer{
		cfg: cfg,
		db:  database,
		log: log,
		http: &http.Client{
			Timeout: 30 * time.Second,
			// Blacklist URLs are operator-configured, but do not follow
			// redirects indefinitely.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 3 {
					return fmt.Errorf("too many redirects")
				}
				return nil
			},
		},
	}
}

// Start runs housekeeping immediately and then on a fixed interval.
func (m *Maintainer) Start(ctx context.Context) {
	m.log.Info("maintenance engine started",
		"retention_days", m.cfg.RetentionDays,
		"cache_max_bytes", m.cfg.CacheMaxBytes,
	)

	m.runOnce(ctx)

	t := time.NewTicker(maintenanceInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.runOnce(ctx)
		}
	}
}

func (m *Maintainer) runOnce(ctx context.Context) {
	if err := m.refreshSpotterLists(ctx); err != nil {
		m.log.Warn("spotter list refresh failed", "err", err)
	}
	if err := m.applyBlacklist(ctx); err != nil {
		m.log.Warn("blacklist purge failed", "err", err)
	}
	if err := m.applyRetention(ctx); err != nil {
		m.log.Warn("retention failed", "err", err)
	}
	if err := m.evictCache(ctx); err != nil {
		m.log.Warn("cache eviction failed", "err", err)
	}
}

func (m *Maintainer) applyRetention(ctx context.Context) error {
	if m.cfg.RetentionDays <= 0 {
		return nil // keep forever
	}
	cutoff := time.Now().AddDate(0, 0, -m.cfg.RetentionDays)

	n, err := m.db.DeleteSpotsOlderThan(ctx, cutoff)
	if err != nil {
		return err
	}
	if n > 0 {
		m.log.Info("retention sweep", "deleted_spots", n, "older_than", cutoff.Format(time.DateOnly))
	}

	// Dispose records only need to outlive the spots they suppress.
	if pruned, err := m.db.PruneModeratedRing(ctx, cutoff); err == nil && pruned > 0 {
		m.log.Info("pruned moderated ring", "deleted", pruned)
	}
	return nil
}

func (m *Maintainer) evictCache(ctx context.Context) error {
	if m.cfg.CacheMaxBytes <= 0 {
		return nil
	}
	before, err := m.db.CacheBytes(ctx)
	if err != nil {
		return err
	}
	nzbs, images, err := m.db.EvictCacheTo(ctx, m.cfg.CacheMaxBytes)
	if err != nil {
		return err
	}
	if nzbs > 0 || images > 0 {
		after, _ := m.db.CacheBytes(ctx)
		m.log.Info("cache eviction",
			"nzbs_evicted", nzbs, "images_evicted", images,
			"bytes_before", before, "bytes_after", after)
	}
	return nil
}

// spotterListXML matches the format published at spotlist.store, which is the
// feed Spotweb ships with. Both a flat <spotterid> list and entries carrying an
// id attribute are accepted so a mildly different feed still parses.
type spotterListXML struct {
	Entries []struct {
		ID    string `xml:"id,attr"`
		Value string `xml:",chardata"`
	} `xml:",any"`
}

func (m *Maintainer) refreshSpotterLists(ctx context.Context) error {
	for _, l := range []struct {
		url      string
		listType string
	}{
		{m.cfg.BlacklistURL, "black"},
		{m.cfg.WhitelistURL, "white"},
	} {
		if l.url == "" {
			continue
		}
		ids, err := m.fetchSpotterList(ctx, l.url)
		if err != nil {
			m.log.Warn("fetch spotter list", "url", l.url, "type", l.listType, "err", err)
			continue
		}
		if len(ids) == 0 {
			// Never let an empty or unparseable response wipe a good list.
			m.log.Warn("spotter list returned no entries, keeping previous",
				"url", l.url, "type", l.listType)
			continue
		}
		if err := m.db.ReplaceSpotterList(ctx, l.listType, l.url, ids); err != nil {
			return err
		}
		m.log.Info("spotter list refreshed", "type", l.listType, "entries", len(ids))
	}
	return nil
}

func (m *Maintainer) fetchSpotterList(ctx context.Context, url string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "spottr")

	resp, err := m.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxListBytes))
	if err != nil {
		return nil, err
	}

	var doc spotterListXML
	if err := xml.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("parse feed: %w", err)
	}

	seen := make(map[string]struct{})
	var ids []string
	for _, e := range doc.Entries {
		for _, candidate := range []string{e.ID, strings.TrimSpace(e.Value)} {
			if candidate == "" || len(candidate) > 64 {
				continue
			}
			if _, dup := seen[candidate]; dup {
				continue
			}
			seen[candidate] = struct{}{}
			ids = append(ids, candidate)
		}
	}
	return ids, nil
}

// applyBlacklist deletes spots from blacklisted spotters. Spotter ids are only
// known after a spot has been enriched, so this runs as a sweep rather than an
// ingest-time filter.
func (m *Maintainer) applyBlacklist(ctx context.Context) error {
	blocked, err := m.db.BlacklistedSpotters(ctx)
	if err != nil {
		return err
	}
	if len(blocked) == 0 {
		return nil
	}
	ids := make([]string, 0, len(blocked))
	for id := range blocked {
		ids = append(ids, id)
	}
	n, err := m.db.DeleteSpotsBySpotter(ctx, ids)
	if err != nil {
		return err
	}
	if n > 0 {
		m.log.Info("blacklist purge", "deleted_spots", n, "blacklisted_spotters", len(ids))
	}
	return nil
}
