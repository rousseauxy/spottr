package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// DB wraps the sql.DB connection with app-specific helpers.
type DB struct {
	*sql.DB

	// writeMu serializes bulk write transactions in-process.
	//
	// SQLite permits exactly one writer regardless, so letting the sync
	// engine, the backfill worker and maintenance sweeps contend for it only
	// converts guaranteed serialization into lock contention and occasional
	// SQLITE_BUSY. Taking the turn in Go is cheaper and deterministic. Reads
	// are unaffected and still run concurrently under WAL.
	writeMu sync.Mutex
}

// lockWrites acquires the bulk-write turn. Callers must call the returned
// release function.
func (db *DB) lockWrites() func() {
	db.writeMu.Lock()
	return db.writeMu.Unlock
}

// Spot represents a single Spotnet post.
type Spot struct {
	ID          int64
	MessageID   string
	ArticleNum  int64
	Title       string
	Description string
	Poster      string
	PostedAt    time.Time
	Tag         string
	NzbID       string
	Category    int
	SubCatA     string
	SubCatB     string
	SubCatC     string
	SubCatD     string
	Size        int64
	ImageURL    string
	Verified    bool
	Moderated   bool
	IsAdult     bool
	SpotterID   string
	Kind        int // KindMovie, KindSeries, ... for Image spots; KindNone otherwise
	// The following are parsed from the From header and are NOT persisted.
	// They exist only while classifying a freshly retrieved article.
	KeyID         int    `json:"-"` // Spotnet signing-key id
	HeaderPart    string `json:"-"` // everything after the @, incl. the signature
	HeaderSig     string `json:"-"` // trailing signature field
	SelfSignedKey string `json:"-"` // inline RSA modulus on self-signed articles
}

// SearchParams holds filtering + pagination for spot queries.
type SearchParams struct {
	Query      string // FTS query (sanitized before it reaches SQLite)
	Categories []int  // main categories to include (nil = all)
	Poster     string
	MinSize    int64
	MaxSize    int64
	Since      time.Time
	Limit      int
	Offset     int
	// Before implements keyset pagination: return only spots posted strictly
	// before this instant. Far cheaper than a large OFFSET.
	Before     time.Time
	SortBy     string   // "date" (default) | "size" | "relevance"
	AllowAdult bool     // include 18+ spots; default false
	Kinds      []int    // KindMovie, KindComic, ... to include (nil = all)
	Tag        string   // exact tag, matched through the full-text index's tag column
	SubCodes   []string // Spotnet subcategory codes that must all be present: "a5", "c2", "d31", "z2"
}

// subCodeColumn maps a subcategory code's letter to its column; z codes live in sub_cat_d.
var subCodeColumn = map[byte]string{'a': "s.sub_cat_a", 'b': "s.sub_cat_b", 'c': "s.sub_cat_c", 'd': "s.sub_cat_d", 'z': "s.sub_cat_d"}

// ValidSubCode reports whether s is a subcategory code like "a5" or "d31".
func ValidSubCode(s string) bool {
	if len(s) < 2 || len(s) > 4 || subCodeColumn[s[0]] == "" {
		return false
	}
	for _, c := range s[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// Open opens (or creates) the SQLite database and applies the schema.
//
// maxConns bounds the connection pool. WAL mode supports many concurrent
// readers alongside a single writer, so more than one connection is both safe
// and necessary — with a pool of one, any slow query blocks every other
// request and the background sync engine.
func Open(path string, maxConns int) (*DB, error) {
	if maxConns < 1 {
		maxConns = 1
	}

	// The DSN needs the file: scheme for query parameters to be parsed at all —
	// a bare path is taken as a filename and the pragmas are silently ignored.
	//
	// busy_timeout makes a writer wait for the lock instead of failing
	// immediately with SQLITE_BUSY. _txlock=immediate is the other half: it
	// makes BeginTx take the write lock up front, so two transactions can never
	// both start deferred and then deadlock trying to upgrade — an upgrade
	// failure is *not* covered by busy_timeout and surfaces as a hard
	// "database is locked". Both matter now that sync, backfill and HTTP
	// requests share a multi-connection pool.
	dsn := path
	if !strings.Contains(dsn, "?") {
		dsn = "file:" + path +
			"?_pragma=busy_timeout(15000)" +
			"&_pragma=journal_mode(WAL)" +
			"&_pragma=foreign_keys(1)" +
			"&_pragma=synchronous(1)" +
			"&_txlock=immediate"
	}

	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	sqlDB.SetMaxOpenConns(maxConns)
	sqlDB.SetMaxIdleConns(maxConns)
	sqlDB.SetConnMaxLifetime(time.Hour)

	// Applied per-connection by the DSN above; repeated here so a pre-existing
	// database file also gets WAL turned on.
	for _, p := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA temp_store=MEMORY",
	} {
		if _, err := sqlDB.Exec(p); err != nil {
			return nil, fmt.Errorf("pragma %q: %w", p, err)
		}
	}

	db := &DB{DB: sqlDB}
	if err := db.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return db, nil
}

// InsertSpots bulk-inserts spots, ignoring duplicates (by message_id) and
// anything already recorded as moderator-removed.
func (db *DB) InsertSpots(ctx context.Context, spots []Spot) (int, error) {
	if len(spots) == 0 {
		return 0, nil
	}
	defer db.lockWrites()()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT OR IGNORE INTO spots
		    (message_id, article_num, title, description, poster, posted_at,
		     tag, nzb_id, category, sub_cat_a, sub_cat_b, sub_cat_c, sub_cat_d,
		     size, image_url, verified, moderated, is_adult, spotter_id, kind)
		SELECT ?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?
		WHERE NOT EXISTS (SELECT 1 FROM moderated_ring WHERE message_id = ?)
	`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	inserted := 0
	for _, s := range spots {
		res, err := stmt.ExecContext(ctx,
			s.MessageID, s.ArticleNum, s.Title, s.Description, s.Poster,
			s.PostedAt.Unix(), s.Tag, s.NzbID,
			s.Category, s.SubCatA, s.SubCatB, s.SubCatC, s.SubCatD,
			s.Size, s.ImageURL, boolInt(s.Verified), boolInt(s.Moderated),
			boolInt(s.IsAdult), s.SpotterID, KindOf(s.Category, s.SubCatD),
			s.MessageID,
		)
		if err != nil {
			return inserted, err
		}
		n, _ := res.RowsAffected()
		inserted += int(n)
	}

	return inserted, tx.Commit()
}

const spotColumns = `s.id, s.message_id, s.article_num, s.title, s.description,
	s.poster, s.posted_at, s.tag, s.nzb_id,
	s.category, s.sub_cat_a, s.sub_cat_b, s.sub_cat_c, s.sub_cat_d,
	s.size, s.image_url, s.verified, s.moderated, s.is_adult, s.spotter_id, s.kind`

// SearchSpots performs a full-text or filtered search.
func (db *DB) SearchSpots(ctx context.Context, p SearchParams) ([]Spot, int, error) {
	if p.Limit == 0 {
		p.Limit = 25
	}
	if p.SortBy == "" {
		p.SortBy = "date"
	}

	var where []string
	var args []any

	ftsQuery := SanitizeFTSQuery(p.Query)
	if ftsQuery != "" {
		where = append(where, "s.id IN (SELECT rowid FROM spots_fts WHERE spots_fts MATCH ?)")
		args = append(args, ftsQuery)
	}
	if len(p.Categories) > 0 {
		where = append(where, inOrEqual("s.category", len(p.Categories)))
		for _, c := range p.Categories {
			args = append(args, c)
		}
	}
	if p.Tag != "" {
		// The tag column of the full-text index, as a phrase: exact words, indexed.
		where = append(where, "s.id IN (SELECT rowid FROM spots_fts WHERE spots_fts MATCH ?)")
		args = append(args, `{tag}: "`+strings.ReplaceAll(p.Tag, `"`, `""`)+`"`)
	}
	for _, code := range p.SubCodes {
		if !ValidSubCode(code) {
			continue
		}
		// Codes are stored as "a5|c2|"; "|" before and after makes "d3" not match "d31".
		where = append(where, fmt.Sprintf("instr('|' || %s, ?) > 0", subCodeColumn[code[0]]))
		args = append(args, "|"+code+"|")
	}
	if len(p.Kinds) > 0 {
		where = append(where, inOrEqual("s.kind", len(p.Kinds)))
		for _, k := range p.Kinds {
			args = append(args, k)
		}
	}
	if p.Poster != "" {
		where = append(where, "s.poster = ?")
		args = append(args, p.Poster)
	}
	if p.MinSize > 0 {
		where = append(where, "s.size >= ?")
		args = append(args, p.MinSize)
	}
	if p.MaxSize > 0 {
		where = append(where, "s.size <= ?")
		args = append(args, p.MaxSize)
	}
	if !p.Since.IsZero() {
		where = append(where, "s.posted_at >= ?")
		args = append(args, p.Since.Unix())
	}
	if !p.Before.IsZero() {
		where = append(where, "s.posted_at < ?")
		args = append(args, p.Before.Unix())
	}
	where = append(where, "s.moderated = 0")
	if !p.AllowAdult {
		// Indexed equality against the partial index, replacing the old
		// `sub_cat_d NOT LIKE '%z3%'` full scan.
		where = append(where, "s.is_adult = 0")
	}

	whereClause := "WHERE " + strings.Join(where, " AND ")

	orderClause := "s.posted_at DESC"
	switch p.SortBy {
	case "size":
		orderClause = "s.size DESC"
	case "relevance":
		if ftsQuery == "" {
			orderClause = "s.posted_at DESC"
		}
	}

	countArgs := make([]any, len(args))
	copy(countArgs, args)

	var total int
	err := db.QueryRowContext(ctx,
		fmt.Sprintf("SELECT COUNT(*) FROM spots s %s", whereClause), countArgs...).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	args = append(args, p.Limit, p.Offset)
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`
		SELECT %s
		FROM spots s
		%s
		ORDER BY %s
		LIMIT ? OFFSET ?
	`, spotColumns, whereClause, orderClause), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var spots []Spot
	for rows.Next() {
		s, err := scanSpot(rows)
		if err != nil {
			return nil, 0, err
		}
		spots = append(spots, *s)
	}

	return spots, total, rows.Err()
}

// inOrEqual is "col = ?" for one value and "col IN (?,...)" for more. With IN, even for a
// single value, SQLite would not walk an index in posted_at order and sorted every matching row
// instead (all 414 k Audio spots, measured 2026-10-17).
func inOrEqual(col string, n int) string {
	if n == 1 {
		return col + " = ?"
	}
	return fmt.Sprintf("%s IN (%s)", col, strings.TrimSuffix(strings.Repeat("?,", n), ","))
}

type rowScanner interface{ Scan(dest ...any) error }

func scanSpot(sc rowScanner) (*Spot, error) {
	var s Spot
	var postedAt int64
	var verified, moderated, isAdult int
	err := sc.Scan(
		&s.ID, &s.MessageID, &s.ArticleNum, &s.Title, &s.Description,
		&s.Poster, &postedAt, &s.Tag, &s.NzbID,
		&s.Category, &s.SubCatA, &s.SubCatB, &s.SubCatC, &s.SubCatD,
		&s.Size, &s.ImageURL, &verified, &moderated, &isAdult, &s.SpotterID, &s.Kind,
	)
	if err != nil {
		return nil, err
	}
	s.PostedAt = time.Unix(postedAt, 0)
	s.Verified = verified == 1
	s.Moderated = moderated == 1
	s.IsAdult = isAdult == 1
	return &s, nil
}

// GetSpotByID fetches a single spot by its integer primary key.
func (db *DB) GetSpotByID(ctx context.Context, id int64) (*Spot, error) {
	row := db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT %s FROM spots s WHERE s.id = ?`, spotColumns), id)

	s, err := scanSpot(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s, nil
}

// GetSyncState returns the last retrieved article number for a newsgroup.
func (db *DB) GetSyncState(ctx context.Context, group string) (int64, error) {
	var n int64
	err := db.QueryRowContext(ctx,
		"SELECT last_article_num FROM sync_state WHERE group_name = ?", group,
	).Scan(&n)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return n, err
}

// SetSyncState updates the last retrieved article number for a newsgroup.
func (db *DB) SetSyncState(ctx context.Context, group string, lastArticle int64) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO sync_state (group_name, last_article_num, last_sync_at)
		VALUES (?, ?, unixepoch())
		ON CONFLICT(group_name) DO UPDATE SET
		    last_article_num = excluded.last_article_num,
		    last_sync_at     = excluded.last_sync_at
	`, group, lastArticle)
	return err
}

// GetDisposeSweepState returns the last article the withdrawal sweep has passed (0 = not started).
func (db *DB) GetDisposeSweepState(ctx context.Context, group string) (int64, error) {
	var n int64
	err := db.QueryRowContext(ctx,
		"SELECT dispose_sweep_article_num FROM sync_state WHERE group_name = ?", group,
	).Scan(&n)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return n, err
}

// SetDisposeSweepState checkpoints the withdrawal sweep.
func (db *DB) SetDisposeSweepState(ctx context.Context, group string, article int64) error {
	_, err := db.ExecContext(ctx,
		`UPDATE sync_state SET dispose_sweep_article_num = ? WHERE group_name = ?`, article, group)
	return err
}

// OldestArticle returns the lowest article number indexed, 0 when nothing is.
func (db *DB) OldestArticle(ctx context.Context) (int64, error) {
	var n sql.NullInt64
	err := db.QueryRowContext(ctx, `SELECT MIN(article_num) FROM spots`).Scan(&n)
	return n.Int64, err
}

// GetBackfillState returns the lowest article number retrieved so far.
// Zero means backfill has not started.
func (db *DB) GetBackfillState(ctx context.Context, group string) (int64, error) {
	var n int64
	err := db.QueryRowContext(ctx,
		"SELECT backfill_article_num FROM sync_state WHERE group_name = ?", group,
	).Scan(&n)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return n, err
}

// SetBackfillState records how far back the historical sweep has reached.
func (db *DB) SetBackfillState(ctx context.Context, group string, oldestArticle int64) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO sync_state (group_name, backfill_article_num, last_sync_at)
		VALUES (?, ?, unixepoch())
		ON CONFLICT(group_name) DO UPDATE SET
		    backfill_article_num = excluded.backfill_article_num
	`, group, oldestArticle)
	return err
}

// CacheNZB stores a fetched NZB in the cache.
func (db *DB) CacheNZB(ctx context.Context, messageID string, content []byte) error {
	_, err := db.ExecContext(ctx, `
		INSERT OR REPLACE INTO nzbs (message_id, content, fetched_at)
		VALUES (?, ?, unixepoch())
	`, messageID, content)
	return err
}

// GetCachedNZB retrieves a cached NZB or returns nil if not cached.
func (db *DB) GetCachedNZB(ctx context.Context, messageID string) ([]byte, error) {
	var content []byte
	err := db.QueryRowContext(ctx,
		"SELECT content FROM nzbs WHERE message_id = ?", messageID,
	).Scan(&content)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return content, err
}

// CacheImage stores a decoded image in the cache.
func (db *DB) CacheImage(ctx context.Context, messageID string, content []byte, mimeType string) error {
	_, err := db.ExecContext(ctx, `
		INSERT OR REPLACE INTO images (message_id, content, mime_type, fetched_at)
		VALUES (?, ?, ?, unixepoch())
	`, messageID, content, mimeType)
	return err
}

// GetCachedImage retrieves a cached image. Returns nil content if not cached.
func (db *DB) GetCachedImage(ctx context.Context, messageID string) (content []byte, mimeType string, err error) {
	err = db.QueryRowContext(ctx,
		"SELECT content, mime_type FROM images WHERE message_id = ?", messageID,
	).Scan(&content, &mimeType)
	if err == sql.ErrNoRows {
		return nil, "", nil
	}
	return content, mimeType, err
}

// UpdateSpotEnrichment saves the enriched fields back to the database after
// lazy-fetching the article body.
func (db *DB) UpdateSpotEnrichment(ctx context.Context, spot *Spot) error {
	_, err := db.ExecContext(ctx, `
		UPDATE spots
		SET description = ?, image_url = ?, nzb_id = ?, size = ?, spotter_id = ?, verified = ?
		WHERE id = ?`,
		spot.Description, spot.ImageURL, spot.NzbID, spot.Size,
		spot.SpotterID, boolInt(spot.Verified), spot.ID,
	)
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
