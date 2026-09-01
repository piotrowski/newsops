package store

import (
	"context"
	"database/sql"
	"fmt"
)

// migration represents a single schema version.
type migration struct {
	Version int
	SQL     string
}

// migrations is the ordered list of all migrations.
var migrations = []migration{
	{1, schemaV1},
}

// Migrate applies every migration above the recorded version, each inside its
// own transaction, and records it in schema_version.
// It enables foreign keys before any DDL so constraints are enforced.
func Migrate(ctx context.Context, db *sql.DB) error {
	// Enable FK enforcement (modernc.org/sqlite doesn't enable it by default).
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		return fmt.Errorf("enable foreign keys: %w", err)
	}

	// Determine the current schema version.  If the table doesn't exist yet,
	// we're at version 0.
	var current int
	exists, err := tableExists(ctx, db, "schema_version")
	if err != nil {
		return fmt.Errorf("check schema_version: %w", err)
	}
	if exists {
		err = db.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_version").Scan(&current)
		if err != nil {
			return fmt.Errorf("read schema version: %w", err)
		}
	}

	for _, m := range migrations {
		if m.Version <= current {
			continue // already applied or before current
		}
		if err := applyMigration(ctx, db, m); err != nil {
			return fmt.Errorf("migration %d: %w", m.Version, err)
		}
		current = m.Version
	}

	return nil
}

// applyMigration runs the DDL inside a transaction and records it.
func applyMigration(ctx context.Context, db *sql.DB, m migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}

	// Enable FK enforcement inside the transaction too.
	if _, err := tx.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		tx.Rollback()
		return fmt.Errorf("enable foreign keys in tx: %w", err)
	}

	if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
		tx.Rollback()
		return fmt.Errorf("execute DDL: %w", err)
	}

	if _, err := tx.ExecContext(
		ctx,
		"INSERT INTO schema_version(version, applied_at) VALUES (?, CURRENT_TIMESTAMP)",
		m.Version,
	); err != nil {
		tx.Rollback()
		return fmt.Errorf("record migration: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}

	return nil
}

// tableExists returns true if the named table exists in the database.
func tableExists(ctx context.Context, db *sql.DB, name string) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?",
		name,
	).Scan(&count)
	return count > 0, err
}
