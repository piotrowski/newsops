package candidates

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"news-ops/internal/config"
	"news-ops/internal/store"

	_ "modernc.org/sqlite"
)

// openTestDB opens an in-memory SQLite database with foreign keys enabled and runs migrations.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	ctx := context.Background()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}
// defaultConfig returns a minimal config suitable for testing.
func defaultConfig() *config.Config {
	return &config.Config{
		PrimaryDomains: map[string]bool{"example.com": true, "arxiv.org": true},
		Prefilter: config.Prefilter{
			CandidateLimit:  40,
			PerSourceCap:    3,
			TitleSimhashMax: 3,
			MaxBackfillDays: 30,
			ExcludeTerms:    []string{},
		},
	}
}

type itemFixture struct {
	URL          string
	CanonicalURL string
	Title        string
	Source       string
	Tier         int
	PublishedAt  string
	FirstSeen    string
	BodyHash     string
	Excerpt      string
}

type storyFixture struct {
	CanonicalItemURL string
	SourceCount      int
	Method           string
	FirstSeen        string
	LastSeen         string
	ShownAt          *string // nil = not shown
}

func seedItemsAndStories(t *testing.T, db *sql.DB, items []itemFixture, stories []storyFixture) {
	t.Helper()
	ctx := context.Background()

	// Insert items.
	for _, it := range items {
		_, err := db.ExecContext(ctx,
			`INSERT INTO items (url, canonical_url, title, source, tier, published_at, first_seen, body_hash, body_excerpt)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			it.URL, it.CanonicalURL, it.Title, it.Source, it.Tier,
			it.PublishedAt, it.FirstSeen, it.BodyHash, it.Excerpt)
		if err != nil {
			t.Fatalf("insert item %s: %v", it.Title, err)
		}
	}

	// Collect canonical item IDs.
	itemIDs := make(map[string]int64)
	for _, it := range items {
		var id int64
		err := db.QueryRowContext(ctx, `SELECT id FROM items WHERE url=?`, it.URL).Scan(&id)
		if err != nil {
			t.Fatalf("lookup item %s: %v", it.URL, err)
		}
		itemIDs[it.URL] = id
	}

	// Insert stories.
	for _, st := range stories {
		canonicalID := itemIDs[st.CanonicalItemURL]
		var shownAt sql.NullString
		if st.ShownAt != nil {
			shownAt = sql.NullString{String: *st.ShownAt, Valid: true}
		}
		_, err := db.ExecContext(ctx,
			`INSERT INTO stories (canonical_item_id, source_count, method, first_seen, last_seen, shown_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			canonicalID, st.SourceCount, st.Method,
			st.FirstSeen, st.LastSeen, shownAt)
		if err != nil {
			t.Fatalf("insert story for %s: %v", st.CanonicalItemURL, err)
		}
	}
}

func TestRun_emptyWindow(t *testing.T) {
	db := openTestDB(t)
	cfg := defaultConfig()

	result, err := Run(context.Background(), db, cfg, time.Now().Add(-24*time.Hour), false)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.TotalInWindow != 0 {
		t.Errorf("total_in_window = %d, want 0", result.TotalInWindow)
	}
	if len(result.Candidates) != 0 {
		t.Errorf("got %d candidates, want 0", len(result.Candidates))
	}
}

func TestRun_shownExcluded(t *testing.T) {
	now := time.Now().UTC()
	since := now.Add(-24 * time.Hour).Format(time.RFC3339)
	lastSeen := now.Add(-12 * time.Hour).Format(time.RFC3339)

	items := []itemFixture{
		{URL: "https://a.com/1", CanonicalURL: "https://a.com/1", Title: "Story one", Source: "A", Tier: 1, PublishedAt: since, FirstSeen: since, BodyHash: "h1", Excerpt: "excerpt one"},
		{URL: "https://b.com/2", CanonicalURL: "https://b.com/2", Title: "Story two", Source: "B", Tier: 1, PublishedAt: since, FirstSeen: since, BodyHash: "h2", Excerpt: "excerpt two"},
	}
	shownAt := now.Add(-6 * time.Hour).Format(time.RFC3339)
	stories := []storyFixture{
		{CanonicalItemURL: "https://a.com/1", SourceCount: 1, Method: "url", FirstSeen: since, LastSeen: lastSeen, ShownAt: &shownAt},
		{CanonicalItemURL: "https://b.com/2", SourceCount: 1, Method: "url", FirstSeen: since, LastSeen: lastSeen, ShownAt: nil},
	}

	db := openTestDB(t)
	seedItemsAndStories(t, db, items, stories)
	cfg := defaultConfig()

	result, err := Run(context.Background(), db, cfg, time.Now().Add(-24*time.Hour), false)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.Candidates) != 1 {
		t.Fatalf("got %d candidates, want 1 (shown story excluded)", len(result.Candidates))
	}
	if result.Candidates[0].StoryID == 1 {
		t.Error("shown story should be excluded")
	}

	// With include-shown, both appear.
	result2, err := Run(context.Background(), db, cfg, time.Now().Add(-24*time.Hour), true)
	if err != nil {
		t.Fatalf("Run with include-shown: %v", err)
	}
	if len(result2.Candidates) != 2 {
		t.Fatalf("include-shown: got %d candidates, want 2", len(result2.Candidates))
	}
}

func TestRun_excludeTerms(t *testing.T) {
	now := time.Now().UTC()
	since := now.Add(-24 * time.Hour).Format(time.RFC3339)
	lastSeen := now.Add(-12 * time.Hour).Format(time.RFC3339)

	items := []itemFixture{
		{URL: "https://a.com/1", CanonicalURL: "https://a.com/1", Title: "Cloudflare outage report", Source: "A", Tier: 1, PublishedAt: since, FirstSeen: since, BodyHash: "h1", Excerpt: "outage details"},
		{URL: "https://b.com/2", CanonicalURL: "https://b.com/2", Title: "Cloudflare funding round announcement", Source: "B", Tier: 1, PublishedAt: since, FirstSeen: since, BodyHash: "h2", Excerpt: "funding news"},
	}
	stories := []storyFixture{
		{CanonicalItemURL: "https://a.com/1", SourceCount: 1, Method: "url", FirstSeen: since, LastSeen: lastSeen, ShownAt: nil},
		{CanonicalItemURL: "https://b.com/2", SourceCount: 1, Method: "url", FirstSeen: since, LastSeen: lastSeen, ShownAt: nil},
	}

	db := openTestDB(t)
	seedItemsAndStories(t, db, items, stories)
	cfg := defaultConfig()
	cfg.Prefilter.ExcludeTerms = []string{"funding", "valuation"}

	result, err := Run(context.Background(), db, cfg, time.Now().Add(-24*time.Hour), false)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.Candidates) != 1 {
		t.Fatalf("got %d candidates, want 1 (funding excluded)", len(result.Candidates))
	}
	if result.Candidates[0].Title != "Cloudflare outage report" {
		t.Errorf("wrong candidate: %s", result.Candidates[0].Title)
	}
	if result.Dropped.Excluded != 1 {
		t.Errorf("dropped.excluded = %d, want 1", result.Dropped.Excluded)
	}
}

func TestRun_perSourceCap(t *testing.T) {
	now := time.Now().UTC()
	since := now.Add(-24 * time.Hour).Format(time.RFC3339)
	lastSeen := now.Add(-12 * time.Hour).Format(time.RFC3339)

	var items []itemFixture
	var stories []storyFixture
	for i := 1; i <= 5; i++ {
		url := "https://arxiv.org/abs/2401." + string(rune('0'+i))
		items = append(items, itemFixture{
			URL: url, CanonicalURL: url, Title: "arXiv paper " + string(rune('0'+i)),
			Source: "arXiv", Tier: 2, PublishedAt: since, FirstSeen: since, BodyHash: "h" + string(rune('0'+i)), Excerpt: "paper excerpt " + string(rune('0'+i)),
		})
		stories = append(stories, storyFixture{
			CanonicalItemURL: url, SourceCount: 1, Method: "url", FirstSeen: since, LastSeen: lastSeen, ShownAt: nil,
		})
	}

	db := openTestDB(t)
	seedItemsAndStories(t, db, items, stories)
	cfg := defaultConfig()
	cfg.Prefilter.PerSourceCap = 3

	result, err := Run(context.Background(), db, cfg, time.Now().Add(-24*time.Hour), false)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	arXivCount := 0
	for _, c := range result.Candidates {
		if c.Source == "arXiv" {
			arXivCount++
		}
	}
	if arXivCount != 3 {
		t.Errorf("arXiv count = %d, want %d (per_source_cap=3)", arXivCount, 3)
	}
}

func TestRun_limit(t *testing.T) {
	now := time.Now().UTC()
	since := now.Add(-24 * time.Hour).Format(time.RFC3339)
	lastSeen := now.Add(-12 * time.Hour).Format(time.RFC3339)

	var items []itemFixture
	var stories []storyFixture
	for i := 1; i <= 10; i++ {
		url := "https://example.com/" + string(rune('0'+i))
		items = append(items, itemFixture{
			URL: url, CanonicalURL: url, Title: "Story " + string(rune('0'+i)),
			Source: "E", Tier: 1, PublishedAt: since, FirstSeen: since, BodyHash: "h" + string(rune('0'+i)), Excerpt: "excerpt " + string(rune('0'+i)),
		})
		stories = append(stories, storyFixture{
			CanonicalItemURL: url, SourceCount: 1, Method: "url", FirstSeen: since, LastSeen: lastSeen, ShownAt: nil,
		})
	}

	db := openTestDB(t)
	seedItemsAndStories(t, db, items, stories)
	cfg := defaultConfig()
	cfg.Prefilter.CandidateLimit = 5
	cfg.Prefilter.PerSourceCap = 10

	result, err := Run(context.Background(), db, cfg, time.Now().Add(-24*time.Hour), false)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.Candidates) != 5 {
		t.Errorf("candidates = %d, want 5 (candidate_limit=5)", len(result.Candidates))
	}
	if result.Returned != 5 {
		t.Errorf("returned = %d, want 5", result.Returned)
	}
}

func TestRun_determinism(t *testing.T) {
	now := time.Now().UTC()
	since := now.Add(-24 * time.Hour).Format(time.RFC3339)
	lastSeen := now.Add(-12 * time.Hour).Format(time.RFC3339)

	var items []itemFixture
	var stories []storyFixture
	for i := 1; i <= 6; i++ {
		url := "https://example.com/" + string(rune('0'+i))
		tier := 1
		if i > 3 {
			tier = 2
		}
		items = append(items, itemFixture{
			URL: url, CanonicalURL: url, Title: "Story " + string(rune('0'+i)),
			Source: "S", Tier: tier, PublishedAt: since, FirstSeen: since, BodyHash: "h" + string(rune('0'+i)), Excerpt: "excerpt " + string(rune('0'+i)),
		})
		stories = append(stories, storyFixture{
			CanonicalItemURL: url, SourceCount: 3 - i/2, Method: "url", FirstSeen: since, LastSeen: lastSeen, ShownAt: nil,
		})
	}

	db := openTestDB(t)
	seedItemsAndStories(t, db, items, stories)
	cfg := defaultConfig()

	result1, err := Run(context.Background(), db, cfg, time.Now().Add(-24*time.Hour), false)
	if err != nil {
		t.Fatalf("Run 1: %v", err)
	}
	result2, err := Run(context.Background(), db, cfg, time.Now().Add(-24*time.Hour), false)
	if err != nil {
		t.Fatalf("Run 2: %v", err)
	}

	json1, _ := json.Marshal(result1.Candidates)
	json2, _ := json.Marshal(result2.Candidates)
	if string(json1) != string(json2) {
		t.Errorf("two runs produced different output:\nrun1: %s\nrun2: %s", json1, json2)
	}
}

func TestRun_excerptPresent(t *testing.T) {
	now := time.Now().UTC()
	since := now.Add(-24 * time.Hour).Format(time.RFC3339)
	lastSeen := now.Add(-12 * time.Hour).Format(time.RFC3339)

	items := []itemFixture{
		{URL: "https://a.com/1", CanonicalURL: "https://a.com/1", Title: "Story one", Source: "A", Tier: 1, PublishedAt: since, FirstSeen: since, BodyHash: "h1", Excerpt: "this is an excerpt"},
	}
	stories := []storyFixture{
		{CanonicalItemURL: "https://a.com/1", SourceCount: 1, Method: "url", FirstSeen: since, LastSeen: lastSeen, ShownAt: nil},
	}

	db := openTestDB(t)
	seedItemsAndStories(t, db, items, stories)
	cfg := defaultConfig()

	result, err := Run(context.Background(), db, cfg, time.Now().Add(-24*time.Hour), false)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.Candidates) != 1 {
		t.Fatalf("got %d candidates, want 1", len(result.Candidates))
	}
	if result.Candidates[0].Excerpt == "" {
		t.Error("excerpt is empty, want non-empty")
	}
}

func TestRun_ordering(t *testing.T) {
	now := time.Now().UTC()
	since := now.Add(-24 * time.Hour).Format(time.RFC3339)
	lastSeen := now.Add(-12 * time.Hour).Format(time.RFC3339)

	var items []itemFixture
	var stories []storyFixture
	type storyOrder struct {
		id       int
		title    string
		source   string
		tier     int
		srcCount int
	}
	order := []storyOrder{
		{id: 5, title: "Low tier high count", source: "A", tier: 2, srcCount: 5},
		{id: 3, title: "High tier low count", source: "B", tier: 1, srcCount: 2},
		{id: 1, title: "High tier same count", source: "C", tier: 1, srcCount: 4},
		{id: 2, title: "Low tier same count", source: "D", tier: 2, srcCount: 5},
		{id: 4, title: "High tier medium count", source: "E", tier: 1, srcCount: 3},
	}

	for _, o := range order {
		url := "https://example.com/" + string(rune('0'+o.id))
		items = append(items, itemFixture{
			URL: url, CanonicalURL: url, Title: o.title, Source: o.source, Tier: o.tier,
			PublishedAt: since, FirstSeen: since, BodyHash: "h" + string(rune('0'+o.id)), Excerpt: "excerpt",
		})
		stories = append(stories, storyFixture{
			CanonicalItemURL: url, SourceCount: o.srcCount, Method: "url", FirstSeen: since, LastSeen: lastSeen, ShownAt: nil,
		})
	}

	db := openTestDB(t)
	seedItemsAndStories(t, db, items, stories)
	cfg := defaultConfig()

	result, err := Run(context.Background(), db, cfg, time.Now().Add(-24*time.Hour), false)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Expected order: tier 1 first (sorted by source_count desc, then story_id asc), then tier 2.
	// Tier 1: id=1 (srcCount=4), id=4 (srcCount=3), id=3 (srcCount=2)
	// Tier 2: id=2 (srcCount=5), id=5 (srcCount=5) — same srcCount, so by story_id asc
	expectedIDs := []int64{3, 5, 2, 1, 4}
	for i, wantID := range expectedIDs {
		if i >= len(result.Candidates) {
			t.Fatalf("index %d: got %d candidates, expected at least %d", i, len(result.Candidates), len(expectedIDs))
		}
		if result.Candidates[i].StoryID != wantID {
			t.Errorf("position %d: story_id = %d, want %d", i, result.Candidates[i].StoryID, wantID)
		}
	}
}
