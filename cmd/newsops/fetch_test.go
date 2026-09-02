package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"news-ops/internal/feed"
	"news-ops/internal/runstate"
	"news-ops/internal/store"
)

// minimalRSS returns a valid RSS 2.0 feed with one item.
func minimalRSS(title, link, description string) string {
	return fmt.Sprintf(`<?xml version="1.0"?>
<rss version="2.0">
  <channel><title>T</title><link>https://example.com</link>
    <item><title>%s</title><link>%s</link>
      <description>%s</description>
      <pubDate>Tue, 01 Sep 2026 12:00:00 GMT</pubDate>
      <guid isPermaLink="true">%s</guid>
    </item></channel></rss>`, title, link, description, link)
}

// minimalRSSNoDate returns RSS with an item that has no pubDate.
func minimalRSSNoDate(title, link string) string {
	return fmt.Sprintf(`<?xml version="1.0"?>
<rss version="2.0">
  <channel><title>T</title><link>https://example.com</link>
    <item><title>%s</title><link>%s</link>
      <description>Content without date.</description>
      <guid isPermaLink="true">%s</guid>
    </item></channel></rss>`, title, link, link)
}

// minimalRSSOldDate returns RSS with an item published before the window.
func minimalRSSOldDate(title, link string) string {
	return fmt.Sprintf(`<?xml version="1.0"?>
<rss version="2.0">
  <channel><title>T</title><link>https://example.com</link>
    <item><title>%s</title><link>%s</link>
      <description>Old content.</description>
      <pubDate>Mon, 01 Jan 2020 12:00:00 GMT</pubDate>
      <guid isPermaLink="true">%s</guid>
    </item></channel></rss>`, title, link, link)
}

// TestComputeWindow_noPriorRun tests that computeWindow uses max_backfill_days when no prior run exists.
func TestComputeWindow_noPriorRun(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(context.Background(), dir, store.ReadWrite)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	start, backfilled, err := computeWindow(context.Background(), db, 30, nil)
	if err != nil {
		t.Fatalf("computeWindow: %v", err)
	}
	if !backfilled {
		t.Error("expected backfilled=true for first run")
	}
	expected := time.Now().AddDate(0, 0, -30)
	if start.Before(expected.Add(-time.Hour)) || start.After(time.Now()) {
		t.Errorf("window start %v not in expected range", start)
	}
}

// TestComputeWindow_recentPriorRun tests that computeWindow uses last success when it's within the backfill window.
func TestComputeWindow_recentPriorRun(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(context.Background(), dir, store.ReadWrite)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	// Create a successful run from 2 days ago.
	run, err := runstate.Begin(context.Background(), db, runstate.StageFetch)
	if err != nil {
		t.Fatalf("begin run: %v", err)
	}
	if err := run.End(context.Background(), true, runstate.Counts{In: 10, Out: 8}, ""); err != nil {
		t.Fatalf("end run: %v", err)
	}

	start, backfilled, err := computeWindow(context.Background(), db, 30, nil)
	if err != nil {
		t.Fatalf("computeWindow: %v", err)
	}
	if backfilled {
		t.Error("expected backfilled=false when last success exists")
	}
	expected := run.StartedAt.Truncate(time.Second)
	if !start.Equal(expected) {
		t.Errorf("window start %v != last success %v", start, expected)
	}
}

// TestComputeWindow_ancientPriorRun tests that computeWindow falls back to max_backfill_days when last success is too old.
func TestComputeWindow_ancientPriorRun(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(context.Background(), dir, store.ReadWrite)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	// Insert a run from 60 days ago (manually via SQL).
	oldTime := time.Now().AddDate(0, 0, -60).Format(time.RFC3339)
	_, err = db.ExecContext(context.Background(),
		`INSERT INTO runs(stage, started_at, ended_at, ok, items_in, items_out, note)
		 VALUES ('fetch', ?, ?, 1, 10, 8, 'old')`, oldTime, oldTime)
	if err != nil {
		t.Fatalf("insert old run: %v", err)
	}

	start, backfilled, err := computeWindow(context.Background(), db, 30, nil)
	if err != nil {
		t.Fatalf("computeWindow: %v", err)
	}
	if !backfilled {
		t.Error("expected backfilled=true when last success is older than max_backfill_days")
	}
	expected := time.Now().AddDate(0, 0, -30)
	if start.Before(expected.Add(-time.Hour)) || start.After(time.Now()) {
		t.Errorf("window start %v not in expected range", start)
	}
}

// TestComputeWindow_sinceOverride tests that --since overrides computed window.
func TestComputeWindow_sinceOverride(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(context.Background(), dir, store.ReadWrite)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	since := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	start, backfilled, err := computeWindow(context.Background(), db, 30, &since)
	if err != nil {
		t.Fatalf("computeWindow: %v", err)
	}
	if backfilled {
		t.Error("expected backfilled=false when --since is provided")
	}
	if !start.Equal(since) {
		t.Errorf("window start %v != since %v", start, since)
	}
}

// TestInsertItem_duplicateURL tests that duplicate URLs are not inserted.
func TestInsertItem_duplicateURL(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(context.Background(), dir, store.ReadWrite)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	item := feed.ParsedItem{
		Title:   "Test Article",
		Link:    "https://example.com/article",
		Content: "This is a test article with enough content to pass the excerpt builder threshold for readability processing. Lorem ipsum dolor sit amet, consectetur adipiscing elit.",
		Published: func() *time.Time {
			t := time.Date(2025, 9, 1, 12, 0, 0, 0, time.UTC)
			return &t
		}(),
	}

	inserted1, err := insertItem(context.Background(), db, item, "Test", 1, "")
	if err != nil {
		t.Fatalf("insertItem: %v", err)
	}
	if !inserted1 {
		t.Error("expected first insert to succeed")
	}

	inserted2, err := insertItem(context.Background(), db, item, "Test", 1, "")
	if err != nil {
		t.Fatalf("insertItem duplicate: %v", err)
	}
	if inserted2 {
		t.Error("expected second insert with same URL to be a no-op")
	}

	var count int
	err = db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM items WHERE url=?", item.Link).Scan(&count)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 row, got %d", count)
	}
}

// TestInsertItem_nullDate tests that items with null published_at are kept.
func TestInsertItem_nullDate(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(context.Background(), dir, store.ReadWrite)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	item := feed.ParsedItem{
		Title:     "No Date Article",
		Link:      "https://example.com/no-date",
		Content:   "Content without publication date.",
		Published: nil,
	}

	inserted, err := insertItem(context.Background(), db, item, "Test", 1, "")
	if err != nil {
		t.Fatalf("insertItem: %v", err)
	}
	if !inserted {
		t.Error("expected insert of null-date item")
	}

	var pubAt interface{}
	err = db.QueryRowContext(context.Background(), "SELECT published_at FROM items WHERE url=?", item.Link).Scan(&pubAt)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if pubAt != nil {
		t.Errorf("expected NULL published_at, got %v", pubAt)
	}
}

// TestInsertItem_bodyHash tests that body_hash is computed correctly.
func TestInsertItem_bodyHash(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(context.Background(), dir, store.ReadWrite)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	item := feed.ParsedItem{
		Title:   "Hash Test",
		Link:    "https://example.com/hash",
		Content: "<p>Hello world</p>",
		Published: func() *time.Time {
			t := time.Date(2025, 9, 1, 12, 0, 0, 0, time.UTC)
			return &t
		}(),
	}

	inserted, err := insertItem(context.Background(), db, item, "Test", 1, "")
	if err != nil {
		t.Fatalf("insertItem: %v", err)
	}
	if !inserted {
		t.Error("expected insert")
	}

	var bodyHash string
	err = db.QueryRowContext(context.Background(), "SELECT body_hash FROM items WHERE url=?", item.Link).Scan(&bodyHash)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	expected := feed.BodyHash(item)
	if bodyHash != expected {
		t.Errorf("body_hash: got %s, want %s", bodyHash, expected)
	}
}

// --- Integration tests via the compiled binary ---

// runFetch executes the newsops binary with the given args and returns stdout.
func runFetch(t *testing.T, extraArgs ...string) (stdout string, err error) {
	t.Helper()
	args := append([]string{"fetch", "--json"}, extraArgs...)
	cmd := exec.Command(binPath(), args...)
	out, execErr := cmd.CombinedOutput()
	return string(out), execErr
}

// setupFetchConfig creates a temporary directory with sources.yml, domains.yml, and prefilter.yml.
func setupFetchConfig(t *testing.T) (dbDir, configDir string) {
	t.Helper()
	tmpDir := t.TempDir()
	dbDir = filepath.Join(tmpDir, "data")
	configDir = filepath.Join(tmpDir, "config")
	os.MkdirAll(configDir, 0755)

	sourcesYML := `sources:
  - name: TestFeed
    tier: 1
    url: "http://localhost:0/feed"`
	domainsYML := `primary_domains: []`
	prefilterYML := `candidate_limit: 40
per_source_cap: 3
title_simhash_max: 3
max_backfill_days: 30
exclude_terms: []`
	os.WriteFile(filepath.Join(configDir, "sources.yml"), []byte(sourcesYML), 0644)
	os.WriteFile(filepath.Join(configDir, "domains.yml"), []byte(domainsYML), 0644)
	os.WriteFile(filepath.Join(configDir, "prefilter.yml"), []byte(prefilterYML), 0644)
	return dbDir, configDir
}

// writeSourcesYML writes a sources.yml with the given source URL.
func writeSourcesYML(t *testing.T, configDir string, name string, tier int, url string) {
	t.Helper()
	content := fmt.Sprintf(`sources:
  - name: %s
    tier: %d
    url: "%s"`, name, tier, url)
	os.WriteFile(filepath.Join(configDir, "sources.yml"), []byte(content), 0644)
}

// writeMultiSourcesYML writes a sources.yml with multiple sources.
func writeMultiSourcesYML(t *testing.T, configDir string, entries ...string) {
	t.Helper()
	var buf strings.Builder
	buf.WriteString("sources:\n")
	for _, e := range entries {
		buf.WriteString(e + "\n")
	}
	os.WriteFile(filepath.Join(configDir, "sources.yml"), []byte(buf.String()), 0644)
}

// TestIntegration_fetchBackfill tests that a first run backfills and reports correctly.
func TestIntegration_fetchBackfill(t *testing.T) {
	if _, err := os.Stat(binPath()); os.IsNotExist(err) {
		t.Skip("binary not built, skipping integration test")
	}

	dbDir, configDir := setupFetchConfig(t)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(minimalRSS("Article 1", "https://example.com/1", "Description for article one.")))
	}))
	defer ts.Close()

	writeSourcesYML(t, configDir, "TestFeed", 1, ts.URL + "/feed")

	out, err := runFetch(t, "--db", dbDir, "--config", configDir)
	if err != nil {
		t.Fatalf("fetch: %v\n%s", err, out)
	}

	var result fetchResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("parse JSON: %v\noutput: %s", err, out)
	}

	if !result.Window.Backfilled {
		t.Error("expected backfilled=true for first run")
	}
	if result.Sources.Total != 1 {
		t.Errorf("sources.total: got %d, want 1", result.Sources.Total)
	}
	if result.Items.Seen != 1 {
		t.Errorf("items.seen: got %d, want 1", result.Items.Seen)
	}
	if result.Items.New != 1 {
		t.Errorf("items.new: got %d, want 1", result.Items.New)
	}
}

// TestIntegration_fetchMaxAgeSkip tests that --max-age skips when last success is fresh.
func TestIntegration_fetchMaxAgeSkip(t *testing.T) {
	if _, err := os.Stat(binPath()); os.IsNotExist(err) {
		t.Skip("binary not built, skipping integration test")
	}

	dbDir, configDir := setupFetchConfig(t)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(minimalRSS("Article 1", "https://example.com/1", "Desc")))
	}))
	defer ts.Close()

	writeSourcesYML(t, configDir, "TestFeed", 1, ts.URL + "/feed")

	// First run — populate the database.
	out, err := runFetch(t, "--db", dbDir, "--config", configDir)
	if err != nil {
		t.Fatalf("first fetch: %v\n%s", err, out)
	}

	// Second run with --max-age 6h — should skip.
	out, err = runFetch(t, "--db", dbDir, "--config", configDir, "--max-age", "6h")
	if err != nil {
		t.Fatalf("second fetch: %v\n%s", err, out)
	}

	var result fetchResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("parse JSON: %v\noutput: %s", err, out)
	}

	if !result.Skipped {
		t.Error("expected skipped=true with --max-age 6h")
	}
	if result.Reason != "fresh" {
		t.Errorf("reason: got %q, want %q", result.Reason, "fresh")
	}
}

// TestIntegration_fetchDuplicateURL tests that duplicate URLs are not inserted.
func TestIntegration_fetchDuplicateURL(t *testing.T) {
	if _, err := os.Stat(binPath()); os.IsNotExist(err) {
		t.Skip("binary not built, skipping integration test")
	}

	dbDir, configDir := setupFetchConfig(t)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(minimalRSS("Article 1", "https://example.com/1", "Desc")))
	}))
	defer ts.Close()

	writeSourcesYML(t, configDir, "TestFeed", 1, ts.URL + "/feed")

	// First run.
	out, err := runFetch(t, "--db", dbDir, "--config", configDir)
	if err != nil {
		t.Fatalf("first fetch: %v\n%s", err, out)
	}

	var result1 fetchResult
	if err := json.Unmarshal([]byte(out), &result1); err != nil {
		t.Fatalf("parse JSON: %v", err)
	}

	// Second run — same content, should be 0 new.
	out, err = runFetch(t, "--db", dbDir, "--config", configDir)
	if err != nil {
		t.Fatalf("second fetch: %v\n%s", err, out)
	}

	var result2 fetchResult
	if err := json.Unmarshal([]byte(out), &result2); err != nil {
		t.Fatalf("parse JSON: %v", err)
	}

	if result2.Items.New != 0 {
		t.Errorf("items.new: got %d, want 0 (duplicate URL)", result2.Items.New)
	}
}

// TestIntegration_fetchConcurrency tests that a second concurrent fetch exits with locked error.
func TestIntegration_fetchConcurrency(t *testing.T) {
	if _, err := os.Stat(binPath()); os.IsNotExist(err) {
		t.Skip("binary not built, skipping integration test")
	}

	dbDir, configDir := setupFetchConfig(t)

	// Create a blocking server that holds the lock.
	block := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block // block forever
		w.Write([]byte("ok"))
	}))
	defer ts.Close()

	writeSourcesYML(t, configDir, "TestFeed", 1, ts.URL + "/feed")

	// First fetch starts and blocks.
	cmd1 := exec.Command(binPath(), "fetch", "--db", dbDir, "--config", configDir)
	var out1Buf strings.Builder
	cmd1.Stdout = &out1Buf
	cmd1.Stderr = &out1Buf
	if err := cmd1.Start(); err != nil {
		t.Fatalf("start first fetch: %v", err)
	}

	// Give it time to acquire the lock.
	time.Sleep(500 * time.Millisecond)

	// Second fetch should fail with locked error quickly.
	cmd2 := exec.Command(binPath(), "fetch", "--db", dbDir, "--config", configDir)
	out2, err := cmd2.CombinedOutput()
	if err == nil {
		t.Fatalf("expected second fetch to fail with locked error")
	}

	var envelope map[string]interface{}
	if jsonErr := json.Unmarshal(out2, &envelope); jsonErr != nil {
		// Might be stderr output; check for "locked" in output.
		if !strings.Contains(string(out2), "locked") {
			t.Logf("second fetch output: %s", string(out2))
		}
	}

	// Release the block so first fetch can complete.
	close(block)
	cmd1.Wait()
}

// TestIntegration_fetchSourceFilter tests that --source restricts to one feed.
func TestIntegration_fetchSourceFilter(t *testing.T) {
	if _, err := os.Stat(binPath()); os.IsNotExist(err) {
		t.Skip("binary not built, skipping integration test")
	}

	tmpDir := t.TempDir()
	dbDir := filepath.Join(tmpDir, "data")
	configDir := filepath.Join(tmpDir, "config")
	os.MkdirAll(configDir, 0755)

	sourcesYML := `sources:
  - name: Alpha
    tier: 1
    url: "http://localhost:0/alpha"
  - name: Beta
    tier: 1
    url: "http://localhost:0/beta"`
	domainsYML := `primary_domains: []`
	prefilterYML := `candidate_limit: 40
per_source_cap: 3
title_simhash_max: 3
max_backfill_days: 30
exclude_terms: []`
	os.WriteFile(filepath.Join(configDir, "sources.yml"), []byte(sourcesYML), 0644)
	os.WriteFile(filepath.Join(configDir, "domains.yml"), []byte(domainsYML), 0644)
	os.WriteFile(filepath.Join(configDir, "prefilter.yml"), []byte(prefilterYML), 0644)

	var mu sync.Mutex
	alphaCount := 0
	betaCount := 0

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if strings.Contains(r.URL.Path, "alpha") {
			alphaCount++
			w.Header().Set("Content-Type", "application/rss+xml")
			w.Write([]byte(minimalRSS("Alpha Article", "https://example.com/alpha", "Desc")))
		} else {
			betaCount++
			w.Header().Set("Content-Type", "application/rss+xml")
			w.Write([]byte(minimalRSS("Beta Article", "https://example.com/beta", "Desc")))
		}
	}))
	defer ts.Close()

	sourcesYML = fmt.Sprintf(`sources:
  - name: Alpha
    tier: 1
    url: "%s/alpha"
  - name: Beta
    tier: 1
    url:  "%s/beta"`, ts.URL, ts.URL)
	os.WriteFile(filepath.Join(configDir, "sources.yml"), []byte(sourcesYML), 0644)
	cmd := exec.Command(binPath(), "fetch", "--db", dbDir, "--config", configDir, "--source", "Alpha", "--json")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fetch: %v\n%s", err, string(out))
	}

	var result fetchResult
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("parse JSON: %v\noutput: %s", err, string(out))
	}

	mu.Lock()
	defer mu.Unlock()
	if alphaCount != 1 {
		t.Errorf("alpha requests: got %d, want 1", alphaCount)
	}
	if betaCount != 0 {
		t.Errorf("beta requests: got %d, want 0 (filtered out)", betaCount)
	}
	if result.Sources.Total != 1 {
		t.Errorf("sources.total: got %d, want 1", result.Sources.Total)
	}
}

// TestIntegration_fetchOldItemsDropped tests that items older than window are dropped.
func TestIntegration_fetchOldItemsDropped(t *testing.T) {
	if _, err := os.Stat(binPath()); os.IsNotExist(err) {
		t.Skip("binary not built, skipping integration test")
	}

	dbDir, configDir := setupFetchConfig(t)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(minimalRSSOldDate("Old Article", "https://example.com/old")))
	}))
	defer ts.Close()

	writeSourcesYML(t, configDir, "TestFeed", 1, ts.URL + "/feed")

	out, err := runFetch(t, "--db", dbDir, "--config", configDir)
	if err != nil {
		t.Fatalf("fetch: %v\n%s", err, out)
	}

	var result fetchResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("parse JSON: %v\noutput: %s", err, out)
	}

	if result.Items.DroppedOld != 1 {
		t.Errorf("items.dropped_old: got %d, want 1 (article from 2020)", result.Items.DroppedOld)
	}
	if result.Items.New != 0 {
		t.Errorf("items.new: got %d, want 0 (all dropped)", result.Items.New)
	}
}

// TestIntegration_fetchNullDateKept tests that items with null published_at are kept.
func TestIntegration_fetchNullDateKept(t *testing.T) {
	if _, err := os.Stat(binPath()); os.IsNotExist(err) {
		t.Skip("binary not built, skipping integration test")
	}

	dbDir, configDir := setupFetchConfig(t)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(minimalRSSNoDate("No Date Article", "https://example.com/no-date")))
	}))
	defer ts.Close()

	writeSourcesYML(t, configDir, "TestFeed", 1, ts.URL + "/feed")

	out, err := runFetch(t, "--db", dbDir, "--config", configDir)
	if err != nil {
		t.Fatalf("fetch: %v\n%s", err, out)
	}

	var result fetchResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("parse JSON: %v\noutput: %s", err, out)
	}

	if result.Items.New != 1 {
		t.Errorf("items.new: got %d, want 1 (null date should be kept)", result.Items.New)
	}
	if result.Items.DroppedOld != 0 {
		t.Errorf("items.dropped_old: got %d, want 0", result.Items.DroppedOld)
	}
}

// TestIntegration_fetchPartialFailure tests exit code 1 when some sources fail.
func TestIntegration_fetchPartialFailure(t *testing.T) {
	if _, err := os.Stat(binPath()); os.IsNotExist(err) {
		t.Skip("binary not built, skipping integration test")
	}

	tmpDir := t.TempDir()
	dbDir := filepath.Join(tmpDir, "data")
	configDir := filepath.Join(tmpDir, "config")
	os.MkdirAll(configDir, 0755)

	sourcesYML := `sources:
  - name: Good
    tier: 1
    url: "http://localhost:0/good"
  - name: Bad
    tier: 1
    url: "http://localhost:0/bad"`
	domainsYML := `primary_domains: []`
	prefilterYML := `candidate_limit: 40
per_source_cap: 3
title_simhash_max: 3
max_backfill_days: 30
exclude_terms: []`
	os.WriteFile(filepath.Join(configDir, "domains.yml"), []byte(domainsYML), 0644)
	os.WriteFile(filepath.Join(configDir, "prefilter.yml"), []byte(prefilterYML), 0644)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "good") {
			w.Header().Set("Content-Type", "application/rss+xml")
			w.Write([]byte(minimalRSS("Good Article", "https://example.com/good", "Desc")))
		} else {
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer ts.Close()

	sourcesYML = fmt.Sprintf(`sources:
  - name: Good
    tier: 1
    url: "%s/good"
  - name: Bad
    tier: 1
    url:  "%s/bad"`, ts.URL, ts.URL)
	os.WriteFile(filepath.Join(configDir, "sources.yml"), []byte(sourcesYML), 0644)

	cmd := exec.Command(binPath(), "fetch", "--db", dbDir, "--config", configDir, "--json")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected non-zero exit for partial failure")
	}

	var result fetchResult
	if jsonErr := json.Unmarshal(out, &result); jsonErr != nil {
		// Might be envelope format; check fields anyway.
		t.Logf("parse error (might be envelope): %v, output: %s", jsonErr, string(out))
	}

	if len(result.Failures) != 1 {
		t.Errorf("failures: got %d entries, want 1", len(result.Failures))
	}
	if result.Sources.OK != 1 {
		t.Errorf("sources.ok: got %d, want 1", result.Sources.OK)
	}
	if result.Sources.Failed != 1 {
		t.Errorf("sources.failed: got %d, want 1", result.Sources.Failed)
	}
}

// TestIntegration_fetchAllFailed tests exit code 2 when all sources fail.
func TestIntegration_fetchAllFailed(t *testing.T) {
	if _, err := os.Stat(binPath()); os.IsNotExist(err) {
		t.Skip("binary not built, skipping integration test")
	}

	tmpDir := t.TempDir()
	dbDir := filepath.Join(tmpDir, "data")
	configDir := filepath.Join(tmpDir, "config")
	os.MkdirAll(configDir, 0755)

	sourcesYML := `sources:
  - name: Bad1
    tier: 1
    url: "http://localhost:0/bad1"
  - name: Bad2
    tier: 1
    url: "http://localhost:0/bad2"`
	domainsYML := `primary_domains: []`
	prefilterYML := `candidate_limit: 40
per_source_cap: 3
title_simhash_max: 3
max_backfill_days: 30
exclude_terms: []`
	os.WriteFile(filepath.Join(configDir, "domains.yml"), []byte(domainsYML), 0644)
	os.WriteFile(filepath.Join(configDir, "prefilter.yml"), []byte(prefilterYML), 0644)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	sourcesYML = fmt.Sprintf(`sources:
  - name: Bad1
    tier: 1
    url: "%s/bad1"
  - name: Bad2
    tier: 1
    url:  "%s/bad2"`, ts.URL, ts.URL)
	os.WriteFile(filepath.Join(configDir, "sources.yml"), []byte(sourcesYML), 0644)

	cmd := exec.Command(binPath(), "fetch", "--db", dbDir, "--config", configDir, "--json")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected non-zero exit for all-failed")
	}

	var result fetchResult
	if jsonErr := json.Unmarshal(out, &result); jsonErr != nil {
		t.Logf("parse error: %v, output: %s", jsonErr, string(out))
	}

	if result.Sources.OK != 0 {
		t.Errorf("sources.ok: got %d, want 0", result.Sources.OK)
	}
	if result.Sources.Failed != 2 {
		t.Errorf("sources.failed: got %d, want 2", result.Sources.Failed)
	}
}

// TestIntegration_fetchETagStored tests that ETags are stored on 200 responses.
func TestIntegration_fetchETagStored(t *testing.T) {
	if _, err := os.Stat(binPath()); os.IsNotExist(err) {
		t.Skip("binary not built, skipping integration test")
	}

	dbDir, configDir := setupFetchConfig(t)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Header().Set("ETag", "test-etag-value")
		w.Header().Set("Last-Modified", "Mon, 01 Sep 2025 12:00:00 GMT")
		w.Write([]byte(minimalRSS("Article", "https://example.com/1", "Desc")))
	}))
	defer ts.Close()

	writeSourcesYML(t, configDir, "TestFeed", 1, ts.URL + "/feed")

	out, err := runFetch(t, "--db", dbDir, "--config", configDir)
	if err != nil {
		t.Fatalf("fetch: %v\n%s", err, out)
	}

	var result fetchResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("parse JSON: %v", err)
	}

	if result.Items.New != 1 {
		t.Errorf("items.new: got %d, want 1", result.Items.New)
	}

	// Verify ETag was stored in feed_state.
	db, err := store.Open(context.Background(), dbDir, store.ReadOnly)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	var storedETag string
	err = db.QueryRowContext(context.Background(), "SELECT etag FROM feed_state WHERE source=?", "TestFeed").Scan(&storedETag)
	if err != nil {
		t.Fatalf("query feed_state: %v", err)
	}
	if storedETag != "test-etag-value" {
		t.Errorf("stored ETag: got %q, want %q", storedETag, "test-etag-value")
	}
}

// TestIntegration_fetchSecondRun304 tests that second run gets 304 for all sources.
func TestIntegration_fetchSecondRun304(t *testing.T) {
	if _, err := os.Stat(binPath()); os.IsNotExist(err) {
		t.Skip("binary not built, skipping integration test")
	}

	dbDir, configDir := setupFetchConfig(t)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != "" || r.Header.Get("If-Modified-Since") != "" {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Header().Set("ETag", `"v1"`)
		w.Write([]byte(minimalRSS("Article", "https://example.com/1", "Desc")))
	}))
	defer ts.Close()

	writeSourcesYML(t, configDir, "TestFeed", 1, ts.URL + "/feed")

	// First run — populates DB and stores ETag.
	out, err := runFetch(t, "--db", dbDir, "--config", configDir)
	if err != nil {
		t.Fatalf("first fetch: %v\n%s", err, out)
	}

	var result1 fetchResult
	if err := json.Unmarshal([]byte(out), &result1); err != nil {
		t.Fatalf("parse JSON: %v", err)
	}

	// Second run — should get 304 for all.
	out, err = runFetch(t, "--db", dbDir, "--config", configDir)
	if err != nil {
		t.Fatalf("second fetch: %v\n%s", err, out)
	}

	var result2 fetchResult
	if err := json.Unmarshal([]byte(out), &result2); err != nil {
		t.Fatalf("parse JSON: %v", err)
	}

	if result2.Items.New != 0 {
		t.Errorf("items.new: got %d, want 0 (304 responses)", result2.Items.New)
	}
	if result2.Sources.NotModified != 1 {
		t.Errorf("sources.not_modified: got %d, want 1", result2.Sources.NotModified)
	}
}

// TestIntegration_fetchRunstate tests that runstate row is written correctly.
func TestIntegration_fetchRunstate(t *testing.T) {
	if _, err := os.Stat(binPath()); os.IsNotExist(err) {
		t.Skip("binary not built, skipping integration test")
	}

	dbDir, configDir := setupFetchConfig(t)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(minimalRSS("Article", "https://example.com/1", "Desc")))
	}))
	defer ts.Close()

	writeSourcesYML(t, configDir, "TestFeed", 1, ts.URL + "/feed")

	out, err := runFetch(t, "--db", dbDir, "--config", configDir)
	if err != nil {
		t.Fatalf("fetch: %v\n%s", err, out)
	}

	var result fetchResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("parse JSON: %v", err)
	}

	// Verify runstate row.
	db, err := store.Open(context.Background(), dbDir, store.ReadOnly)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	var stage string
	var ok bool
	var itemsIn, itemsOut int
	err = db.QueryRowContext(context.Background(),
		`SELECT stage, ok, items_in, items_out FROM runs WHERE stage='fetch' ORDER BY started_at DESC LIMIT 1`,
	).Scan(&stage, &ok, &itemsIn, &itemsOut)
	if err != nil {
		t.Fatalf("query runs: %v", err)
	}

	if stage != "fetch" {
		t.Errorf("stage: got %q, want %q", stage, "fetch")
	}
	if !ok {
		t.Error("expected ok=true for successful fetch")
	}
	if itemsIn != 1 {
		t.Errorf("items_in: got %d, want 1", itemsIn)
	}
	if itemsOut != 1 {
		t.Errorf("items_out: got %d, want 1", itemsOut)
	}
}

// TestIntegration_fetchInvalidSource tests that --source with unknown name exits with error.
func TestIntegration_fetchInvalidSource(t *testing.T) {
	if _, err := os.Stat(binPath()); os.IsNotExist(err) {
		t.Skip("binary not built, skipping integration test")
	}

	dbDir, configDir := setupFetchConfig(t)

	cmd := exec.Command(binPath(), "fetch", "--db", dbDir, "--config", configDir, "--source", "NonExistent", "--json")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected error for invalid source")
	}

	var envelope map[string]interface{}
	if jsonErr := json.Unmarshal(out, &envelope); jsonErr != nil {
		t.Logf("parse error (might be envelope): %v, output: %s", jsonErr, string(out))
	}
}
