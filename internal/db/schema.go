package db

// Schema defines the base DDL. Every statement is idempotent (IF NOT EXISTS)
// so it can be re-applied on every boot. Changes to EXISTING tables belong in
// migrations (see migrations.go), not here — SQLite has no "ADD COLUMN IF NOT
// EXISTS", so altering this block would break upgrades of live databases.

const Schema = `
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;
PRAGMA synchronous = NORMAL;
PRAGMA temp_store = MEMORY;

-- Core spots table
CREATE TABLE IF NOT EXISTS spots (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    message_id  TEXT    NOT NULL UNIQUE,         -- NNTP Message-ID
    article_num INTEGER NOT NULL,                 -- NNTP article number
    title       TEXT    NOT NULL,
    description TEXT    NOT NULL DEFAULT '',
    poster      TEXT    NOT NULL,
    posted_at   INTEGER NOT NULL,                 -- Unix timestamp
    tag         TEXT    NOT NULL DEFAULT '',
    nzb_id      TEXT    NOT NULL DEFAULT '',      -- Message-ID of the NZB article
    category    INTEGER NOT NULL DEFAULT 0,       -- Main category (0=image,1=audio,2=game,3=app)
    sub_cat_a   TEXT    NOT NULL DEFAULT '',       -- Subcategory list A (pipe-separated, e.g. "a2|a5|")
    sub_cat_b   TEXT    NOT NULL DEFAULT '',       -- Subcategory list B
    sub_cat_c   TEXT    NOT NULL DEFAULT '',       -- Subcategory list C
    sub_cat_d   TEXT    NOT NULL DEFAULT '',       -- Subcategory list D
    size        INTEGER NOT NULL DEFAULT 0,       -- Bytes
    image_url   TEXT    NOT NULL DEFAULT '',
    verified    INTEGER NOT NULL DEFAULT 0,       -- 1 = RSA signature verified
    moderated   INTEGER NOT NULL DEFAULT 0,       -- 1 = removed by moderator
    created_at  INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE INDEX IF NOT EXISTS idx_spots_posted_at   ON spots(posted_at DESC);
CREATE INDEX IF NOT EXISTS idx_spots_category    ON spots(category, sub_cat_a);
CREATE INDEX IF NOT EXISTS idx_spots_poster      ON spots(poster);
CREATE INDEX IF NOT EXISTS idx_spots_article_num ON spots(article_num);

-- FTS5 virtual table for full-text search
CREATE VIRTUAL TABLE IF NOT EXISTS spots_fts USING fts5 (
    title,
    description,
    poster,
    tag,
    content=spots,
    content_rowid=id,
    tokenize='unicode61 remove_diacritics 1'
);

-- Triggers to keep FTS in sync
CREATE TRIGGER IF NOT EXISTS spots_ai AFTER INSERT ON spots BEGIN
    INSERT INTO spots_fts(rowid, title, description, poster, tag)
    VALUES (new.id, new.title, new.description, new.poster, new.tag);
END;

CREATE TRIGGER IF NOT EXISTS spots_ad AFTER DELETE ON spots BEGIN
    INSERT INTO spots_fts(spots_fts, rowid, title, description, poster, tag)
    VALUES ('delete', old.id, old.title, old.description, old.poster, old.tag);
END;

-- spots_au: see spotsUpdateTrigger (Go), shared with migration 5.

-- NZB cache: store raw NZB XML fetched from Usenet
CREATE TABLE IF NOT EXISTS nzbs (
    message_id TEXT    PRIMARY KEY,
    content    BLOB    NOT NULL,
    fetched_at INTEGER NOT NULL DEFAULT (unixepoch())
);

-- Image cache: store decoded image bytes fetched from Usenet
CREATE TABLE IF NOT EXISTS images (
    message_id TEXT    PRIMARY KEY,
    content    BLOB    NOT NULL,
    mime_type  TEXT    NOT NULL DEFAULT 'image/jpeg',
    fetched_at INTEGER NOT NULL DEFAULT (unixepoch())
);

-- Users
CREATE TABLE IF NOT EXISTS users (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    username     TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    password     TEXT    NOT NULL,  -- bcrypt
    email        TEXT    NOT NULL DEFAULT '',
    api_key      TEXT    NOT NULL UNIQUE,
    role         TEXT    NOT NULL DEFAULT 'user',  -- 'admin' or 'user'
    preferences  TEXT    NOT NULL DEFAULT '{}',    -- JSON blob
    created_at   INTEGER NOT NULL DEFAULT (unixepoch()),
    last_seen_at INTEGER NOT NULL DEFAULT (unixepoch())
);

-- Per-user spot state (read, downloaded, bookmarked)
CREATE TABLE IF NOT EXISTS user_spots (
    user_id  INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    spot_id  INTEGER NOT NULL REFERENCES spots(id) ON DELETE CASCADE,
    state    TEXT    NOT NULL DEFAULT 'unread',   -- 'read','downloaded','bookmarked'
    updated_at INTEGER NOT NULL DEFAULT (unixepoch()),
    PRIMARY KEY (user_id, spot_id)
);

-- Sync state: track highest article number retrieved per group.
-- backfill_article_num is the lowest article we have walked back to; the
-- backfill worker moves it down towards the group's first article.
CREATE TABLE IF NOT EXISTS sync_state (
    group_name           TEXT    PRIMARY KEY,
    last_article_num     INTEGER NOT NULL DEFAULT 0,
    last_sync_at         INTEGER NOT NULL DEFAULT 0,
    backfill_article_num INTEGER NOT NULL DEFAULT 0
);

-- Persistent application settings. Owns the newznab API key so it survives
-- both process restarts and container recreation.
CREATE TABLE IF NOT EXISTS app_settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at INTEGER NOT NULL DEFAULT (unixepoch())
);

-- Spotter blacklist/whitelist, populated from external XML feeds.
CREATE TABLE IF NOT EXISTS spotter_lists (
    spotter_id TEXT NOT NULL,
    list_type  TEXT NOT NULL,              -- 'black' | 'white'
    origin     TEXT NOT NULL DEFAULT '',   -- feed URL it came from
    updated_at INTEGER NOT NULL DEFAULT (unixepoch()),
    PRIMARY KEY (spotter_id, list_type)
);

-- Message-ids removed by a moderator dispose. Kept after the spot is deleted so
-- a later sync pass does not re-insert it.
CREATE TABLE IF NOT EXISTS moderated_ring (
    message_id TEXT PRIMARY KEY,
    stamp      INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE INDEX IF NOT EXISTS idx_nzbs_fetched_at   ON nzbs(fetched_at);
CREATE INDEX IF NOT EXISTS idx_images_fetched_at ON images(fetched_at);
`

// spotsUpdateTrigger keeps the search index in step when an indexed column changes. Created by
// migration 5 (for new and existing databases alike); before that it fired on every update of a
// row, so filling any new column rewrote the whole full-text index.
const spotsUpdateTrigger = `CREATE TRIGGER IF NOT EXISTS spots_au
AFTER UPDATE OF title, description, poster, tag ON spots BEGIN
    INSERT INTO spots_fts(spots_fts, rowid, title, description, poster, tag)
    VALUES ('delete', old.id, old.title, old.description, old.poster, old.tag);
    INSERT INTO spots_fts(rowid, title, description, poster, tag)
    VALUES (new.id, new.title, new.description, new.poster, new.tag);
END`
