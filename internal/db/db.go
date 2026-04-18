package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// DB wraps the sql.DB connection with app-specific helpers.
type DB struct {
	*sql.DB
}

// Spot represents a single Spotnet post.
type Spot struct {
	ID         int64
	MessageID  string
	ArticleNum int64
	Title      string
	Description string
	Poster     string
	PostedAt   time.Time
	Tag        string
	NzbID      string
	Category   int
	SubCatA    string
	SubCatB    string
	SubCatC    string
	SubCatD    string
	Size       int64
	ImageURL   string
	Verified   bool
	Moderated  bool
}

// SearchParams holds filtering + pagination for spot queries.
type SearchParams struct {
	Query      string   // FTS query
	Categories []int    // main categories to include (nil = all)
	Poster     string
	MinSize    int64
	MaxSize    int64
	Since      time.Time
	Limit      int
	Offset     int
	SortBy     string // "date" (default) | "size" | "relevance"
	AllowAdult bool   // include subcatz=z3 (18+) spots; default false
}

// Open opens (or creates) the SQLite database and applies the schema.
func Open(path string) (*DB, error) {
	sqlDB, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// SQLite works best with a single writer
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	// Apply pragmas after open (most reliable with modernc/sqlite)
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA temp_store=MEMORY",
	}
	for _, p := range pragmas {
		if _, err := sqlDB.Exec(p); err != nil {
			return nil, fmt.Errorf("pragma %q: %w", p, err)
		}
	}

	db := &DB{sqlDB}
	if err := db.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return db, nil
}

func (db *DB) migrate() error {
	_, err := db.Exec(Schema)
	return err
}

// InsertSpots bulk-inserts spots, ignoring duplicates (by message_id).
func (db *DB) InsertSpots(ctx context.Context, spots []Spot) (int, error) {
	if len(spots) == 0 {
		return 0, nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT OR IGNORE INTO spots
		    (message_id, article_num, title, description, poster, posted_at,
		     tag, nzb_id, category, sub_cat_a, sub_cat_b, sub_cat_c, sub_cat_d,
		     size, image_url, verified, moderated)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
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
		)
		if err != nil {
			return inserted, err
		}
		n, _ := res.RowsAffected()
		inserted += int(n)
	}

	return inserted, tx.Commit()
}

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

	if p.Query != "" {
		where = append(where, "s.id IN (SELECT rowid FROM spots_fts WHERE spots_fts MATCH ?)")
		args = append(args, p.Query)
	}
	if len(p.Categories) > 0 {
		placeholders := strings.Repeat("?,", len(p.Categories))
		placeholders = placeholders[:len(placeholders)-1]
		where = append(where, fmt.Sprintf("s.category IN (%s)", placeholders))
		for _, c := range p.Categories {
			args = append(args, c)
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
	where = append(where, "s.moderated = 0")
	if !p.AllowAdult {
		where = append(where, "s.sub_cat_d NOT LIKE '%z3%'")
	}

	whereClause := ""
	if len(where) > 0 {
		whereClause = "WHERE " + strings.Join(where, " AND ")
	}

	orderClause := "s.posted_at DESC"
	if p.SortBy == "size" {
		orderClause = "s.size DESC"
	}

	countArgs := make([]any, len(args))
	copy(countArgs, args)

	var total int
	err := db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM spots s %s", whereClause), countArgs...).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	args = append(args, p.Limit, p.Offset)
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`
		SELECT s.id, s.message_id, s.article_num, s.title, s.description,
		       s.poster, s.posted_at, s.tag, s.nzb_id,
		       s.category, s.sub_cat_a, s.sub_cat_b, s.sub_cat_c, s.sub_cat_d,
		       s.size, s.image_url, s.verified, s.moderated
		FROM spots s
		%s
		ORDER BY %s
		LIMIT ? OFFSET ?
	`, whereClause, orderClause), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var spots []Spot
	for rows.Next() {
		var s Spot
		var postedAt int64
		var verified, moderated int
		err := rows.Scan(
			&s.ID, &s.MessageID, &s.ArticleNum, &s.Title, &s.Description,
			&s.Poster, &postedAt, &s.Tag, &s.NzbID,
			&s.Category, &s.SubCatA, &s.SubCatB, &s.SubCatC, &s.SubCatD,
			&s.Size, &s.ImageURL, &verified, &moderated,
		)
		if err != nil {
			return nil, 0, err
		}
		s.PostedAt = time.Unix(postedAt, 0)
		s.Verified = verified == 1
		s.Moderated = moderated == 1
		spots = append(spots, s)
	}

	return spots, total, rows.Err()
}

// GetSpotByID fetches a single spot by its integer primary key.
func (db *DB) GetSpotByID(ctx context.Context, id int64) (*Spot, error) {
	row := db.QueryRowContext(ctx, `
		SELECT id, message_id, article_num, title, description, poster, posted_at,
		       tag, nzb_id, category, sub_cat_a, sub_cat_b, sub_cat_c, sub_cat_d,
		       size, image_url, verified, moderated
		FROM spots WHERE id = ?`, id)

	var s Spot
	var postedAt int64
	var verified, moderated int
	err := row.Scan(
		&s.ID, &s.MessageID, &s.ArticleNum, &s.Title, &s.Description, &s.Poster, &postedAt,
		&s.Tag, &s.NzbID, &s.Category, &s.SubCatA, &s.SubCatB, &s.SubCatC, &s.SubCatD,
		&s.Size, &s.ImageURL, &verified, &moderated,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s.PostedAt = time.Unix(postedAt, 0)
	s.Verified = verified == 1
	s.Moderated = moderated == 1
	return &s, nil
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

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
// UpdateSpotEnrichment saves the enriched fields (Description, ImageURL, NzbID, Size)
// back to the database after lazy-fetching the article body.
func (db *DB) UpdateSpotEnrichment(ctx context.Context, spot *Spot) error {
        _, err := db.ExecContext(ctx, `
                UPDATE spots
                SET description = ?, image_url = ?, nzb_id = ?, size = ?
                WHERE id = ?`,
                spot.Description, spot.ImageURL, spot.NzbID, spot.Size, spot.ID,
        )
        return err
}