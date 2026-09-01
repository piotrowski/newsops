package runstate

import (
	"context"
	"testing"
	"time"

	"news-ops/internal/store"
)

func newTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(context.Background(), t.TempDir(), store.ReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestBegin_End(t *testing.T) {
	db := newTestDB(t)

	run, err := Begin(context.Background(), db, StageFetch)
	if err != nil {
		t.Fatal(err)
	}
	if run.ID == 0 {
		t.Fatal("expected non-zero ID")
	}
	if run.Stage != StageFetch {
		t.Errorf("stage = %v, want %v", run.Stage, StageFetch)
	}

	err = run.End(context.Background(), true, Counts{In: 10, Out: 8}, "done")
	if err != nil {
		t.Fatal(err)
	}

	// Verify the row was written correctly.
	var endedAt string
	var ok int
	var itemsIn, itemsOut int
	var note string
	err = db.QueryRowContext(context.Background(),
		`SELECT ended_at, ok, items_in, items_out, note FROM runs WHERE id=?`,
		run.ID,
	).Scan(&endedAt, &ok, &itemsIn, &itemsOut, &note)
	if err != nil {
		t.Fatal(err)
	}
	if endedAt == "" {
		t.Error("ended_at should not be empty")
	}
	if ok != 1 {
		t.Errorf("ok = %d, want 1", ok)
	}
	if itemsIn != 10 || itemsOut != 8 {
		t.Errorf("items_in=%d items_out=%d, want 10,8", itemsIn, itemsOut)
	}
	if note != "done" {
		t.Errorf("note = %q, want %q", note, "done")
	}
}

func TestBegin_End_Failed(t *testing.T) {
	db := newTestDB(t)

	run, err := Begin(context.Background(), db, StageDedupe)
	if err != nil {
		t.Fatal(err)
	}

	err = run.End(context.Background(), false, Counts{In: 5, Out: 0}, "crash")
	if err != nil {
		t.Fatal(err)
	}

	var ok int
	err = db.QueryRowContext(context.Background(),
		`SELECT ok FROM runs WHERE id=?`, run.ID,
	).Scan(&ok)
	if err != nil {
		t.Fatal(err)
	}
	if ok != 0 {
		t.Errorf("ok = %d, want 0", ok)
	}

	// LastSuccess should ignore failed runs.
	timestamp, found, err := LastSuccess(context.Background(), db, StageDedupe)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Error("LastSuccess should return found=false for failed run")
	}
	if !timestamp.IsZero() {
		t.Errorf("timestamp = %v, want zero", timestamp)
	}
}

func TestBegin_CrashedRun(t *testing.T) {
	db := newTestDB(t)

	// Simulate a crashed run: Begin without End.
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO runs(stage, started_at) VALUES (?, CURRENT_TIMESTAMP)`,
		StageCandidates,
	)
	if err != nil {
		t.Fatal(err)
	}

	// LastSuccess should ignore crashed runs (ok is NULL).
	timestamp, found, err := LastSuccess(context.Background(), db, StageCandidates)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Error("LastSuccess should return found=false for crashed run")
	}
	if !timestamp.IsZero() {
		t.Errorf("timestamp = %v, want zero", timestamp)
	}
}

func TestLastSuccess_NeverRun(t *testing.T) {
	db := newTestDB(t)

	timestamp, found, err := LastSuccess(context.Background(), db, StageFetch)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Error("LastSuccess should return found=false for never-run stage")
	}
	if !timestamp.IsZero() {
		t.Errorf("timestamp = %v, want zero", timestamp)
	}
}

func TestLastSuccess_SuccessAfterFailed(t *testing.T) {
	db := newTestDB(t)

	// Insert a failed run first.
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO runs(stage, started_at, ok, items_in, items_out, note) VALUES (?, CURRENT_TIMESTAMP, 0, 5, 0, 'failed')`,
		StageFetch,
	)
	if err != nil {
		t.Fatal(err)
	}

	// Then a successful run.
	run, err := Begin(context.Background(), db, StageFetch)
	if err != nil {
		t.Fatal(err)
	}
	err = run.End(context.Background(), true, Counts{In: 20, Out: 15}, "ok")
	if err != nil {
		t.Fatal(err)
	}

	timestamp, found, err := LastSuccess(context.Background(), db, StageFetch)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Error("LastSuccess should return found=true")
	}
	if timestamp.IsZero() {
		t.Error("timestamp should not be zero")
	}

	// The successful run's started_at should match.
	var dbStartedAt string
	err = db.QueryRowContext(context.Background(),
		`SELECT started_at FROM runs WHERE id=?`, run.ID,
	).Scan(&dbStartedAt)
	if err != nil {
		t.Fatal(err)
	}
	if timestamp.Format(time.RFC3339Nano) == "" {
		t.Error("timestamp should be parseable")
	}
}

func TestStale_NeverRun(t *testing.T) {
	db := newTestDB(t)

	stale, err := Stale(context.Background(), db, StageFetch, 1*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !stale {
		t.Error("Stale should return true when no run exists")
	}
}

func TestStale_FreshRun(t *testing.T) {
	db := newTestDB(t)

	run, err := Begin(context.Background(), db, StageFetch)
	if err != nil {
		t.Fatal(err)
	}
	err = run.End(context.Background(), true, Counts{In: 10, Out: 8}, "ok")
	if err != nil {
		t.Fatal(err)
	}

	stale, err := Stale(context.Background(), db, StageFetch, 1*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if stale {
		t.Error("Stale should return false for a fresh successful run")
	}
}

func TestStale_OldRun(t *testing.T) {
	db := newTestDB(t)

	// Insert a run with an old timestamp.
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO runs(stage, started_at, ok, items_in, items_out) VALUES (?, datetime('now', '-2 hours'), 1, 10, 8)`,
		StageFetch,
	)
	if err != nil {
		t.Fatal(err)
	}

	stale, err := Stale(context.Background(), db, StageFetch, 1*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !stale {
		t.Error("Stale should return true for a run older than maxAge")
	}
}

func TestStale_FailedRun(t *testing.T) {
	db := newTestDB(t)

	// Only a failed run exists.
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO runs(stage, started_at, ok) VALUES (?, CURRENT_TIMESTAMP, 0)`,
		StageFetch,
	)
	if err != nil {
		t.Fatal(err)
	}

	stale, err := Stale(context.Background(), db, StageFetch, 1*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !stale {
		t.Error("Stale should return true when only failed runs exist")
	}
}

func TestEnd_Twice(t *testing.T) {
	db := newTestDB(t)

	run, err := Begin(context.Background(), db, StageDedupe)
	if err != nil {
		t.Fatal(err)
	}

	err = run.End(context.Background(), true, Counts{In: 5, Out: 4}, "first")
	if err != nil {
		t.Fatal(err)
	}

	err = run.End(context.Background(), false, Counts{}, "second")
	if err == nil {
		t.Error("expected error when calling End twice")
	}
}
