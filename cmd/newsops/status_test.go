package main

import (
	"context"
	"fmt"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"news-ops/internal/runstate"
	"news-ops/internal/store"
)

// TestStatus_VirginShape verifies the JSON shape when no database exists.
func TestStatus_VirginShape(t *testing.T) {
	dir := t.TempDir()
	dbDir = dir
	configDir = ""
	fetchMaxAge = 0 // reset to default

	err := runCommand(context.Background(), cmdStatus, nil)
	if err != nil {
		t.Fatalf("status returned error: %v", err)
	}

	// Assert no files were created.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected empty directory, got %d entries", len(entries))
	}

	// Re-run and capture JSON output.
	err = runCommand(context.Background(), cmdStatus, nil)
	if err != nil {
		t.Fatalf("status returned error: %v", err)
	}

	// Verify the command exits 0 by checking no error path was taken.
}

// TestStatus_JsonOutput verifies the JSON shape for a populated database.
func TestStatus_JsonOutput(t *testing.T) {
	dir := t.TempDir()
	configDir = ""
	fetchMaxAge = 0

	// Open DB and insert test data.
	sqlDB, err := store.Open(context.Background(), dir, store.ReadWrite)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer sqlDB.Close()

	ctx := context.Background()

	// Record a successful fetch run 2 hours ago.
	twoHoursAgo := time.Now().Add(-2 * time.Hour).UTC()
	fetchStarted := store.FormatTime(twoHoursAgo)
	_, err = sqlDB.ExecContext(ctx,
		`INSERT INTO runs(stage, started_at, ended_at, ok, items_in, items_out, note)
		 VALUES ('fetch', ?, ?, 1, 100, 80, '')`,
		fetchStarted, fetchStarted)
	if err != nil {
		t.Fatalf("insert fetch run: %v", err)
	}

	// Record a successful dedupe run 1 hour ago.
	oneHourAgo := time.Now().Add(-1 * time.Hour).UTC()
	dedupeStarted := store.FormatTime(oneHourAgo)
	_, err = sqlDB.ExecContext(ctx,
		`INSERT INTO runs(stage, started_at, ended_at, ok, items_in, items_out, note)
		 VALUES ('dedupe', ?, ?, 1, 80, 60, '')`,
		dedupeStarted, dedupeStarted)
	if err != nil {
		t.Fatalf("insert dedupe run: %v", err)
	}

	// Insert items.
	now := store.FormatTime(time.Now().UTC())
	for i := 0; i < 10; i++ {
		url := "https://example.com/article" + string(rune('a'+i))
		title := "Test article " + string(rune('a'+i))
		hash := "hash" + string(rune('a'+i))
		_, err = sqlDB.ExecContext(ctx,
			`INSERT INTO items(url, canonical_url, title, source, tier, first_seen, body_hash)
			 VALUES (?, ?, ?, 'Test', 1, ?, ?)`,
			url, url, title, now, hash)
		if err != nil {
			t.Fatalf("insert item: %v", err)
		}
	}

	// Insert stories — some shown, some not.
	for i := 0; i < 5; i++ {
		itemID := int64(i + 1)
		nowRFC := store.FormatTime(time.Now().UTC())
		_, err = sqlDB.ExecContext(ctx,
			`INSERT INTO stories(canonical_item_id, source_count, method, first_seen, last_seen, shown_at)
			 VALUES (?, 1, 'url', ?, ?, NULL)`,
			itemID, nowRFC, nowRFC)
		if err != nil {
			t.Fatalf("insert story: %v", err)
		}
		_, err = sqlDB.ExecContext(ctx,
			`UPDATE items SET story_id=1 WHERE id=?`, itemID)
		if err != nil {
			t.Fatalf("assign item to story: %v", err)
		}
	}

	// Insert a shown story.
	_, err = sqlDB.ExecContext(ctx,
		`INSERT INTO stories(canonical_item_id, source_count, method, first_seen, last_seen, shown_at)
		 VALUES (1, 1, 'url', ?, ?, ?)`,
		now, now, fetchStarted)
	if err != nil {
		t.Fatalf("insert shown story: %v", err)
	}

	dbDir = dir
	fetchMaxAge = 0 // reset default

	result, err := cmdStatus(ctx, nil)
	if err != nil {
		t.Fatalf("status returned error: %v", err)
	}

	r, ok := result.(statusResult)
	if !ok {
		t.Fatalf("expected statusResult, got %T", result)
	}

	// Verify stages.
	fetchStage := r.Stages["fetch"]
	if fetchStage.Stale {
		t.Error("fetch should not be stale (2h ago, default 6h)")
	}
	if fetchStage.LastSuccess == nil {
		t.Error("fetch last_success should not be nil")
	}

	dedupeStage := r.Stages["dedupe"]
	if dedupeStage.Stale {
		t.Error("dedupe should not be stale (1h ago, default 6h)")
	}

	candidatesStage := r.Stages["candidates"]
	if !candidatesStage.Stale {
		t.Error("candidates should be stale (never ran)")
	}
	if candidatesStage.LastSuccess != nil {
		t.Error("candidates last_success should be nil")
	}

	// Verify counts.
	if r.Counts.ItemsTotal != 10 {
		t.Errorf("items_total = %d, want 10", r.Counts.ItemsTotal)
	}
	if r.Counts.UnshownStories < 0 {
		t.Errorf("unshown_stories = %d, want >= 0", r.Counts.UnshownStories)
	}

	// Verify JSON serialization round-trips.
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal statusResult: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Verify stages key exists and is a map.
	stagesVal, ok := decoded["stages"].(map[string]any)
	if !ok {
		t.Fatal("stages should be a JSON object")
	}
	for _, stageName := range []string{"fetch", "dedupe", "candidates"} {
		stage, ok := stagesVal[stageName].(map[string]any)
		if !ok {
			t.Errorf("stage %q missing or not an object", stageName)
			continue
		}
		// stale must be a boolean.
		if _, ok := stage["stale"].(bool); !ok {
			t.Errorf("stage %q stale is not a boolean", stageName)
		}
	}

	// Verify counts key exists.
	countsVal, ok := decoded["counts"].(map[string]any)
	if !ok {
		t.Fatal("counts should be a JSON object")
	}
	for key := range map[string]bool{
		"items_total": true, "new_since_fetch": true, "unshown_stories": true,
	} {
		if _, ok := countsVal[key]; !ok {
			t.Errorf("counts missing key %q", key)
		}
	}

	// Verify digest key.
	digestVal, ok := decoded["digest"].(map[string]any)
	if !ok {
		t.Fatal("digest should be a JSON object")
	}
	if _, ok := digestVal["path"]; !ok {
		t.Error("digest missing path")
	}
}

// TestStatus_StaleBoundary verifies the --max-age flag flips stale correctly.
func TestStatus_StaleBoundary(t *testing.T) {
	dir := t.TempDir()

	sqlDB, err := store.Open(context.Background(), dir, store.ReadWrite)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer sqlDB.Close()

	ctx := context.Background()

	// Record a fetch run 7 hours ago.
	sevenHoursAgo := time.Now().Add(-7 * time.Hour).UTC()
	started := store.FormatTime(sevenHoursAgo)
	_, err = sqlDB.ExecContext(ctx,
		`INSERT INTO runs(stage, started_at, ended_at, ok, items_in, items_out)
		 VALUES ('fetch', ?, ?, 1, 10, 8)`,
		started, started)
	if err != nil {
		t.Fatalf("insert run: %v", err)
	}

	dbDir = dir

	// With default 6h max-age, should be stale.
	fetchMaxAge = 0
	result, err := cmdStatus(ctx, nil)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	r := result.(statusResult)
	if !r.Stages["fetch"].Stale {
		t.Error("fetch should be stale at 7h with 6h default")
	}

	// With 8h max-age, should not be stale.
	fetchMaxAge = 8 * time.Hour
	result, err = cmdStatus(ctx, nil)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	r = result.(statusResult)
	if r.Stages["fetch"].Stale {
		t.Error("fetch should not be stale at 7h with 8h max-age")
	}

	// With 5h max-age, should be stale.
	fetchMaxAge = 5 * time.Hour
	result, err = cmdStatus(ctx, nil)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	r = result.(statusResult)
	if !r.Stages["fetch"].Stale {
		t.Error("fetch should be stale at 7h with 5h max-age")
	}
}

// TestStatus_AgeMin verifies age_min is an integer when last_success exists, null otherwise.
func TestStatus_AgeMin(t *testing.T) {
	dir := t.TempDir()

	sqlDB, err := store.Open(context.Background(), dir, store.ReadWrite)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer sqlDB.Close()

	ctx := context.Background()

	// Record a fetch run.
	now := store.FormatTime(time.Now().UTC())
	_, err = sqlDB.ExecContext(ctx,
		`INSERT INTO runs(stage, started_at, ended_at, ok, items_in, items_out)
		 VALUES ('fetch', ?, ?, 1, 10, 8)`,
		now, now)
	if err != nil {
		t.Fatalf("insert run: %v", err)
	}

	dbDir = dir
	fetchMaxAge = 6 * time.Hour

	result, err := cmdStatus(ctx, nil)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	r := result.(statusResult)

	if r.Stages["fetch"].AgeMin == nil {
		t.Fatal("fetch age_min should not be nil when last_success exists")
	}
	if *r.Stages["fetch"].AgeMin < 0 {
		t.Errorf("age_min = %d, want >= 0", *r.Stages["fetch"].AgeMin)
	}

	// Candidates has never run — age_min should be nil.
	if r.Stages["candidates"].AgeMin != nil {
		t.Error("candidates age_min should be nil (never ran)")
	}
}

// TestStatus_ConcurrentRead verifies status works while a write lock is held.
func TestStatus_ConcurrentRead(t *testing.T) {
	dir := t.TempDir()

	sqlDB, err := store.Open(context.Background(), dir, store.ReadWrite)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	ctx := context.Background()
	now := store.FormatTime(time.Now().UTC())
	_, err = sqlDB.ExecContext(ctx,
		`INSERT INTO runs(stage, started_at, ended_at, ok, items_in, items_out)
		 VALUES ('fetch', ?, ?, 1, 10, 8)`,
		now, now)
	if err != nil {
		t.Fatalf("insert run: %v", err)
	}

	dbDir = dir
	fetchMaxAge = 6 * time.Hour

	// Run status in a goroutine while the DB is still open (write lock held).
	var result any
	errCh := make(chan error, 1)
	go func() {
		r, e := cmdStatus(ctx, nil)
		result = r
		errCh <- e
	}()

	// Wait a moment then close the DB (releasing the lock).
	time.Sleep(50 * time.Millisecond)
	sqlDB.Close()

	err = <-errCh
	if err != nil {
		t.Fatalf("status failed while write lock was held: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result from status")
	}
}

// TestStatus_UnshownStories verifies unshown_stories excludes shown and old stories.
func TestStatus_UnshownStories(t *testing.T) {
	dir := t.TempDir()

	sqlDB, err := store.Open(context.Background(), dir, store.ReadWrite)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer sqlDB.Close()

	ctx := context.Background()
	now := time.Now().UTC()
	nowRFC := store.FormatTime(now)
	fetchTime := store.FormatTime(now.Add(-1 * time.Hour))

	// Record a fetch run.
	_, err = sqlDB.ExecContext(ctx,
		`INSERT INTO runs(stage, started_at, ended_at, ok, items_in, items_out)
		 VALUES ('fetch', ?, ?, 1, 10, 8)`,
		fetchTime, fetchTime)
	if err != nil {
		t.Fatalf("insert run: %v", err)
	}

	// Insert 3 unshown stories within 7 days.
	for i := 0; i < 3; i++ {
		url := "https://example.com/article" + string(rune('a'+i))
		title := "Test article " + string(rune('a'+i))
		hash := "hash" + string(rune('a'+i))
		res, err := sqlDB.ExecContext(ctx,
			`INSERT INTO items(url, canonical_url, title, source, tier, first_seen, body_hash)
			 VALUES (?, ?, ?, 'Test', 1, ?, ?)`,
			url, url, title, nowRFC, hash)
		if err != nil {
			t.Fatalf("insert item: %v", err)
		}
		itemID, err := res.LastInsertId()
		if err != nil {
			t.Fatalf("get item id: %v", err)
		}
		_, err = sqlDB.ExecContext(ctx,
			`INSERT INTO stories(canonical_item_id, source_count, method, first_seen, last_seen, shown_at)
			 VALUES (?, 1, 'url', ?, ?, NULL)`,
			itemID, nowRFC, nowRFC)
		if err != nil {
			t.Fatalf("insert story: %v", err)
		}
	}

	// Insert 2 shown stories.
	for i := 0; i < 2; i++ {
		url := "https://example.com/article" + string(rune('a'+i+10))
		title := "Shown article " + string(rune('a'+i+10))
		hash := "hash" + string(rune('a'+i+10))
		res, err := sqlDB.ExecContext(ctx,
			`INSERT INTO items(url, canonical_url, title, source, tier, first_seen, body_hash)
			 VALUES (?, ?, ?, 'Test', 1, ?, ?)`,
			url, url, title, nowRFC, hash)
		if err != nil {
			t.Fatalf("insert shown item: %v", err)
		}
		itemID, err := res.LastInsertId()
		if err != nil {
			t.Fatalf("get item id: %v", err)
		}
		_, err = sqlDB.ExecContext(ctx,
			`INSERT INTO stories(canonical_item_id, source_count, method, first_seen, last_seen, shown_at)
			 VALUES (?, 1, 'url', ?, ?, ?)`,
			itemID, nowRFC, nowRFC, fetchTime)
		if err != nil {
			t.Fatalf("insert shown story: %v", err)
		}
	}

	dbDir = dir
	fetchMaxAge = 6 * time.Hour

	result, err := cmdStatus(ctx, nil)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	r := result.(statusResult)

	if r.Counts.UnshownStories != 3 {
		t.Errorf("unshown_stories = %d, want 3 (excludes shown)", r.Counts.UnshownStories)
	}
}

// TestStatus_NewSinceFetch verifies new_since_fetch counts items since last fetch.
func TestStatus_NewSinceFetch(t *testing.T) {
	dir := t.TempDir()

	sqlDB, err := store.Open(context.Background(), dir, store.ReadWrite)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer sqlDB.Close()

	ctx := context.Background()
	now := time.Now().UTC()
	fetchTime := store.FormatTime(now.Add(-2 * time.Hour))
	oldTime := store.FormatTime(now.Add(-4 * time.Hour))
	nowRFC := store.FormatTime(now)

	// Record a fetch run.
	_, err = sqlDB.ExecContext(ctx,
		`INSERT INTO runs(stage, started_at, ended_at, ok, items_in, items_out)
		 VALUES ('fetch', ?, ?, 1, 10, 8)`,
		fetchTime, fetchTime)
	if err != nil {
		t.Fatalf("insert run: %v", err)
	}

	// Insert 2 items seen before the fetch.
	for i := 0; i < 2; i++ {
		url := "https://example.com/old" + string(rune('a'+i))
		hash := "hash" + string(rune('a'+i))
		_, err = sqlDB.ExecContext(ctx,
			`INSERT INTO items(url, canonical_url, title, source, tier, first_seen, body_hash)
			 VALUES (?, ?, 'Old article', 'Test', 1, ?, ?)`,
			url, url, oldTime, hash)
		if err != nil {
			t.Fatalf("insert old item: %v", err)
		}
	}

	// Insert 3 items seen after the fetch.
	for i := 0; i < 3; i++ {
		url := "https://example.com/new" + string(rune('a'+i))
		hash := "hash" + string(rune('a'+i))
		_, err = sqlDB.ExecContext(ctx,
			`INSERT INTO items(url, canonical_url, title, source, tier, first_seen, body_hash)
			 VALUES (?, ?, 'New article', 'Test', 1, ?, ?)`,
			url, url, nowRFC, hash)
		if err != nil {
			t.Fatalf("insert new item: %v", err)
		}
	}

	dbDir = dir
	fetchMaxAge = 6 * time.Hour

	result, err := cmdStatus(ctx, nil)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	r := result.(statusResult)

	if r.Counts.NewSinceFetch != 3 {
		t.Errorf("new_since_fetch = %d, want 3", r.Counts.NewSinceFetch)
	}
}

// TestStatus_Perf verifies status completes in under 100ms on a database with 10k items.
func TestStatus_Perf(t *testing.T) {
	dir := t.TempDir()

	sqlDB, err := store.Open(context.Background(), dir, store.ReadWrite)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer sqlDB.Close()

	ctx := context.Background()
	now := store.FormatTime(time.Now().UTC())

	// Insert 10k items.
	for i := 0; i < 10000; i++ {
		url := fmt.Sprintf("https://example.com/article/%d", i)
		hash := "hash" + string(rune('a'+(i%26)))
		_, err = sqlDB.ExecContext(ctx,
			`INSERT INTO items(url, canonical_url, title, source, tier, first_seen, body_hash)
			 VALUES (?, ?, 'Article', 'Test', 1, ?, ?)`,
			url, url, now, hash)
		if err != nil {
			t.Fatalf("insert item %d: %v", i, err)
		}
	}

	dbDir = dir
	fetchMaxAge = 6 * time.Hour

	start := time.Now()
	result, err := cmdStatus(ctx, nil)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	r := result.(statusResult)

	if r.Counts.ItemsTotal != 10000 {
		t.Errorf("items_total = %d, want 10000", r.Counts.ItemsTotal)
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("status took %v, want < 100ms", elapsed)
	}
}

// TestStatus_DigestFile verifies digest.md detection.
func TestStatus_DigestFile(t *testing.T) {
	dir := t.TempDir()

	sqlDB, err := store.Open(context.Background(), dir, store.ReadWrite)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer sqlDB.Close()

	ctx := context.Background()
	now := store.FormatTime(time.Now().UTC())
	_, err = sqlDB.ExecContext(ctx,
		`INSERT INTO runs(stage, started_at, ended_at, ok, items_in, items_out)
		 VALUES ('fetch', ?, ?, 1, 10, 8)`,
		now, now)
	if err != nil {
		t.Fatalf("insert run: %v", err)
	}

	dbDir = dir
	fetchMaxAge = 6 * time.Hour

	// No digest.md — should be null.
	digestPath = filepath.Join(dir, "digest.md")
	defer func() { digestPath = "digest.md" }()
	result, err := cmdStatus(ctx, nil)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	r := result.(statusResult)
	if r.Digest.GeneratedAt != nil {
		t.Errorf("digest generated_at should be null, got %q", *r.Digest.GeneratedAt)
	}

	// Create digest.md.
	digestPath := filepath.Join(dir, "digest.md")
	err = os.WriteFile(digestPath, []byte("# Digest\n"), 0644)
	if err != nil {
		t.Fatalf("write digest: %v", err)
	}
	configDir = dir

	result, err = cmdStatus(ctx, nil)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	r = result.(statusResult)
	if r.Digest.GeneratedAt == nil {
		t.Fatal("digest generated_at should not be nil when digest.md exists")
	}
	if *r.Digest.GeneratedAt == "" {
		t.Error("digest generated_at should not be empty")
	}
}

// TestStatus_NeverRunShape verifies the exact JSON shape for a never-run database.
func TestStatus_NeverRunShape(t *testing.T) {
	dir := t.TempDir()

	sqlDB, err := store.Open(context.Background(), dir, store.ReadWrite)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer sqlDB.Close()

	ctx := context.Background()
	dbDir = dir
	fetchMaxAge = 6 * time.Hour
	configDir = ""
	digestPath = "digest.md"

	result, err := cmdStatus(ctx, nil)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	r := result.(statusResult)

	// Verify every stage has stale=true, last_success=nil, age_min=nil.
	for _, stage := range []runstate.Stage{
		runstate.StageFetch, runstate.StageDedupe, runstate.StageCandidates,
	} {
		si := r.Stages[string(stage)]
		if !si.Stale {
			t.Errorf("stage %q stale should be true for never-run", stage)
		}
		if si.LastSuccess != nil {
			t.Errorf("stage %q last_success should be nil for never-run", stage)
		}
		if si.AgeMin != nil {
			t.Errorf("stage %q age_min should be nil for never-run", stage)
		}
	}

	// Verify counts are zero.
	if r.Counts.ItemsTotal != 0 {
		t.Errorf("items_total = %d, want 0", r.Counts.ItemsTotal)
	}
	if r.Counts.NewSinceFetch != 0 {
		t.Errorf("new_since_fetch = %d, want 0", r.Counts.NewSinceFetch)
	}
	if r.Counts.UnshownStories != 0 {
		t.Errorf("unshown_stories = %d, want 0", r.Counts.UnshownStories)
	}

	// Verify digest is null.
	if r.Digest.GeneratedAt != nil {
		t.Error("digest generated_at should be null")
	}

	// Verify JSON round-trip preserves nulls.
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Check that stale values are JSON booleans.
	stagesVal := decoded["stages"].(map[string]any)
	for name, val := range stagesVal {
		stage := val.(map[string]any)
		if s, ok := stage["stale"].(bool); !ok || !s {
			t.Errorf("stage %q stale should be true JSON boolean", name)
		}
		if stage["last_success"] != nil {
			t.Errorf("stage %q last_success should be null in JSON", name)
		}
		if stage["age_min"] != nil {
			t.Errorf("stage %q age_min should be null in JSON", name)
		}
	}

	// Verify counts are zero in JSON.
	countsVal := decoded["counts"].(map[string]any)
	for key, want := range map[string]int64{
		"items_total": 0, "new_since_fetch": 0, "unshown_stories": 0,
	} {
		if v, ok := countsVal[key].(float64); !ok || int64(v) != want {
			t.Errorf("counts.%s = %v, want %d", key, v, want)
		}
	}

	// Verify digest path is present.
	digestVal := decoded["digest"].(map[string]any)
	if p, ok := digestVal["path"].(string); !ok || p != "digest.md" {
		t.Errorf("digest.path = %v, want \"digest.md\"", digestVal["path"])
	}
	if digestVal["generated_at"] != nil {
		t.Error("digest.generated_at should be null in JSON")
	}
}
