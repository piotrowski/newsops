package dedupe

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"news-ops/internal/config"
	"news-ops/internal/store"
)

// seedTestDB creates an in-memory SQLite DB with the schema and inserts test items.
func seedTestDB(t *testing.T, items []testItem) *store.SQLiteDB {
	t.Helper()

	sqlDB, err := sql.Open("sqlite", "file::memory:?_journal_mode=WAL&_foreign_keys=ON")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	const schemaV1 = `
CREATE TABLE items (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    url           TEXT NOT NULL UNIQUE,
    canonical_url TEXT NOT NULL,
    title         TEXT NOT NULL,
    source        TEXT NOT NULL,
    tier          INTEGER NOT NULL,
    published_at  DATETIME,
    first_seen    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    body_hash     TEXT NOT NULL,
    body_excerpt  TEXT,
    raw_path      TEXT,
    comments_url  TEXT,
    story_id      INTEGER REFERENCES stories(id)
);

CREATE INDEX idx_items_first_seen    ON items(first_seen);
CREATE INDEX idx_items_canonical_url ON items(canonical_url);
CREATE INDEX idx_items_story         ON items(story_id);

CREATE TABLE stories (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    canonical_item_id INTEGER NOT NULL REFERENCES items(id),
    source_count      INTEGER NOT NULL DEFAULT 1,
    method            TEXT NOT NULL,
    first_seen        DATETIME NOT NULL,
    last_seen         DATETIME NOT NULL,
    shown_at          DATETIME
);

CREATE INDEX idx_stories_last_seen ON stories(last_seen);
CREATE INDEX idx_stories_shown     ON stories(shown_at);

CREATE TABLE story_sources (
    story_id INTEGER NOT NULL REFERENCES stories(id),
    source   TEXT NOT NULL,
    url      TEXT NOT NULL,
    kind     TEXT NOT NULL CHECK(kind IN ('article','discussion')),
    PRIMARY KEY(story_id, source, url)
);

CREATE TABLE runs (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    stage      TEXT NOT NULL CHECK(stage IN ('fetch','dedupe','candidates')),
    started_at DATETIME NOT NULL,
    ended_at   DATETIME,
    ok         BOOLEAN,
    items_in   INTEGER,
    items_out  INTEGER,
    note       TEXT
);

CREATE INDEX idx_runs_stage ON runs(stage, started_at);

CREATE TABLE feed_state (
    source        TEXT PRIMARY KEY,
    url           TEXT NOT NULL,
    etag          TEXT,
    last_modified TEXT,
    last_fetched  DATETIME,
    last_status   INTEGER,
    last_error    TEXT
);

CREATE TABLE schema_version (
    version    INTEGER PRIMARY KEY,
    applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE VIRTUAL TABLE items_fts USING fts5(
    title, body_excerpt, content='items', content_rowid='id'
);

CREATE TRIGGER items_ai AFTER INSERT ON items BEGIN
    INSERT INTO items_fts(rowid, title, body_excerpt)
        VALUES (new.id, new.title, new.body_excerpt);
END;

CREATE TRIGGER items_ad AFTER DELETE ON items BEGIN
    INSERT INTO items_fts(items_fts, rowid, title, body_excerpt)
        VALUES ('delete', old.id, old.title, old.body_excerpt);
END;

CREATE TRIGGER items_au AFTER UPDATE ON items BEGIN
    INSERT INTO items_fts(items_fts, rowid, title, body_excerpt)
        VALUES ('delete', old.id, old.title, old.body_excerpt);
    INSERT INTO items_fts(rowid, title, body_excerpt)
        VALUES (new.id, new.title, new.body_excerpt);
END;
`

	if _, err := sqlDB.Exec(schemaV1); err != nil {
		t.Fatalf("exec schema: %v", err)
	}

	for _, it := range items {
		var commentsURL sql.NullString
		if it.CommentsURL != "" {
			commentsURL = sql.NullString{String: it.CommentsURL, Valid: true}
		}
		_, err := sqlDB.Exec(`
			INSERT INTO items(url, canonical_url, title, source, tier, published_at, first_seen, body_hash, comments_url)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			it.URL, it.CanonicalURL, it.Title, it.Source, it.Tier,
			it.PublishedAt, it.FirstSeen, it.BodyHash, commentsURL)
		if err != nil {
			t.Fatalf("insert item %q: %v", it.Title, err)
		}
	}

	return &store.SQLiteDB{DB: sqlDB}
}

// testItem is a simplified items row for seeding tests.
type testItem struct {
	URL          string
	CanonicalURL string
	Title        string
	Source       string
	Tier         int
	PublishedAt  *string // nil means NULL
	FirstSeen    string
	BodyHash     string
	CommentsURL  string // aggregator discussion link
}

func TestAssign_HNPlusArticle(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{
		Sources: []config.Source{
			{Name: "Hacker News", Tier: 1, URL: "https://hnrss.org/frontpage"},
			{Name: "Blogspot", Tier: 2, URL: "https://blog.example.com/feed"},
		},
		PrimaryDomains: map[string]bool{"blog.example.com": true},
	}

	now := time.Now().UTC().Format(time.RFC3339)
	published := now
	items := []testItem{
		{
			URL:          "https://blog.example.com/article",
			CanonicalURL: "https://blog.example.com/article",
			Title:        "Show HN: My new project | Hacker News",
			Source:       "Hacker News",
			Tier:         1,
			PublishedAt:  &published,
			FirstSeen:    now,
			BodyHash:     "abc123",
			CommentsURL:  "https://news.ycombinator.com/item?id=12345",
		},
		{
			URL:          "https://blog.example.com/article?utm_source=newsops",
			CanonicalURL: "https://blog.example.com/article",
			Title:        "My new project announced",
			Source:       "Blogspot",
			Tier:         2,
			PublishedAt:  &published,
			FirstSeen:    now,
			BodyHash:     "def456",
		},
	}

	db := seedTestDB(t, items)
	defer db.Close()

	result, err := Assign(ctx, db, cfg)
	if err != nil {
		t.Fatalf("Assign: %v", err)
	}

	if result.ItemsIn != 2 {
		t.Errorf("items_in = %d, want 2", result.ItemsIn)
	}
	if result.StoriesCreated != 1 {
		t.Errorf("stories_created = %d, want 1", result.StoriesCreated)
	}

	var sourceCount int
	err = db.QueryRowContext(ctx, `SELECT source_count FROM stories WHERE id=1`).Scan(&sourceCount)
	if err != nil {
		t.Fatalf("query story: %v", err)
	}
	if sourceCount != 2 {
		t.Errorf("source_count = %d, want 2", sourceCount)
	}

	var method string
	err = db.QueryRowContext(ctx, `SELECT method FROM stories WHERE id=1`).Scan(&method)
	if err != nil {
		t.Fatalf("query method: %v", err)
	}
	if method != "url" {
		t.Errorf("method = %q, want url", method)
	}

	var discCount int
	err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM story_sources WHERE story_id=1 AND kind='discussion'`).Scan(&discCount)
	if err != nil {
		t.Fatalf("query discussion: %v", err)
	}
	if discCount != 1 {
		t.Errorf("discussion count = %d, want 1", discCount)
	}
}

func TestAssign_TitleMatchWithin48h(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{
		Sources: []config.Source{
			{Name: "Ars Technica", Tier: 1, URL: "https://arstechnica.com/feed"},
			{Name: "The Verge", Tier: 1, URL: "https://theverge.com/rss/index.xml"},
		},
		PrimaryDomains: map[string]bool{},
	}

	now := time.Now().UTC()
	t0 := now.Format(time.RFC3339)
	t1 := now.Add(2 * time.Hour).Format(time.RFC3339) // within 48h

	items := []testItem{
		{
			URL:          "https://arstechnica.com/gadgets/123/",
			CanonicalURL: "https://arstechnica.com/gadgets/123/",
			Title:        "Cloudflare publishes outage postmortem | Ars Technica",
			Source:       "Ars Technica",
			Tier:         1,
			PublishedAt:  &t0,
			FirstSeen:    t0,
			BodyHash:     "aaa",
		},
		{
			URL:          "https://theverge.com/2024/1/cloudflare-outage",
			CanonicalURL: "https://theverge.com/2024/1/cloudflare-outage",
			Title:        "Cloudflare publishes postmortem on outage — The Verge",
			Source:       "The Verge",
			Tier:         1,
			PublishedAt:  &t1,
			FirstSeen:    t1,
			BodyHash:     "bbb",
		},
	}

	db := seedTestDB(t, items)
	defer db.Close()

	result, err := Assign(ctx, db, cfg)
	if err != nil {
		t.Fatalf("Assign: %v", err)
	}

	if result.StoriesCreated != 1 {
		t.Errorf("stories_created = %d, want 1 (title match)", result.StoriesCreated)
	}

	var method string
	err = db.QueryRowContext(ctx, `SELECT method FROM stories WHERE id=1`).Scan(&method)
	if err != nil {
		t.Fatalf("query method: %v", err)
	}
	if method != "title" {
		t.Errorf("method = %q, want title", method)
	}
}

func TestAssign_TitleMatchOutside48h(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{
		Sources: []config.Source{
			{Name: "Ars Technica", Tier: 1, URL: "https://arstechnica.com/feed"},
			{Name: "The Verge", Tier: 1, URL: "https://theverge.com/rss/index.xml"},
		},
		PrimaryDomains: map[string]bool{},
	}

	now := time.Now().UTC()
	t0 := now.Add(-72 * time.Hour).Format(time.RFC3339) // >48h ago
	t1 := now.Format(time.RFC3339)

	items := []testItem{
		{
			URL:          "https://arstechnica.com/gadgets/123/",
			CanonicalURL: "https://arstechnica.com/gadgets/123/",
			Title:        "Cloudflare publishes outage postmortem | Ars Technica",
			Source:       "Ars Technica",
			Tier:         1,
			PublishedAt:  &t0,
			FirstSeen:    t0,
			BodyHash:     "aaa",
		},
		{
			URL:          "https://theverge.com/2024/1/cloudflare-outage",
			CanonicalURL: "https://theverge.com/2024/1/cloudflare-outage",
			Title:        "Cloudflare publishes postmortem on outage — The Verge",
			Source:       "The Verge",
			Tier:         1,
			PublishedAt:  &t1,
			FirstSeen:    t1,
			BodyHash:     "bbb",
		},
	}

	db := seedTestDB(t, items)
	defer db.Close()

	result, err := Assign(ctx, db, cfg)
	if err != nil {
		t.Fatalf("Assign: %v", err)
	}

	if result.StoriesCreated != 2 {
		t.Errorf("stories_created = %d, want 2 (outside 48h window)", result.StoriesCreated)
	}
}

func TestAssign_Idempotent(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{
		Sources: []config.Source{
			{Name: "Hacker News", Tier: 1, URL: "https://hnrss.org/frontpage"},
		},
		PrimaryDomains: map[string]bool{},
	}

	now := time.Now().UTC().Format(time.RFC3339)
	items := []testItem{
		{
			URL:          "https://example.com/article",
			CanonicalURL: "https://example.com/article",
			Title:        "Test article title here",
			Source:       "Hacker News",
			Tier:         1,
			PublishedAt:  &now,
			FirstSeen:    now,
			BodyHash:     "hash1",
		},
	}

	db := seedTestDB(t, items)
	defer db.Close()

	result1, err := Assign(ctx, db, cfg)
	if err != nil {
		t.Fatalf("first Assign: %v", err)
	}
	if result1.ItemsIn != 1 {
		t.Errorf("first: items_in = %d, want 1", result1.ItemsIn)
	}

	result2, err := Assign(ctx, db, cfg)
	if err != nil {
		t.Fatalf("second Assign: %v", err)
	}
	if result2.ItemsIn != 0 {
		t.Errorf("second: items_in = %d, want 0", result2.ItemsIn)
	}
	if result2.StoriesCreated != 0 {
		t.Errorf("second: stories_created = %d, want 0", result2.StoriesCreated)
	}

	var storyID int64
	err = db.QueryRowContext(ctx, `SELECT id FROM stories LIMIT 1`).Scan(&storyID)
	if err != nil {
		t.Fatalf("query story: %v", err)
	}
	if storyID != 1 {
		t.Errorf("story ID changed to %d, want 1", storyID)
	}
}

func TestAssign_ThirdItemIncreasesSourceCount(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{
		Sources: []config.Source{
			{Name: "Hacker News", Tier: 1, URL: "https://hnrss.org/frontpage"},
			{Name: "Blogspot", Tier: 2, URL: "https://blog.example.com/feed"},
			{Name: "TechCrunch", Tier: 1, URL: "https://techcrunch.com/feed/"},
		},
		PrimaryDomains: map[string]bool{"blog.example.com": true},
	}

	now := time.Now().UTC().Format(time.RFC3339)
	published := now

	items := []testItem{
		{
			URL:          "https://news.ycombinator.com/item?id=1",
			CanonicalURL: "https://blog.example.com/article",
			Title:        "Test article | Hacker News",
			Source:       "Hacker News",
			Tier:         1,
			PublishedAt:  &published,
			FirstSeen:    now,
			BodyHash:     "h1",
			CommentsURL:  "https://news.ycombinator.com/item?id=1",
		},
		{
			URL:          "https://blog.example.com/article",
			CanonicalURL: "https://blog.example.com/article",
			Title:        "Test article title here",
			Source:       "Blogspot",
			Tier:         2,
			PublishedAt:  &published,
			FirstSeen:    now,
			BodyHash:     "b1",
		},
	}

	db := seedTestDB(t, items)
	defer db.Close()

	result1, err := Assign(ctx, db, cfg)
	if err != nil {
		t.Fatalf("first Assign: %v", err)
	}
	if result1.StoriesCreated != 1 {
		t.Fatalf("first: stories_created = %d, want 1", result1.StoriesCreated)
	}

	var sourceCount int
	db.QueryRowContext(ctx, `SELECT source_count FROM stories WHERE id=1`).Scan(&sourceCount)
	if sourceCount != 2 {
		t.Errorf("after first batch: source_count = %d, want 2", sourceCount)
	}

	// Add a third item from a different source.
	_, err = db.ExecContext(ctx,
		`INSERT INTO items(url, canonical_url, title, source, tier, published_at, first_seen, body_hash, comments_url)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"https://techcrunch.com/new-article", "https://blog.example.com/article",
		"Another outlet covers it — TechCrunch", "TechCrunch", 1,
		&published, now, "tc1", nil)
	if err != nil {
		t.Fatalf("insert third item: %v", err)
	}

	result2, err := Assign(ctx, db, cfg)
	if err != nil {
		t.Fatalf("second Assign: %v", err)
	}
	if result2.ItemsIn != 1 {
		t.Errorf("second: items_in = %d, want 1", result2.ItemsIn)
	}

	var newSourceCount int
	db.QueryRowContext(ctx, `SELECT source_count FROM stories WHERE id=1`).Scan(&newSourceCount)
	if newSourceCount != 3 {
		t.Errorf("after third item: source_count = %d, want 3", newSourceCount)
	}

	var storyID int64
	var firstSeen string
	db.QueryRowContext(ctx, `SELECT id, first_seen FROM stories WHERE id=1`).Scan(&storyID, &firstSeen)
	if storyID != 1 {
		t.Errorf("story ID changed to %d, want 1", storyID)
	}
	if result2.StoriesCreated != 0 {
		t.Errorf("second: stories_created = %d, want 0", result2.StoriesCreated)
	}
}

func TestAssign_CanonicalItemPrefersEarliest(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{
		Sources: []config.Source{
			{Name: "Blogspot", Tier: 2, URL: "https://blog.example.com/feed"},
			{Name: "Hacker News", Tier: 1, URL: "https://hnrss.org/frontpage"},
		},
		PrimaryDomains: map[string]bool{"blog.example.com": true},
	}

	now := time.Now().UTC()
	published := now.Add(-24 * time.Hour).Format(time.RFC3339) // earlier
	t0 := now.Format(time.RFC3339)                              // later

	items := []testItem{
		{
			URL:          "https://blog.example.com/article",
			CanonicalURL: "https://blog.example.com/article",
			Title:        "Original article title here",
			Source:       "Blogspot",
			Tier:         2,
			PublishedAt:  &published, // earlier published_at
			FirstSeen:    t0,
			BodyHash:     "b1",
		},
		{
			URL:          "https://news.ycombinator.com/item?id=99",
			CanonicalURL: "https://blog.example.com/article",
			Title:        "Same story | Hacker News",
			Source:       "Hacker News",
			Tier:         1,
			PublishedAt:  &t0, // later published_at
			FirstSeen:    t0,
			BodyHash:     "h1",
			CommentsURL:  "https://news.ycombinator.com/item?id=99",
		},
	}

	db := seedTestDB(t, items)
	defer db.Close()

	_, err := Assign(ctx, db, cfg)
	if err != nil {
		t.Fatalf("Assign: %v", err)
	}

	var canonID int64
	db.QueryRowContext(ctx, `SELECT canonical_item_id FROM stories WHERE id=1`).Scan(&canonID)
	if err != nil {
		t.Fatalf("query canonical: %v", err)
	}

	var canonSource string
	db.QueryRowContext(ctx, `SELECT source FROM items WHERE id=?`, canonID).Scan(&canonSource)
	if canonSource != "Blogspot" {
		t.Errorf("canonical_item_id points to %q, want Blogspot (earliest published_at)", canonSource)
	}
}

func TestAssign_SameSourceCountsOnce(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{
		Sources: []config.Source{
			{Name: "Hacker News", Tier: 1, URL: "https://hnrss.org/frontpage"},
		},
		PrimaryDomains: map[string]bool{},
	}

	now := time.Now().UTC().Format(time.RFC3339)
	published := now

	items := []testItem{
		{
			URL:          "https://news.ycombinator.com/item?id=1",
			CanonicalURL: "https://example.com/article",
			Title:        "Story from HN | Hacker News",
			Source:       "Hacker News",
			Tier:         1,
			PublishedAt:  &published,
			FirstSeen:    now,
			BodyHash:     "h1",
			CommentsURL:  "https://news.ycombinator.com/item?id=1",
		},
		{
			URL:          "https://news.ycombinator.com/item?id=2",
			CanonicalURL: "https://example.com/article",
			Title:        "Another HN link to same story | Hacker News",
			Source:       "Hacker News",
			Tier:         1,
			PublishedAt:  &published,
			FirstSeen:    now,
			BodyHash:     "h2",
			CommentsURL:  "https://news.ycombinator.com/item?id=2",
		},
	}

	db := seedTestDB(t, items)
	defer db.Close()

	_, err := Assign(ctx, db, cfg)
	if err != nil {
		t.Fatalf("Assign: %v", err)
	}

	var sourceCount int
	db.QueryRowContext(ctx, `SELECT source_count FROM stories WHERE id=1`).Scan(&sourceCount)
	if sourceCount != 1 {
		t.Errorf("source_count = %d, want 1 (same source counted once)", sourceCount)
	}
}

func TestAssign_StoryIDsStable(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{
		Sources: []config.Source{
			{Name: "Hacker News", Tier: 1, URL: "https://hnrss.org/frontpage"},
			{Name: "Blogspot", Tier: 2, URL: "https://blog.example.com/feed"},
		},
		PrimaryDomains: map[string]bool{"blog.example.com": true},
	}

	now := time.Now().UTC().Format(time.RFC3339)
	published := now

	items := []testItem{
		{
			URL:          "https://news.ycombinator.com/item?id=1",
			CanonicalURL: "https://blog.example.com/a",
			Title:        "First story | Hacker News",
			Source:       "Hacker News",
			Tier:         1,
			PublishedAt:  &published,
			FirstSeen:    now,
			BodyHash:     "h1",
			CommentsURL:  "https://news.ycombinator.com/item?id=1",
		},
		{
			URL:          "https://news.ycombinator.com/item?id=2",
			CanonicalURL: "https://blog.example.com/b",
			Title:        "Second story | Hacker News",
			Source:       "Hacker News",
			Tier:         1,
			PublishedAt:  &published,
			FirstSeen:    now,
			BodyHash:     "h2",
			CommentsURL:  "https://news.ycombinator.com/item?id=2",
		},
	}

	db := seedTestDB(t, items)
	defer db.Close()

	_, err := Assign(ctx, db, cfg)
	if err != nil {
		t.Fatalf("first Assign: %v", err)
	}

	rows, err := db.QueryContext(ctx, `SELECT id, canonical_item_id FROM stories ORDER BY id`)
	if err != nil {
		t.Fatalf("query stories: %v", err)
	}
	type storyID struct {
		ID      int64
		CanonID int64
	}
	var originalIDs []storyID
	for rows.Next() {
		var s storyID
		rows.Scan(&s.ID, &s.CanonID)
		originalIDs = append(originalIDs, s)
	}
	rows.Close()

	if len(originalIDs) != 2 {
		t.Fatalf("expected 2 stories after first run, got %d", len(originalIDs))
	}

	items2 := []testItem{
		{
			URL:          "https://news.ycombinator.com/item?id=3",
			CanonicalURL: "https://blog.example.com/c",
			Title:        "Third story | Hacker News",
			Source:       "Hacker News",
			Tier:         1,
			PublishedAt:  &published,
			FirstSeen:    now,
			BodyHash:     "h3",
			CommentsURL:  "https://news.ycombinator.com/item?id=3",
		},
	}

	for _, it := range items2 {
		var commentsURL sql.NullString
		if it.CommentsURL != "" {
			commentsURL = sql.NullString{String: it.CommentsURL, Valid: true}
		}
		_, err := db.ExecContext(ctx,
			`INSERT INTO items(url, canonical_url, title, source, tier, published_at, first_seen, body_hash, comments_url)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			it.URL, it.CanonicalURL, it.Title, it.Source, it.Tier,
			it.PublishedAt, it.FirstSeen, it.BodyHash, commentsURL)
		if err != nil {
			t.Fatalf("insert item: %v", err)
		}
	}

	result2, err := Assign(ctx, db, cfg)
	if err != nil {
		t.Fatalf("second Assign: %v", err)
	}

	rows, err = db.QueryContext(ctx, `SELECT id, canonical_item_id FROM stories ORDER BY id`)
	if err != nil {
		t.Fatalf("query stories after second run: %v", err)
	}
	var newIDs []storyID
	for rows.Next() {
		var s storyID
		rows.Scan(&s.ID, &s.CanonID)
		newIDs = append(newIDs, s)
	}
	rows.Close()

	origMap := make(map[int64]int64)
	for _, s := range originalIDs {
		origMap[s.ID] = s.CanonID
	}

	if len(newIDs) != 3 {
		t.Fatalf("expected 3 stories after second run, got %d", len(newIDs))
	}

	for _, orig := range originalIDs {
		found := false
		for _, newS := range newIDs {
			if newS.ID == orig.ID {
				found = true
				if newS.CanonID != orig.CanonID {
					t.Errorf("story %d canonical_item_id changed from %d to %d",
						newS.ID, orig.CanonID, newS.CanonID)
				}
				break
			}
		}
		if !found {
			t.Errorf("original story ID %d not found after second run", orig.ID)
		}
	}

	if result2.StoriesCreated != 1 {
		t.Errorf("second: stories_created = %d, want 1", result2.StoriesCreated)
	}
}
