package db

import (
	"database/sql"
	"fmt"
	"strings"
	"unicode/utf8"
)

// migration is one forward-only schema change. Statements run inside a single
// transaction; the version is recorded via PRAGMA user_version so each
// migration applies exactly once.
type migration struct {
	version int
	name    string
	stmts   []string
	// fn runs after stmts, in the same transaction, for a change SQL alone cannot express.
	fn func(tx *sql.Tx) error
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
	{
		version: 5,
		name:    "add spots.kind (movie, series, book, comic, erotic); index kind and category",
		stmts: []string{
			// The search index trigger fired on ANY update of a row, so filling a new column
			// would rewrite the full-text index for every spot. It only needs to fire when an
			// indexed column changes.
			`DROP TRIGGER IF EXISTS spots_au`,
			spotsUpdateTrigger,
			`ALTER TABLE spots ADD COLUMN kind INTEGER NOT NULL DEFAULT 0`,
			`UPDATE spots SET kind = ` + kindCaseSQL + ` WHERE category = 0`,
			// Filtering by kind or main category is the browse query plus kind = ? or
			// category = ?, newest first. Partial on the default browse condition like
			// idx_spots_browse, so the COUNT(*) is answered from the index alone: without
			// is_adult in it, Audio (414 k spots) read every row and took 0.9 s (measured on
			// a copy 2026-10-17).
			`CREATE INDEX IF NOT EXISTS idx_spots_kind
			   ON spots(kind, posted_at DESC) WHERE moderated = 0 AND is_adult = 0`,
			// idx_spots_category (category, sub_cat_a) already exists in the base schema; a new
			// index under that name would silently not be created.
			`CREATE INDEX IF NOT EXISTS idx_spots_category_browse
			   ON spots(category, posted_at DESC) WHERE moderated = 0 AND is_adult = 0`,
		},
	},
	{
		version: 6,
		name:    "re-decode titles, tags and posters stored as raw 8-bit text",
		// 27,657 of 2.49 M titles (2026-10-17) held Windows-1252 bytes, not UTF-8, and showed
		// as "d�" in the browser. New spots are decoded at parse time; this repairs the rest.
		fn: redecodeEightBitText,
	},
	{
		version: 7,
		name:    "add sync_state.dispose_sweep_article_num for the withdrawal history sweep",
		stmts: []string{
			// Withdrawals were dropped at ingest without being applied, for the whole history.
			// The sweep walks the group forward once and applies them; 0 = not started.
			`ALTER TABLE sync_state ADD COLUMN dispose_sweep_article_num INTEGER NOT NULL DEFAULT 0`,
		},
	},
}

// redecodeEightBitText rewrites every title, tag and poster that is not valid UTF-8, reading the
// bytes as Windows-1252 (the same decoding the parser now applies).
func redecodeEightBitText(tx *sql.Tx) error {
	rows, err := tx.Query(`SELECT id, CAST(title AS BLOB), CAST(tag AS BLOB), CAST(poster AS BLOB) FROM spots`)
	if err != nil {
		return err
	}
	type fix struct {
		id                 int64
		title, tag, poster string
	}
	var fixes []fix
	for rows.Next() {
		var id int64
		var title, tag, poster []byte
		if err := rows.Scan(&id, &title, &tag, &poster); err != nil {
			rows.Close()
			return err
		}
		if utf8.Valid(title) && utf8.Valid(tag) && utf8.Valid(poster) {
			continue
		}
		fixes = append(fixes, fix{id, toUTF8(title), toUTF8(tag), toUTF8(poster)})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	stmt, err := tx.Prepare(`UPDATE spots SET title = ?, tag = ?, poster = ? WHERE id = ?`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, f := range fixes {
		if _, err := stmt.Exec(f.title, f.tag, f.poster, f.id); err != nil {
			return err
		}
	}
	return nil
}

// toUTF8 keeps valid UTF-8 and reads anything else as Windows-1252, like spotnet.Windows1252ToUTF8
// (db cannot import spotnet, which imports db).
func toUTF8(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	high := [32]rune{
		0x20AC, 0x81, 0x201A, 0x0192, 0x201E, 0x2026, 0x2020, 0x2021,
		0x02C6, 0x2030, 0x0160, 0x2039, 0x0152, 0x8D, 0x017D, 0x8F,
		0x90, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014,
		0x02DC, 0x2122, 0x0161, 0x203A, 0x0153, 0x9D, 0x017E, 0x0178,
	}
	var sb strings.Builder
	for _, c := range b {
		switch {
		case c < 0x80:
			sb.WriteByte(c)
		case c < 0xA0:
			sb.WriteRune(high[c-0x80])
		default:
			sb.WriteRune(rune(c))
		}
	}
	return sb.String()
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

	if m.fn != nil {
		if err := m.fn(tx); err != nil {
			return err
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
