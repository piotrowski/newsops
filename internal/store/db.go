package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	_ "modernc.org/sqlite"
)

// Mode controls whether Open takes a write lock.
type Mode int

const (
	ReadOnly  Mode = iota // no lock taken
	ReadWrite             // exclusive, non-blocking lock on data/news.lock
)

// DB wraps *sql.DB with an optional process lock.
type DB struct {
	*sql.DB
	lock *os.File // nil for read-only opens
}

// Open creates dir if needed, opens data/news.db, applies pragmas, runs
// Migrate, and for ReadWrite takes the lock.
func Open(ctx context.Context, dir string, mode Mode) (*DB, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}

	dbPath := dbPath(dir)
	sqlDB, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_journal_mode=WAL&_foreign_keys=ON&_busy_timeout=5000&_synchronous=NORMAL", dbPath))
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// Verify pragmas are active.
	var journalMode string
	err = sqlDB.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode)
	if err != nil || journalMode != "wal" {
		sqlDB.Close()
		return nil, fmt.Errorf("journal_mode pragma failed (got %q): %w", journalMode, err)
	}

	var fkOn int
	err = sqlDB.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fkOn)
	if err != nil || fkOn != 1 {
		sqlDB.Close()
		return nil, fmt.Errorf("foreign_keys pragma failed: %w", err)
	}

	var syncMode int
	err = sqlDB.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&syncMode)
	if err != nil || syncMode != 1 {
		sqlDB.Close()
		return nil, fmt.Errorf("synchronous pragma failed (got %d): %w", syncMode, err)
	}

	var busyTimeout int
	err = sqlDB.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout)
	if err != nil || busyTimeout != 5000 {
		sqlDB.Close()
		return nil, fmt.Errorf("busy_timeout pragma failed: %w", err)
	}

	// Run migrations.
	if err := Migrate(ctx, sqlDB); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("apply migrations: %w", err)
	}

	d := &DB{DB: sqlDB}

	if mode == ReadWrite {
		lockF, err := openLock(dir)
		if err != nil {
			sqlDB.Close()
			return nil, fmt.Errorf("open lock file: %w", err)
		}
		if err := acquireLock(lockF); err != nil {
			lockF.Close()
			sqlDB.Close()
			return nil, fmt.Errorf("acquire lock: %w", err)
		}
		d.lock = lockF
	}

	return d, nil
}

// Close releases the lock and closes the database.
func (db *DB) Close() error {
	var firstErr error
	if db.lock != nil {
		syscall.Flock(int(db.lock.Fd()), syscall.LOCK_UN)
		db.lock.Close()
	}
	if err := db.DB.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// Tx runs fn in a transaction, rolling back on error or panic.
func (db *DB) Tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	defer func() {
		if r := recover(); r != nil {
			tx.Rollback() //nolint:errcheck
			panic(r)      // re-panic after rollback
		}
	}()

	if err := fn(tx); err != nil {
		tx.Rollback() //nolint:errcheck
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}
// dbPath returns the full path to the database file within dir.
func dbPath(dir string) string { return filepath.Join(dir, "news.db") }
