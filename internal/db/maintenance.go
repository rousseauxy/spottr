package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// MaintenanceStats reports what a maintenance pass removed.
type MaintenanceStats struct {
	SpotsDeleted     int64
	NZBsEvicted      int64
	ImagesEvicted    int64
	CacheBytesBefore int64
	CacheBytesAfter  int64
}

// DeleteSpotsOlderThan removes spots posted before the cutoff, along with their
// cached NZB blobs. Without this the database grows without bound — Spotweb has
// a retention sweep, SpotTR previously had nothing.
//
// Deletion is chunked so a first run against a large backlog does not hold the
// write lock for the whole pass.
func (db *DB) DeleteSpotsOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	const chunk = 5000
	var total int64
	defer db.lockWrites()()

	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}

		// Drop the cached NZBs first; once the spot row is gone we can no
		// longer resolve which blobs belonged to it.
		if _, err := db.ExecContext(ctx, `
			DELETE FROM nzbs WHERE message_id IN (
				SELECT nzb_id FROM spots
				WHERE posted_at < ? AND nzb_id != ''
				LIMIT ?
			)`, cutoff.Unix(), chunk); err != nil {
			return total, fmt.Errorf("evict nzbs: %w", err)
		}

		res, err := db.ExecContext(ctx, `
			DELETE FROM spots WHERE id IN (
				SELECT id FROM spots WHERE posted_at < ? LIMIT ?
			)`, cutoff.Unix(), chunk)
		if err != nil {
			return total, fmt.Errorf("delete spots: %w", err)
		}
		n, _ := res.RowsAffected()
		total += n
		if n < chunk {
			return total, nil
		}
	}
}

// CountSpots returns the number of visible spots, for the metrics endpoint.
func (db *DB) CountSpots(ctx context.Context) (int64, error) {
	var n int64
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM spots WHERE moderated = 0`).Scan(&n)
	return n, err
}

// CacheBytes returns the approximate size of the blob caches.
func (db *DB) CacheBytes(ctx context.Context) (int64, error) {
	var nzb, img int64
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(LENGTH(content)), 0) FROM nzbs`).Scan(&nzb); err != nil {
		return 0, err
	}
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(LENGTH(content)), 0) FROM images`).Scan(&img); err != nil {
		return 0, err
	}
	return nzb + img, nil
}

// EvictCacheTo trims the NZB and image caches down to maxBytes, dropping the
// least-recently-fetched entries first. Both caches are pure caches: anything
// evicted is re-fetchable from Usenet on demand.
func (db *DB) EvictCacheTo(ctx context.Context, maxBytes int64) (nzbs, images int64, err error) {
	defer db.lockWrites()()

	total, err := db.CacheBytes(ctx)
	if err != nil {
		return 0, 0, err
	}
	if total <= maxBytes {
		return 0, 0, nil
	}

	// Evict images before NZBs: images are decorative and cheaper to refetch,
	// NZBs are what downloads actually depend on.
	for _, t := range []struct {
		table string
		out   *int64
	}{{"images", &images}, {"nzbs", &nzbs}} {
		for total > maxBytes {
			if err := ctx.Err(); err != nil {
				return nzbs, images, err
			}
			res, execErr := db.ExecContext(ctx, fmt.Sprintf(`
				DELETE FROM %s WHERE message_id IN (
					SELECT message_id FROM %s ORDER BY fetched_at ASC LIMIT 200
				)`, t.table, t.table))
			if execErr != nil {
				return nzbs, images, fmt.Errorf("evict %s: %w", t.table, execErr)
			}
			n, _ := res.RowsAffected()
			if n == 0 {
				break // table exhausted
			}
			*t.out += n

			if total, err = db.CacheBytes(ctx); err != nil {
				return nzbs, images, err
			}
		}
	}
	return nzbs, images, nil
}

// ─── Spotter blacklist / whitelist ───────────────────────────────────────────

// ReplaceSpotterList atomically swaps the contents of one list type.
func (db *DB) ReplaceSpotterList(ctx context.Context, listType, origin string, ids []string) error {
	defer db.lockWrites()()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM spotter_lists WHERE list_type = ?`, listType); err != nil {
		return err
	}

	stmt, err := tx.PrepareContext(ctx, `
		INSERT OR REPLACE INTO spotter_lists (spotter_id, list_type, origin, updated_at)
		VALUES (?, ?, ?, unixepoch())`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, err := stmt.ExecContext(ctx, id, listType, origin); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// BlacklistedSpotters returns the current blacklist as a set.
func (db *DB) BlacklistedSpotters(ctx context.Context) (map[string]struct{}, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT spotter_id FROM spotter_lists WHERE list_type = 'black'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]struct{})
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = struct{}{}
	}
	return out, rows.Err()
}

// DeleteSpotsBySpotter removes every spot from the given spotter ids.
func (db *DB) DeleteSpotsBySpotter(ctx context.Context, ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	var total int64
	const batch = 400
	for start := 0; start < len(ids); start += batch {
		end := start + batch
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[start:end]

		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		res, err := db.ExecContext(ctx, fmt.Sprintf(
			`DELETE FROM spots WHERE spotter_id IN (%s)`, placeholders), args...)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
	}
	return total, nil
}

// ─── Moderation ──────────────────────────────────────────────────────────────

// MarkModerated flags spots as moderator-removed and records the message-ids so
// a later sync pass does not re-insert them.
func (db *DB) MarkModerated(ctx context.Context, messageIDs []string) (int64, error) {
	if len(messageIDs) == 0 {
		return 0, nil
	}
	defer db.lockWrites()()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	ring, err := tx.PrepareContext(ctx,
		`INSERT OR IGNORE INTO moderated_ring (message_id) VALUES (?)`)
	if err != nil {
		return 0, err
	}
	defer ring.Close()

	mark, err := tx.PrepareContext(ctx,
		`UPDATE spots SET moderated = 1 WHERE message_id = ?`)
	if err != nil {
		return 0, err
	}
	defer mark.Close()

	var affected int64
	for _, id := range messageIDs {
		if _, err := ring.ExecContext(ctx, id); err != nil {
			return affected, err
		}
		res, err := mark.ExecContext(ctx, id)
		if err != nil {
			return affected, err
		}
		n, _ := res.RowsAffected()
		affected += n
	}
	return affected, tx.Commit()
}

// SpotOwner returns the spotter id and post time of a spot by message-id.
// An empty spotter id means the spot has not been enriched yet, so its owner
// is unknown.
func (db *DB) SpotOwner(ctx context.Context, messageID string) (string, time.Time, error) {
	t, _, err := db.DisposeTarget(ctx, messageID)
	return t.Owner, t.Posted, err
}

// DisposeTargetInfo is what a withdrawal needs to know about the spot it names.
type DisposeTargetInfo struct {
	Owner     string // spotter id; "" until the spot's full headers have been read
	Posted    time.Time
	Moderated bool
}

// DisposeTarget looks up the spot a withdrawal names; found is false when it is not indexed.
func (db *DB) DisposeTarget(ctx context.Context, messageID string) (DisposeTargetInfo, bool, error) {
	var t DisposeTargetInfo
	var stamp int64
	var moderated int
	err := db.QueryRowContext(ctx,
		`SELECT spotter_id, posted_at, moderated FROM spots WHERE message_id = ?`, messageID,
	).Scan(&t.Owner, &stamp, &moderated)
	if err == sql.ErrNoRows {
		return t, false, nil
	}
	if err != nil {
		return t, false, err
	}
	t.Posted = time.Unix(stamp, 0)
	t.Moderated = moderated == 1
	return t, true, nil
}

// SetSpotOwner records the spotter id (and whether the spot's signature verified) learned from
// a spot's full headers, without touching anything else of the row.
func (db *DB) SetSpotOwner(ctx context.Context, messageID, spotterID string, verified bool) error {
	_, err := db.ExecContext(ctx,
		`UPDATE spots SET spotter_id = ?, verified = ? WHERE message_id = ?`,
		spotterID, boolInt(verified), messageID)
	return err
}

// DeleteControlMessages removes withdrawal-request articles that were indexed
// as if they were spots, before those were recognised and filtered at ingest.
func (db *DB) DeleteControlMessages(ctx context.Context) (int64, error) {
	res, err := db.ExecContext(ctx, `
		DELETE FROM spots
		WHERE title LIKE 'DISPOSE %@%'
		   OR title LIKE 'DELETE %@%'
		   OR title LIKE 'REMOVE %@%'`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// DeleteSpotsByMessageID removes spots named by a validated withdrawal request.
func (db *DB) DeleteSpotsByMessageID(ctx context.Context, messageIDs []string) (int64, error) {
	if len(messageIDs) == 0 {
		return 0, nil
	}
	var total int64
	const batch = 400
	for start := 0; start < len(messageIDs); start += batch {
		end := start + batch
		if end > len(messageIDs) {
			end = len(messageIDs)
		}
		chunk := messageIDs[start:end]

		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		res, err := db.ExecContext(ctx, fmt.Sprintf(
			`DELETE FROM spots WHERE message_id IN (%s)`, placeholders), args...)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
	}
	return total, nil
}

// PruneModeratedRing drops dispose records older than the cutoff so the table
// does not grow forever. Safe because a spot old enough to have aged out of
// Usenet retention will not be re-offered by the server.
func (db *DB) PruneModeratedRing(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := db.ExecContext(ctx,
		`DELETE FROM moderated_ring WHERE stamp < ?`, cutoff.Unix())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}
