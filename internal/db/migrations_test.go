package db

import (
	"path/filepath"
	"testing"
)

func TestFreshDatabaseMigratesAndRedecodesEightBitText(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "spottr.db"), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	var version int
	if err := d.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != migrations[len(migrations)-1].version {
		t.Fatalf("user_version %d, want %d", version, migrations[len(migrations)-1].version)
	}

	// A title as old ingestion stored it: Windows-1252 bytes, not UTF-8.
	if _, err := d.Exec(`INSERT INTO spots (message_id, article_num, title, poster, posted_at, tag, category, sub_cat_d)
		VALUES ('<a@b>', 1, CAST(X'50432047616D657220697320649720' AS TEXT), 'po', 0, '', 0, 'd31|z2|')`); err != nil {
		t.Fatal(err)
	}
	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := redecodeEightBitText(tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var title string
	if err := d.QueryRow(`SELECT title FROM spots WHERE message_id = '<a@b>'`).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title != "PC Gamer is d— " {
		t.Errorf("title %q", title)
	}
	// The repaired title is findable through the full-text index (the update trigger fired).
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM spots_fts WHERE spots_fts MATCH 'gamer'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("fts matches %d, want 1", n)
	}
}
