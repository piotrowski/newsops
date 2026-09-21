package runstate

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"news-ops/internal/store"
)

// Repo abstracts all database operations for the runs table.
type Repo interface {
	BeginRun(ctx context.Context, stage Stage) (int64, time.Time, error)
	EndRun(ctx context.Context, runID int64, ok bool, in, out int, note string) error
	LastSuccess(ctx context.Context, stage Stage) (time.Time, bool, error)
}

// sqliteRepo implements Repo using a database connection.
type sqliteRepo struct {
	db store.DB
}

func newRepo(db store.DB) Repo { return &sqliteRepo{db: db} }

func (r *sqliteRepo) BeginRun(ctx context.Context, stage Stage) (int64, time.Time, error) {
	res, err := r.db.ExecContext(ctx, `INSERT INTO runs(stage, started_at) VALUES (?, CURRENT_TIMESTAMP)`, stage)
	if err != nil {
		return 0, time.Time{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, time.Time{}, err
	}
	return id, time.Now(), nil
}

func (r *sqliteRepo) EndRun(ctx context.Context, runID int64, ok bool, in, out int, note string) error {
	var endedAt sql.NullString
	if err := r.db.QueryRowContext(ctx, `SELECT ended_at FROM runs WHERE id=?`, runID).Scan(&endedAt); err != nil {
		return err
	}
	if endedAt.Valid {
		return errRunEnded
	}
	_, err := r.db.ExecContext(ctx,
		`UPDATE runs SET ended_at=CURRENT_TIMESTAMP, ok=?, items_in=?, items_out=?, note=? WHERE id=?`,
		ok, in, out, note, runID)
	return err
}

func (r *sqliteRepo) LastSuccess(ctx context.Context, stage Stage) (time.Time, bool, error) {
	var t time.Time
	err := r.db.QueryRowContext(ctx,
		`SELECT started_at FROM runs WHERE stage=? AND ok=1 ORDER BY started_at DESC LIMIT 1`, stage).Scan(&t)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return time.Time{}, false, nil
		}
		return time.Time{}, false, err
	}
	return t, true, nil
}
