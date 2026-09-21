// Package runstate provides run bookkeeping: beginning, ending, and querying
// pipeline runs stored in the SQLite database.
package runstate

import (
	"context"
	"errors"
	"time"

	"news-ops/internal/store"
)

// Stage identifies a pipeline phase.
type Stage string

const (
	StageFetch    Stage = "fetch"
	StageDedupe   Stage = "dedupe"
	StageCandidates Stage = "candidates"
)

// Counts holds the number of items flowing through a stage.
type Counts struct {
	In  int
	Out int
}

type Run struct {
	ID        int64
	Stage     Stage
	StartedAt time.Time
	repo      Repo // for End() calls
}

var errRunEnded = errors.New("run already ended")

// Begin inserts a new row into the runs table with the given stage and the
// current timestamp as started_at. It returns the inserted run (with ID set).
func Begin(ctx context.Context, db store.DB, s Stage) (*Run, error) {
	repo := newRepo(db)
	id, startedAt, err := repo.BeginRun(ctx, s)
	if err != nil {
		return nil, err
	}
	return &Run{ID: id, Stage: s, StartedAt: startedAt, repo: repo}, nil
}

// End marks the run as finished. It is an error to call End twice on the same
// Run (the second call returns errRunEnded).
func (r *Run) End(ctx context.Context, ok bool, c Counts, note string) error {
	return r.repo.EndRun(ctx, r.ID, ok, c.In, c.Out, note)
}

// LastSuccess returns the started_at of the most recent successful run for
// the given stage. It returns found=false when no successful run exists (not
// a zero time). Rows with a NULL ok column (crashed/incomplete runs) are
// ignored.
func LastSuccess(ctx context.Context, db store.DB, s Stage) (time.Time, bool, error) {
	return newRepo(db).LastSuccess(ctx, s)
}

// Stale reports whether the last successful run for stage s is older than
// maxAge. It returns true when no successful run has ever been recorded.
func Stale(ctx context.Context, db store.DB, s Stage, maxAge time.Duration) (bool, error) {
	t, found, err := LastSuccess(ctx, db, s)
	if err != nil {
		return false, err
	}
	if !found {
		return true, nil
	}
	return time.Since(t) > maxAge, nil
}
