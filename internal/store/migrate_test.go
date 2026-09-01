package store

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// openTestDB opens an in-memory SQLite database with foreign keys enabled.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		t.Fatalf("enable foreign keys: %v", err)
	}
	return db
}

func TestMigrate_CreatesAllTables(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	expectedTables := []string{
		"items", "stories", "story_sources", "decisions",
		"runs", "feed_state", "schema_version", "items_fts",
	}
	for _, tbl := range expectedTables {
		var count int
		err := db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?",
			tbl,
		).Scan(&count)
		if err != nil {
			t.Fatalf("query sqlite_master for %s: %v", tbl, err)
		}
		if count == 0 {
			t.Errorf("missing table: %s", tbl)
		}
	}

	// Check indexes.
	expectedIndexes := []string{
		"idx_items_first_seen", "idx_items_canonical_url", "idx_items_story",
		"idx_stories_last_seen", "idx_stories_shown",
		"idx_decisions_story", "idx_runs_stage",
	}
	for _, idx := range expectedIndexes {
		var count int
		err := db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?",
			idx,
		).Scan(&count)
		if err != nil {
			t.Fatalf("query index %s: %v", idx, err)
		}
		if count == 0 {
			t.Errorf("missing index: %s", idx)
		}
	}

	// Check triggers.
	expectedTriggers := []string{"items_ai", "items_ad", "items_au"}
	for _, tr := range expectedTriggers {
		var count int
		err := db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name=?",
			tr,
		).Scan(&count)
		if err != nil {
			t.Fatalf("query trigger %s: %v", tr, err)
		}
		if count == 0 {
			t.Errorf("missing trigger: %s", tr)
		}
	}

	// Verify schema_version = 1.
	var version int
	err := db.QueryRowContext(ctx, "SELECT version FROM schema_version").Scan(&version)
	if err != nil {
		t.Fatalf("read schema_version: %v", err)
	}
	if version != 1 {
		t.Errorf("schema_version = %d, want 1", version)
	}
}

func TestMigrate_Idempotent(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}

	// Second call should be a no-op.
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}

	var count int
	err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_version").Scan(&count)
	if err != nil {
		t.Fatalf("read schema_version count: %v", err)
	}
	if count != 1 {
		t.Errorf("schema_version has %d rows, want 1 (idempotent)", count)
	}
}
func TestMigrate_FailedMigrationRollsBack(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// Apply migration 1 first so schema_version exists.
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}

	// Now try to apply invalid DDL in a transaction — it must roll back.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	_, err = tx.ExecContext(ctx, "NOT VALID SQL AT ALL @#$%")
	if err == nil {
		tx.Rollback()
		t.Fatal("expected error from invalid DDL in transaction")
	}
	// The failed transaction must not have changed the DB state.
	err = tx.Rollback()
	if err != nil {
		t.Fatalf("rollback failed tx: %v", err)
	}

	var version int
	err = db.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_version").Scan(&version)
	if err != nil {
		t.Fatalf("read schema_version: %v", err)
	}
	if version != 1 {
		t.Errorf("schema_version = %d, want 1 (rollback should preserve state)", version)
	}
}


func TestFTS_SyncInsert(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	_, err := db.ExecContext(ctx, `
		INSERT INTO items(url, canonical_url, title, source, tier, body_hash, body_excerpt)
		VALUES ('https://example.com/1', 'https://example.com/1', 'Hello World', 'test', 1, 'hash1', 'excerpt')
	`)
	if err != nil {
		t.Fatalf("insert item: %v", err)
	}

	var rowid int64
	err = db.QueryRowContext(ctx, "SELECT rowid FROM items_fts WHERE items_fts MATCH 'Hello'").Scan(&rowid)
	if err != nil {
		t.Fatalf("FTS search for 'Hello': %v", err)
	}
	if rowid == 0 {
		t.Fatal("FTS returned no rows")
	}
}

func TestFTS_SyncUpdate(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	id := insertItem(t, ctx, db, "https://example.com/1", "Old Title", "old excerpt")

	// Verify the item is searchable with its original title.
	var count int
	db.QueryRowContext(ctx, "SELECT COUNT(*) FROM items_fts WHERE items_fts MATCH 'Old'").Scan(&count)
	if count != 1 {
		t.Fatalf("FTS should find old title before update: %d", count)
	}

	// Update the title.
	_, err := db.ExecContext(ctx, "UPDATE items SET title = 'New Title', body_excerpt = 'new excerpt' WHERE id = ?", id)
	if err != nil {
		t.Fatalf("update item: %v", err)
	}

	// Old search should return nothing.
	db.QueryRowContext(ctx, "SELECT COUNT(*) FROM items_fts WHERE items_fts MATCH 'Old'").Scan(&count)
	if count != 0 {
		t.Errorf("FTS still has old title after update: %d", count)
	}

	// New search should find it.
	db.QueryRowContext(ctx, "SELECT COUNT(*) FROM items_fts WHERE items_fts MATCH 'New'").Scan(&count)
	if count == 0 {
		t.Fatal("FTS did not pick up updated title")
	}
}

func TestFTS_SyncDelete(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	insertItem(t, ctx, db, "https://example.com/1", "Delete Me", "excerpt")

	// Verify it's in FTS.
	var count int
	db.QueryRowContext(ctx, "SELECT COUNT(*) FROM items_fts WHERE items_fts MATCH 'Delete'").Scan(&count)
	if count != 1 {
		t.Fatalf("FTS has %d rows before delete, want 1", count)
	}

	// Delete the item.
	_, err := db.ExecContext(ctx, "DELETE FROM items WHERE url = 'https://example.com/1'")
	if err != nil {
		t.Fatalf("delete item: %v", err)
	}

	// Should no longer be in FTS.
	db.QueryRowContext(ctx, "SELECT COUNT(*) FROM items_fts WHERE items_fts MATCH 'Delete'").Scan(&count)
	if count != 0 {
		t.Errorf("FTS still has deleted item: %d", count)
	}
}

func TestCheckConstraint_DecisionsAction(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	_, err := db.ExecContext(ctx, `
		INSERT INTO decisions(story_id, action) VALUES (999, 'invalid_action')
	`)
	if err == nil {
		t.Fatal("expected error for invalid decisions.action")
	}
}

func TestCheckConstraint_StorySourcesKind(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	_, err := db.ExecContext(ctx, `
		INSERT INTO story_sources(story_id, source, url, kind) VALUES (999, 'src', 'http://x', 'invalid')
	`)
	if err == nil {
		t.Fatal("expected error for invalid story_sources.kind")
	}
}

func TestCheckConstraint_RunsStage(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	_, err := db.ExecContext(ctx, `
		INSERT INTO runs(stage, started_at) VALUES ('invalid_stage', CURRENT_TIMESTAMP)
	`)
	if err == nil {
		t.Fatal("expected error for invalid runs.stage")
	}
}

func TestUniqueConstraint_ItemsURL(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	_, err := db.ExecContext(ctx, `
		INSERT INTO items(url, canonical_url, title, source, tier, body_hash)
		VALUES ('https://example.com/1', 'https://example.com/1', 'First', 'src', 1, 'h1')
	`)
	if err != nil {
		t.Fatalf("first insert: %v", err)
	}

	_, err = db.ExecContext(ctx, `
		INSERT INTO items(url, canonical_url, title, source, tier, body_hash)
		VALUES ('https://example.com/1', 'https://example.com/1', 'Dup', 'src', 1, 'h2')
	`)
	if err == nil {
		t.Fatal("expected error for duplicate url")
	}
}

func TestForeignKey_StoriesRejectsBadItem(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// story_id 999 doesn't exist.
	_, err := db.ExecContext(ctx, `
		INSERT INTO items(url, canonical_url, title, source, tier, body_hash, story_id)
		VALUES ('https://example.com/1', 'https://example.com/1', 'Test', 'src', 1, 'h1', 999)
	`)
	if err == nil {
		t.Fatal("expected foreign key violation for bad story_id")
	}
}

func TestForeignKey_DecisionsRejectsBadStory(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	_, err := db.ExecContext(ctx, `
		INSERT INTO decisions(story_id, action) VALUES (999, 'shown')
	`)
	if err == nil {
		t.Fatal("expected foreign key violation for bad story_id in decisions")
	}
}

// insertItem is a test helper that inserts an item and returns its id.
func insertItem(t *testing.T, ctx context.Context, db *sql.DB, url, title, excerpt string) int64 {
	t.Helper()
	res, err := db.ExecContext(ctx, `
		INSERT INTO items(url, canonical_url, title, source, tier, body_hash, body_excerpt)
		VALUES (?, ?, ?, 'test', 1, 'hash', ?)
	`, url, url, title, excerpt)
	if err != nil {
		t.Fatalf("insert item: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId: %v", err)
	}
	return id
}
