package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"news-ops/internal/runstate"
	"news-ops/internal/store"
)


// stageInfo holds per-stage status for JSON output.
type stageInfo struct {
	LastSuccess *string `json:"last_success"`
	AgeMin      *int    `json:"age_min"`
	Stale       bool    `json:"stale"`
}

// digestInfo holds the digest file path and mtime for JSON output.
type digestInfo struct {
	Path        string  `json:"path"`
	GeneratedAt *string `json:"generated_at"`
}

// statusResult is the JSON output from the status subcommand.
type statusResult struct {
	Stages map[string]stageInfo `json:"stages"`
	Counts struct {
		ItemsTotal      int `json:"items_total"`
		NewSinceFetch   int `json:"new_since_fetch"`
		UnshownStories  int `json:"unshown_stories"`
	} `json:"counts"`
	Digest digestInfo `json:"digest"`
}

func cmdStatus(ctx context.Context, args []string) (any, error) {
	if fetchMaxAge == 0 {
		fetchMaxAge = 6 * time.Hour
	}

	result := statusResult{
		Stages: make(map[string]stageInfo),
		Digest: digestInfo{Path: "digest.md"},
	}

	// Report the digest file's mtime, if it exists.
	if digestPath != "" {
		result.Digest.Path = digestPath
		if fi, err := os.Stat(digestPath); err == nil {
			t := fi.ModTime().UTC().Format(time.RFC3339)
			result.Digest.GeneratedAt = &t
		}
	}

	// Check if the database file exists. If not, return virgin shape
	// without touching the filesystem — no directories or files created.
	dbPath := filepath.Join(dbDir, "news.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		for _, stage := range []runstate.Stage{
			runstate.StageFetch, runstate.StageDedupe, runstate.StageCandidates,
		} {
			result.Stages[string(stage)] = stageInfo{Stale: true}
		}
		return result, nil
	}

	// Open the database ReadOnly.
	db, err := store.Open(ctx, dbDir, store.ReadOnly)
	if err != nil {
		// Missing DB is not an error for status; return virgin shape.
		for _, stage := range []runstate.Stage{
			runstate.StageFetch, runstate.StageDedupe, runstate.StageCandidates,
		} {
			result.Stages[string(stage)] = stageInfo{Stale: true}
		}
		return result, nil
	}
	defer db.Close()

	now := time.Now()

	for _, stage := range []runstate.Stage{
		runstate.StageFetch, runstate.StageDedupe, runstate.StageCandidates,
	} {
		stale, err := runstate.Stale(ctx, db, stage, fetchMaxAge)
		if err != nil {
			return nil, fmt.Errorf("check %s stale: %w", stage, err)
		}

		lastTime, found, err := runstate.LastSuccess(ctx, db, stage)
		if err != nil {
			return nil, fmt.Errorf("query %s last success: %w", stage, err)
		}

		si := stageInfo{Stale: stale}
		if found {
			t := lastTime.UTC().Format(time.RFC3339)
			si.LastSuccess = &t
			ageMin := int(now.Sub(lastTime).Minutes())
			si.AgeMin = &ageMin
		}
		result.Stages[string(stage)] = si
	}

	// Counts from the database.
	var itemsTotal int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM items`).Scan(&itemsTotal); err != nil {
		return nil, fmt.Errorf("count items: %w", err)
	}
	result.Counts.ItemsTotal = itemsTotal

	// new_since_fetch: items with first_seen >= last successful fetch started_at.
	fetchLastTime, found, err := runstate.LastSuccess(ctx, db, runstate.StageFetch)
	if err != nil {
		return nil, fmt.Errorf("query fetch last success: %w", err)
	}
	if found {
		var newCount int
		err = db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM items WHERE first_seen >= ?`,
			store.FormatTime(fetchLastTime)).Scan(&newCount)
		if err != nil {
			return nil, fmt.Errorf("count new since fetch: %w", err)
		}
		result.Counts.NewSinceFetch = newCount
	}

	// unshown_stories: shown_at IS NULL and last_seen within 7 days.
	var sevenDaysAgo string
	if found {
		sevenDaysAgo = store.FormatTime(fetchLastTime.Add(-7 * 24 * time.Hour))
	} else {
		// No fetch has ever succeeded — nothing can be new, so use epoch.
		sevenDaysAgo = store.FormatTime(time.Time{})
	}

	err = db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM stories
		 WHERE shown_at IS NULL AND last_seen >= ?`,
		sevenDaysAgo).Scan(&result.Counts.UnshownStories)
	if err != nil {
		return nil, fmt.Errorf("count unshown stories: %w", err)
	}

	return result, nil
}
