package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"news-ops/internal/candidates"
	"news-ops/internal/config"
	"news-ops/internal/dedupe"
	"news-ops/internal/feed"
	"news-ops/internal/runstate"
	"news-ops/internal/store"
)


// stageOutput holds one stage's result within the collect output.
type stageOutput struct {
	Ran     bool   `json:"ran"`
	Skipped bool   `json:"skipped"`
	Reason  string `json:"reason,omitempty"`
	Result  any    `json:"result,omitempty"`
}

// collectResult is the JSON output from the collect subcommand.
type collectResult struct {
	Exit       int                        `json:"exit"`
	Stages     map[string]stageOutput     `json:"stages"`
	Candidates []candidates.Candidate     `json:"candidates"`
}

func cmdCollect(ctx context.Context, args []string) (any, error) {
	if fetchMaxAge == 0 {
		fetchMaxAge = 6 * time.Hour
	}

	cfg, err := config.Load(configDir)
	if err != nil {
		return &ExitError{Code: 2, Slug: "config_error", Message: fmt.Sprintf("load config: %v", err)}, nil
	}

	db, err := store.Open(ctx, dbDir, store.ReadWrite)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	result := &collectResult{
		Stages: make(map[string]stageOutput),
	}

	var worstExit int

	// --- Stage 1: fetch ---
	fetchOut, fetchErr := collectRunFetch(ctx, db, cfg)
	if fetchErr != nil {
		result.Stages["fetch"] = stageOutput{Ran: true, Skipped: false, Result: exitErrorOrPlain(fetchErr)}
		worstExit = exitCodeFromError(fetchErr)
	} else if fetchOut != nil {
		if fo, ok := fetchOut.(*fetchResult); ok {
			result.Stages["fetch"] = stageOutput{Ran: true, Skipped: false, Result: fo}
		} else {
			result.Stages["fetch"] = stageOutput{Ran: true, Skipped: false, Result: fetchOut}
		}
	} else {
		result.Stages["fetch"] = stageOutput{Ran: false, Skipped: true, Reason: "fresh"}
	}

	if worstExit == 2 {
		result.Exit = 2
		result.Stages["dedupe"] = stageOutput{Ran: false, Skipped: true, Reason: "fetch_failed"}
		result.Stages["candidates"] = stageOutput{Ran: false, Skipped: true, Reason: "fetch_failed"}
		return result, nil
	}

	// --- Stage 2: dedupe ---
	dedupeOut, dedupeErr := runDedupe(ctx, db, cfg)
	if dedupeErr != nil {
		result.Stages["dedupe"] = stageOutput{Ran: true, Skipped: false, Result: exitErrorOrPlain(dedupeErr)}
		worstExit = 2
	} else if dedupeOut != nil {
		if do, ok := dedupeOut.(*dedupe.AssignResult); ok {
			result.Stages["dedupe"] = stageOutput{Ran: true, Skipped: false, Result: do}
		} else {
			result.Stages["dedupe"] = stageOutput{Ran: true, Skipped: false, Result: dedupeOut}
		}
	} else {
		result.Stages["dedupe"] = stageOutput{Ran: false, Skipped: true, Reason: "fresh"}
	}

	if worstExit == 2 {
		result.Exit = 2
		result.Stages["candidates"] = stageOutput{Ran: false, Skipped: true, Reason: "dedupe_failed"}
		return result, nil
	}

	// --- Stage 3: candidates ---
	candidatesOut, candidatesErr := runCandidates(ctx, db, cfg)
	if candidatesErr != nil {
		result.Stages["candidates"] = stageOutput{Ran: true, Skipped: false, Result: exitErrorOrPlain(candidatesErr)}
		worstExit = 2
	} else if candidatesOut != nil {
		result.Stages["candidates"] = stageOutput{Ran: true, Skipped: false, Result: candidatesOut}
		if co, ok := candidatesOut.(*candidates.Result); ok {
			result.Candidates = co.Candidates
		}
	} else {
		result.Stages["candidates"] = stageOutput{Ran: false, Skipped: true, Reason: "fresh"}
	}

	result.Exit = worstExit

	if !jsonOut {
		fmt.Fprintf(stderr, "collect: exit=%d\n", result.Exit)
		for name, s := range result.Stages {
			if s.Skipped {
				fmt.Fprintf(stderr, "  %s: skipped (%s)\n", name, s.Reason)
			} else if s.Ran {
				fmt.Fprintf(stderr, "  %s: ran\n", name)
			}
		}
		if len(result.Candidates) > 0 {
			fmt.Fprintf(stderr, "  candidates: %d returned\n", len(result.Candidates))
		}
	}

	return result, nil
}

func exitErrorOrPlain(err error) any {
	var ee *ExitError
	if errors.As(err, &ee) {
		return ee
	}
	return err
}

func exitCodeFromError(err error) int {
	var ee *ExitError
	if errors.As(err, &ee) {
		return ee.Code
	}
	return 2
}

// runFetch executes the fetch stage if gated. Returns (result, error).
func collectRunFetch(ctx context.Context, db store.DB, cfg *config.Config) (any, error) {
	// Fetch is gated by age only.
	if !collectStaleOrNoFetch(ctx, db, fetchMaxAge) {
		return nil, nil
	}

	f := &feed.Fetcher{
		Client:      &http.Client{Timeout: 20 * time.Second},
		UserAgent:   "newsops/" + Version + " (+personal feed reader)",
		Limit:       10,
		PerHost:     time.Second,
		CacheDir:    "",
	}

	var sinceFlag *time.Time
	if fetchSince != "" {
		t, err := time.Parse(time.RFC3339, fetchSince)
		if err != nil {
			return nil, &ExitError{Code: 2, Slug: "invalid_argument", Message: fmt.Sprintf("invalid --since value %q: %v", fetchSince, err)}
		}
		sinceFlag = &t
	}

	windowStart, backfilled, err := computeWindow(ctx, db, cfg.Prefilter.MaxBackfillDays, sinceFlag)
	if err != nil {
		return nil, fmt.Errorf("compute window: %w", err)
	}

	sources := cfg.Sources
	if fetchSource != "" {
		var filtered []config.Source
		for _, s := range sources {
			if strings.EqualFold(s.Name, fetchSource) {
				filtered = append(filtered, s)
			}
		}
		if len(filtered) == 0 {
			return nil, &ExitError{Code: 2, Slug: "invalid_argument", Message: fmt.Sprintf("source %q not found in config", fetchSource)}
		}
		sources = filtered
	}

	run, err := runstate.Begin(ctx, db, runstate.StageFetch)
	if err != nil {
		return nil, fmt.Errorf("begin fetch run: %w", err)
	}

	repo := newFetchRepo(db)
	state := &dbStateStore{repo: repo}
	results, err := f.Fetch(ctx, sources, state)
	if err != nil {
		run.End(ctx, false, runstate.Counts{}, "fetch error")
		return nil, fmt.Errorf("fetch: %w", err)
	}

	var seen, newCount, droppedOld int
	var failures []failureRecord
	okSources, notModifiedCount := 0, 0

	for _, r := range results {
		if r.Err != nil {
			failures = append(failures, failureRecord{Source: r.Source.Name, Error: r.Err.Error()})
			continue
		}
		okSources++
		if r.NotModified {
			notModifiedCount++
		}

		for _, item := range r.Items {
			seen++
			if item.Published != nil && item.Published.Before(windowStart) {
				droppedOld++
				continue
			}

			inserted, err := repo.InsertItem(ctx, item, r.Source.Name, r.Source.Tier, r.RawPath)
			if err != nil {
				fmt.Fprintf(stderr, "newsops: insert %s: %v\n", item.Link, err)
				continue
			}
			if inserted {
				newCount++
			}
		}
	}

	totalSources := len(sources)
	failedCount := totalSources - okSources

	ok := true
	c := runstate.Counts{In: seen, Out: newCount}
	run.End(ctx, ok, c, "")

	var exitCode int
	if failedCount == 0 {
		exitCode = 0
	} else if okSources > 0 {
		exitCode = 1
	} else {
		exitCode = 2
	}

	fr := &fetchResult{
		Window:   windowInfo{Since: windowStart.Format(time.RFC3339), Backfilled: backfilled},
		Sources:  sourceCounts{Total: totalSources, OK: okSources, NotModified: notModifiedCount, Failed: failedCount},
		Items:    itemCounts{Seen: seen, New: newCount, DroppedOld: droppedOld},
		Failures: failures,
	}

	if exitCode != 0 {
		return nil, &ExitError{Code: exitCode, Slug: "partial_fetch", Message: fmt.Sprintf("%d/%d sources failed", failedCount, totalSources), Fields: map[string]any{
			"total":    totalSources,
			"ok":       okSources,
			"failed":   failedCount,
			"sources":  fr.Sources,
			"items":    fr.Items,
			"failures": failures,
		}}
	}

	return fr, nil
}

// runDedupe executes the dedupe stage if gated. Returns (result, error).
func runDedupe(ctx context.Context, db store.DB, cfg *config.Config) (any, error) {
	// Check if dedupe is fresh: fetch ran recently AND no unassigned items.
	if !collectStaleOrNoFetch(ctx, db, fetchMaxAge) && !collectHasUnassignedItems(ctx, db) {
		return nil, nil
	}

	run, err := runstate.Begin(ctx, db, runstate.StageDedupe)
	if err != nil {
		return nil, fmt.Errorf("begin dedupe run: %w", err)
	}

	var result *dedupe.AssignResult
	txdb, ok := db.(store.TxDB)
	if !ok {
		return nil, fmt.Errorf("database does not support transactions")
	}
	err = txdb.Tx(ctx, func(tx *sql.Tx) error {
		res, err := dedupe.Assign(ctx, txdb, cfg)
		if err != nil {
			return err
		}
		result = res
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("dedupe: %w", err)
	}

	c := runstate.Counts{In: result.ItemsIn, Out: result.StoriesCreated + result.StoriesUpdated}
	run.End(ctx, true, c, "")

	return result, nil
}

// runCandidates executes the candidates stage if gated. Returns (result, error).
func runCandidates(ctx context.Context, db store.DB, cfg *config.Config) (any, error) {
	sinceTime := collectComputeCandidatesSince(ctx, db, cfg.Prefilter.MaxBackfillDays)

	hasUnshown := collectHasUnshownStories(ctx, db, sinceTime)
	if !hasUnshown {
		return &candidates.Result{
			GeneratedAt:   time.Now().UTC(),
			Window:        candidates.WindowSummary{Since: sinceTime.Format(time.RFC3339), Days: int(time.Since(sinceTime).Hours() / 24)},
			TotalInWindow: 0,
			Returned:      0,
			Candidates:    []candidates.Candidate{},
		}, nil
	}

	limit := candidatesLimit
	if limit > 0 {
		cfg.Prefilter.CandidateLimit = limit
	}

	result, err := candidates.Run(ctx, db, cfg, sinceTime, false)
	if err != nil {
		return nil, fmt.Errorf("candidates: %w", err)
	}

	run, err := runstate.Begin(ctx, db, runstate.StageCandidates)
	if err != nil {
		fmt.Fprintf(stderr, "newsops: begin candidates run: %v\n", err)
	} else {
		err = run.End(ctx, true, runstate.Counts{In: result.TotalInWindow, Out: result.Returned}, "")
		if err != nil {
			fmt.Fprintf(stderr, "newsops: end candidates run: %v\n", err)
		}
	}

	return result, nil
}

// StaleOrNoFetch reports whether fetch should run (stale or no prior success).
func collectStaleOrNoFetch(ctx context.Context, db store.DB, maxAge time.Duration) bool {
	t, found, err := runstate.LastSuccess(ctx, db, runstate.StageFetch)
	if err != nil {
		return true
	}
	if !found {
		return true
	}
	return time.Since(t) > maxAge
}

// hasUnassignedItems reports whether any items have story_id IS NULL.
func collectHasUnassignedItems(ctx context.Context, db store.DB) bool {
	var count int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM items WHERE story_id IS NULL`).Scan(&count)
	return err == nil && count > 0
}

// hasUnshownStories reports whether any stories have shown_at IS NULL in the window.
func collectHasUnshownStories(ctx context.Context, db store.DB, sinceTime time.Time) bool {
	var count int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM stories WHERE shown_at IS NULL AND last_seen >= ?`,
		sinceTime.Format(time.RFC3339)).Scan(&count)
	return err == nil && count > 0
}

// computeCandidatesSince determines the window start for candidates.
func collectComputeCandidatesSince(ctx context.Context, db store.DB, maxBackfillDays int) time.Time {
	fetchLastTime, found, err := runstate.LastSuccess(ctx, db, runstate.StageFetch)
	if err != nil {
		return time.Now().UTC().Add(-time.Duration(maxBackfillDays) * 24 * time.Hour)
	}
	if found {
		return fetchLastTime
	}
	return time.Now().UTC().Add(-time.Duration(maxBackfillDays) * 24 * time.Hour)
}
