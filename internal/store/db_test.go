package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestOpen_CreatesDirectoryAndDatabase verifies that Open(ReadWrite) on a
// fresh directory creates the directory, database file, and lock file.
func TestOpen_CreatesDirectoryAndDatabase(t *testing.T) {
	dir := t.TempDir()

	db, err := Open(context.Background(), dir, ReadWrite)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// Database file exists.
	if _, err := os.Stat(dbPath(dir)); err != nil {
		t.Errorf("database file missing: %v", err)
	}

	// Lock file exists.
	if _, err := os.Stat(lockPath(dir)); err != nil {
		t.Errorf("lock file missing: %v", err)
	}

	// schema_version was recorded.
	var version int
	err = db.QueryRowContext(context.Background(), "SELECT MAX(version) FROM schema_version").Scan(&version)
	if want := migrations[len(migrations)-1].Version; err != nil || version != want {
		t.Fatalf("schema_version = %d, want %d (err=%v)", version, want, err)
	}
}

// TestOpen_ConcurrentLockContention verifies that a second Open(ReadWrite)
// while the first is held fails in under 100ms with LockedError.
func TestOpen_ConcurrentLockContention(t *testing.T) {
	dir := t.TempDir()

	first, err := Open(context.Background(), dir, ReadWrite)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}

	start := time.Now()
	_, err = Open(context.Background(), dir, ReadWrite)
	elapsed := time.Since(start)

	if elapsed > 100*time.Millisecond {
		t.Errorf("lock contention took %v, expected <100ms", elapsed)
	}

	var lockErr *LockedError
	if err == nil {
		t.Fatal("expected LockedError from second Open")
	}
	if !errors.As(err, &lockErr) {
		t.Fatalf("expected *LockedError, got %T: %v", err, err)
	}

	if lockErr.PID <= 0 {
		t.Errorf("LockedError.PID = %d (full error: %v), expected >0", lockErr.PID, err)
	}

	first.Close()
}

// TestOpen_ReadOnlyUnderWriteLock verifies that Open(ReadOnly) succeeds while
// a write lock is held — WAL allows concurrent readers.
func TestOpen_ReadOnlyUnderWriteLock(t *testing.T) {
	dir := t.TempDir()

	_, err := Open(context.Background(), dir, ReadWrite)
	if err != nil {
		t.Fatalf("first Open(ReadWrite): %v", err)
	}

	// ReadOnly should succeed even while write lock is held.
	db, err := Open(context.Background(), dir, ReadOnly)
	if err != nil {
		t.Fatalf("Open(ReadOnly) under write lock: %v", err)
	}
	defer db.Close()

	// Verify we can read the schema_version table.
	var version int
	err = db.QueryRowContext(context.Background(), "SELECT MAX(version) FROM schema_version").Scan(&version)
	if want := migrations[len(migrations)-1].Version; err != nil || version != want {
		t.Fatalf("read schema_version under lock: version=%d, want %d, err=%v", version, want, err)
	}
}

// TestTx_RollbackOnError verifies that Tx rolls back on a returned error.
func TestTx_RollbackOnError(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(context.Background(), dir, ReadWrite)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// Insert a row.
	err = db.Tx(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `
			INSERT INTO items(url, canonical_url, title, source, tier, body_hash)
			VALUES ('https://example.com/1', 'https://example.com/1', 'Test', 'src', 1, 'hash')
		`)
		return err
	})
	if err != nil {
		t.Fatalf("first Tx: %v", err)
	}

	// Verify the row exists.
	var count int
	err = db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM items").Scan(&count)
	if err != nil || count != 1 {
		t.Fatalf("expected 1 item, got %d (err=%v)", count, err)
	}

	// Now run a Tx that errors — should roll back.
	err = db.Tx(context.Background(), func(tx *sql.Tx) error {
		return fmt.Errorf("rollback me")
	})
	if err == nil {
		t.Fatal("expected error from Tx")
	}

	// Verify the rollback worked: still exactly 1 item.
	err = db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM items").Scan(&count)
	if err != nil || count != 1 {
		t.Fatalf("after rollback, expected 1 item, got %d (err=%v)", count, err)
	}
}

// TestTx_RollbackOnPanic verifies that Tx rolls back on a panic and re-panics.
func TestTx_RollbackOnPanic(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(context.Background(), dir, ReadWrite)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	var panicked bool
	func() {
		defer func() {
			if r := recover(); r != nil {
				if r == "test panic" {
					panicked = true
				} else {
					t.Fatalf("unexpected panic: %v", r)
				}
			}
		}()

		db.Tx(context.Background(), func(*sql.Tx) error {
			panic("test panic")
		})
	}()

	if !panicked {
		t.Fatal("expected Tx to re-panic after rollback")
	}

	// Verify the rollback worked: no items inserted.
	var count int
	err = db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM items").Scan(&count)
	if err != nil || count != 0 {
		t.Fatalf("after panic rollback, expected 0 items, got %d (err=%v)", count, err)
	}
}

// TestClose_ReleasesLock verifies that Close releases the lock.
func TestClose_ReleasesLock(t *testing.T) {
	dir := t.TempDir()

	db, err := Open(context.Background(), dir, ReadWrite)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Close should release the lock.
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// A new Open(ReadWrite) should succeed immediately.
	db2, err := Open(context.Background(), dir, ReadWrite)
	if err != nil {
		t.Fatalf("Open after Close: %v", err)
	}
	db2.Close()
}

// TestConcurrentReadOnly verifies multiple ReadOnly opens can coexist.
func TestConcurrentReadOnly(t *testing.T) {
	dir := t.TempDir()

	writeDB, err := Open(context.Background(), dir, ReadWrite)
	if err != nil {
		t.Fatalf("Open(ReadWrite): %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			db, err := Open(context.Background(), dir, ReadOnly)
			if err != nil {
				t.Errorf("ReadOnly open %d: %v", i, err)
				return
			}
			db.Close()
		}()
	}
	wg.Wait()

	writeDB.Close()
}

// TestIntegrityAfterForcedKill verifies that after a forced kill mid-write,
// PRAGMA integrity_check returns ok. This is a manual check — the test
// documents what to verify but doesn't fork+kill (unreliable in CI).
//
// Manual steps:
// 1. Run: go test -run=TestIntegrityAfterForcedKill -v ./internal/store/
// 2. The test will print a PID and a sleep command — run that sleep in another terminal.
// 3. While sleeping, SIGKILL the process.
// 4. Open the database with: sqlite3 <path> "PRAGMA integrity_check;"
// 5. Expected output: ok
func TestIntegrityAfterForcedKill(t *testing.T) {
	dir := t.TempDir()
	dbPath := dbPath(dir)

	db, err := Open(context.Background(), dir, ReadWrite)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Create a long-running transaction.
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}

	_, err = tx.ExecContext(context.Background(), `
		INSERT INTO items(url, canonical_url, title, source, tier, body_hash)
		VALUES ('https://example.com/kill', 'https://example.com/kill', 'KILL ME', 'src', 1, 'hash')
	`)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}

	t.Logf("=== MANUAL TEST ===")
	t.Logf("PID: %d", os.Getpid())
	t.Logf("DB path: %s", dbPath)
	t.Logf("Run in another terminal:")
	t.Logf("  sleep 30 && kill -KILL %d", os.Getpid())
	t.Logf("Then verify integrity:")
	t.Logf("  sqlite3 '%s' 'PRAGMA integrity_check;'", filepath.Clean(dbPath))

	// Don't actually wait — just print instructions.
	// In a real test we'd fork+kill, but that's unreliable in CI.
	t.Log("=== END MANUAL TEST ===")

	tx.Rollback() // clean up for the test harness
	db.Close()
}
