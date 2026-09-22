package main

import (
	"context"
	"fmt"
	"time"

	"news-ops/internal/candidates"
	"news-ops/internal/config"
	"news-ops/internal/runstate"
	"news-ops/internal/store"
)


func cmdCandidates(ctx context.Context, args []string) (any, error) {
	cfg, err := config.Load(configDir)
	if err != nil {
		return &ExitError{Code: 2, Slug: "config_error", Message: fmt.Sprintf("load config: %v", err)}, nil
	}

	db, err := store.Open(ctx, dbDir, store.ReadOnly)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	// Determine window start.
	var sinceTime time.Time
	if candidatesSince != "" {
		t, parseErr := time.Parse(time.RFC3339, candidatesSince)
		if parseErr != nil {
			return nil, &ExitError{Code: 2, Slug: "invalid_argument", Message: fmt.Sprintf("invalid --since value %q: %v", candidatesSince, parseErr)}
		}
		sinceTime = t
	} else {
		// Use last successful fetch's started_at, or fall back to now - max_backfill_days.
		fetchLastTime, found, err := runstate.LastSuccess(ctx, db, runstate.StageFetch)
		if err != nil {
			return nil, fmt.Errorf("query last fetch success: %w", err)
		}
		if found {
			sinceTime = fetchLastTime
		} else {
			sinceTime = time.Now().UTC().Add(-time.Duration(cfg.Prefilter.MaxBackfillDays) * 24 * time.Hour)
		}
	}

	includeShown := candidatesIncludeShown
	limit := candidatesLimit
	if limit > 0 {
		cfg.Prefilter.CandidateLimit = limit
	}

	result, err := candidates.Run(ctx, db, cfg, sinceTime, includeShown)
	if err != nil {
		return nil, fmt.Errorf("candidates: %w", err)
	}

	// Record runstate.
	run, err := runstate.Begin(ctx, db, runstate.StageCandidates)
	if err != nil {
		fmt.Fprintf(stderr, "newsops: begin candidates run: %v\n", err)
	} else {
		err = run.End(ctx, true, runstate.Counts{In: result.TotalInWindow, Out: result.Returned}, "")
		if err != nil {
			fmt.Fprintf(stderr, "newsops: end candidates run: %v\n", err)
		}
	}

	if !jsonOut {
		fmt.Fprintf(stderr, "candidates: %d in window → %d returned (%d shown dropped, %d excluded, %d source-capped)\n",
			result.TotalInWindow, result.Returned,
			result.Dropped.Shown, result.Dropped.Excluded, result.Dropped.SourceCap)
	}

	return result, nil
}
