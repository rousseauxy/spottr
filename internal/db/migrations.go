package db

import (
	"database/sql"
	"fmt"
	"strings"
)

// migration is one forward-only schema change. Statements run inside a single
// transaction; the version is recorded via PRAGMA user_version so each
// migration applies exactly once.
type migration struct {
	version int
	name    string
	stmts   []string
}

// migrations must be append-only. Never renumber or edit a released entry —
// databases in the field have already recorded the version.
var migrations = []migration{
	{
		version: 1,
		name:    "add spots.is_adult + spots.spotter_id with covering indexes",
		stmts: []string{
			// Adult filtering used `sub_cat_d NOT LIKE '%z3%'`, a leading-wildcard
			// match that forced a full table scan on every single query. Hoist it
			// into a real column so it can be indexed.
			`ALTER TABLE spots ADD COLUMN is_adult INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE spots ADD COLUMN spotter_id TEXT NOT NULL DEFAULT ''`,
			`UPDATE spots SET is_adult = 1 WHERE sub_cat_d LIKE '%z3%'`,
			// Partial index matching the default browse query
			// (moderated = 0 AND is_adult = 0 ORDER BY posted_at DESC).
			`CREATE INDEX IF NOT EXISTS idx_spots_browse
			   ON spots(posted_at DESC) WHERE moderated = 0 AND is_adult = 0`,
			`CREATE INDEX IF NOT EXISTS idx_spots_browse_adult
			   ON spots(posted_at DESC) WHERE moderated = 0`,
			`CREATE INDEX IF NOT EXISTS idx_spots_spotter_id ON spots(spotter_id)`,
		},
	},
	{
		version: 2,
		name:    "add sync_state.backfill_article_num for historical retrieval",
		stmts: []string{
			// SYNC_LOOKBACK only ever reached backwards from the newest article
			// on first run, so everything older than that window was
			// unreachable forever. This column lets a worker walk backwards
			// towards the group's first article across restarts.
			`ALTER TABLE sync_state ADD COLUMN backfill_article_num INTEGER NOT NULL DEFAULT 0`,
			// Seed it from what we actually hold, so an existing install
			// resumes from its current oldest article rather than re-fetching.
			`UPDATE sync_state SET backfill_article_num =
			   COALESCE((SELECT MIN(article_num) FROM spots), 0)
			 WHERE backfill_article_num = 0`,
		},
	},
	{
		version: 3,
		name:    "remove indexed withdrawal-request control messages",
		stmts: []string{
			// These are control messages, never content. They were indexed as
			// spots before ingestion learned to recognise them.
			`DELETE FROM spots
			   WHERE title LIKE 'DISPOSE %@%'
			      OR title LIKE 'DELETE %@%'
			      OR title LIKE 'REMOVE %@%'`,
		},
	},
	{
		version: 4,
		name:    "re-ingest spots dated at index time by the Date-header fallback",
		stmts: []string{
			// parseNNTPDate used to return time.Now() when it could not read a
			// Date header — notably the common two-digit-year form
			// "Thu, 12 May 22 21:11:49 UTC". Those spots were stored with the
			// moment of indexing as their post date, so a 2022 spot showed up
			// as brand new and sorted to the top of every listing.
			//
			// The fingerprint is posted_at landing within a minute of
			// created_at. Genuinely fresh spots can look the same, which is
			// harmless: everything deleted here is re-inserted by the re-walk
			// below, now with a correct date.
			`DELETE FROM spots WHERE ABS(posted_at - created_at) <= 60`,

			// Rewind the forward watermark to the oldest article still held so
			// the next sync re-walks the whole retained range and restores the
			// rows above with correct dates. INSERT OR IGNORE makes re-walking
			// untouched spots a no-op.
			`UPDATE sync_state
			    SET last_article_num = COALESCE((SELECT MIN(article_num) FROM spots), 0)
			  WHERE group_name = 'free.pt'`,
		},
	},
}

// migrate applies the base schema then any pending migrations.
func (db *DB) migrate() error {
	if _, err := db.Exec(Schema); err != nil {
		return fmt.Errorf("base schema: %w", err)
	}

	var current int
	if err := db.QueryRow("PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}

	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		if err := db.applyMigration(m); err != nil {
			return fmt.Errorf("migration %d (%s): %w", m.version, m.name, err)
		}
	}
	return nil
}

func (db *DB) applyMigration(m migration) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, s := range m.stmts {
		if _, err := tx.Exec(s); err != nil {
			// ALTER TABLE ADD COLUMN is the one statement we may legitimately
			// re-run against a database that was patched by hand. Treat a
			// duplicate column as already-applied rather than a hard failure.
			if isDuplicateColumn(err) {
				continue
			}
			return fmt.Errorf("%s: %w", firstLine(s), err)
		}
	}

	// PRAGMA user_version does not accept a bound parameter.
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", m.version)); err != nil {
		return fmt.Errorf("set user_version: %w", err)
	}
	return tx.Commit()
}

func isDuplicateColumn(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "duplicate column name")
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// Setting reads a persisted application setting. Returns ("", nil) when unset.
func (db *DB) Setting(key string) (string, error) {
	var v string
	err := db.QueryRow(`SELECT value FROM app_settings WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

// SetSetting writes a persisted application setting.
func (db *DB) SetSetting(key, value string) error {
	_, err := db.Exec(`
		INSERT INTO app_settings (key, value, updated_at)
		VALUES (?, ?, unixepoch())
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at
	`, key, value)
	return err
}
