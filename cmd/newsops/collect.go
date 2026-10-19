package main

import (
	"context"
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
	Exit       int                    `json:"exit"`
	Stages     map[string]stageOutput `json:"stages"`
	Candidates []candidates.Candidate `json:"candidates"`
}

// ExitCode makes the process exit status follow the "exit" field: 1 for a
// partial fetch whose output is still usable, 2 for a hard failure.
func (r *collectResult) ExitCode() int { return r.Exit }

func cmdCollect(ctx context.Context, args []string) (any, error) {
	// collectMaxAge defaults to 6h at flag registration; 0 means "run every
	// stage regardless of freshness".

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
		Stages:     make(map[string]stageOutput),
		Candidates: []candidates.Candidate{},
	}

	var worstExit int

	// --- Stage 1: fetch ---
	fetchOut, fetchExit, fetchErr := collectRunFetch(ctx, db, cfg)
	switch {
	case fetchErr != nil:
		result.Stages["fetch"] = stageOutput{Ran: true, Result: exitErrorOrPlain(fetchErr)}
		worstExit = exitCodeFromError(fetchErr)
	case fetchOut != nil:
		result.Stages["fetch"] = stageOutput{Ran: true, Result: fetchOut}
		worstExit = fetchExit
	default:
		result.Stages["fetch"] = stageOutput{Skipped: true, Reason: "fresh"}
	}

	if worstExit == 2 {
		result.Exit = 2
		result.Stages["dedupe"] = stageOutput{Skipped: true, Reason: "fetch_failed"}
		result.Stages["candidates"] = stageOutput{Skipped: true, Reason: "fetch_failed"}
		return result, nil
	}

	// --- Stage 2: dedupe ---
	dedupeOut, dedupeErr := runDedupe(ctx, db, cfg)
	switch {
	case dedupeErr != nil:
		result.Stages["dedupe"] = stageOutput{Ran: true, Result: exitErrorOrPlain(dedupeErr)}
		worstExit = 2
	case dedupeOut != nil:
		result.Stages["dedupe"] = stageOutput{Ran: true, Result: dedupeOut}
	default:
		result.Stages["dedupe"] = stageOutput{Skipped: true, Reason: "fresh"}
	}

	if worstExit == 2 {
		result.Exit = 2
		result.Stages["candidates"] = stageOutput{Skipped: true, Reason: "dedupe_failed"}
		return result, nil
	}

	// --- Stage 3: candidates ---
	candidatesOut, candidatesErr := runCandidates(ctx, db, cfg)
	switch {
	case candidatesErr != nil:
		result.Stages["candidates"] = stageOutput{Ran: true, Result: exitErrorOrPlain(candidatesErr)}
		worstExit = 2
	case candidatesOut != nil:
		result.Stages["candidates"] = stageOutput{Ran: true, Result: candidatesOut}
		result.Candidates = candidatesOut.Candidates
	default:
		result.Stages["candidates"] = stageOutput{Skipped: true, Reason: "fresh"}
	}

	result.Exit = worstExit

	if !jsonOut {
		fmt.Fprintf(stderr, "collect: exit=%d\n", result.Exit)
		for _, name := range []string{"fetch", "dedupe", "candidates"} {
			s := result.Stages[name]
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
		return buildEnvelope(ee.Slug, ee.Message, ee.Fields)
	}
	return buildEnvelope("internal", err.Error(), nil)
}

func exitCodeFromError(err error) int {
	var ee *ExitError
	if errors.As(err, &ee) {
		return ee.Code
	}
	return 2
}

// collectRunFetch executes the fetch stage if gated. It returns the stage
// result (nil when skipped), the stage exit code (0 ok, 1 partial, 2 all
// sources failed) and an error only for failures before any source was tried.
func collectRunFetch(ctx context.Context, db store.DB, cfg *config.Config) (*fetchResult, int, error) {
	// Fetch is gated by age only.
	if !collectStaleOrNoFetch(ctx, db, collectMaxAge) {
		return nil, 0, nil
	}

	f := &feed.Fetcher{
		Client:    &http.Client{Timeout: 20 * time.Second},
		UserAgent: "newsops/" + Version + " (+personal feed reader)",
		Limit:     10,
		PerHost:   time.Second,
	}
	if !fetchNoCache {
		f.CacheDir = dbDir
	}

	var sinceFlag *time.Time
	if fetchSince != "" {
		t, err := time.Parse(time.RFC3339, fetchSince)
		if err != nil {
			return nil, 2, &ExitError{Code: 2, Slug: "invalid_argument", Message: fmt.Sprintf("invalid --since value %q: %v", fetchSince, err)}
		}
		sinceFlag = &t
	}

	windowStart, backfilled, err := computeWindow(ctx, db, cfg.Prefilter.MaxBackfillDays, sinceFlag)
	if err != nil {
		return nil, 2, fmt.Errorf("compute window: %w", err)
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
			return nil, 2, &ExitError{Code: 2, Slug: "invalid_argument", Message: fmt.Sprintf("source %q not found in config", fetchSource)}
		}
		sources = filtered
	}

	run, err := runstate.Begin(ctx, db, runstate.StageFetch)
	if err != nil {
		return nil, 2, fmt.Errorf("begin fetch run: %w", err)
	}

	repo := newFetchRepo(db)
	state := &dbStateStore{repo: repo}
	results, err := f.Fetch(ctx, sources, state)
	if err != nil {
		run.End(ctx, false, runstate.Counts{}, "fetch error")
		return nil, 2, fmt.Errorf("fetch: %w", err)
	}

	var seen, newCount, droppedOld int
	failures := []failureRecord{}
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

	// A run in which every source failed is not a success: recording it as
	// one would advance the fetch window past the outage.
	ok := okSources > 0
	note := ""
	if !ok {
		note = "all sources failed"
	}
	run.End(ctx, ok, runstate.Counts{In: seen, Out: newCount}, note)

	var exitCode int
	switch {
	case failedCount == 0:
		exitCode = 0
	case okSources > 0:
		exitCode = 1
	default:
		exitCode = 2
	}

	return &fetchResult{
		Window:   windowInfo{Since: windowStart.UTC().Format(time.RFC3339), Backfilled: backfilled},
		Sources:  sourceCounts{Total: totalSources, OK: okSources, NotModified: notModifiedCount, Failed: failedCount},
		Items:    itemCounts{Seen: seen, New: newCount, DroppedOld: droppedOld},
		Failures: failures,
	}, exitCode, nil
}

// runDedupe executes the dedupe stage if gated. dedupe.Assign owns its own
// run row and transaction, so nothing is wrapped here.
func runDedupe(ctx context.Context, db store.TxDB, cfg *config.Config) (*dedupe.AssignResult, error) {
	// Dedupe is fresh when fetch is fresh AND nothing is waiting for assignment.
	if !collectStaleOrNoFetch(ctx, db, collectMaxAge) && !collectHasUnassignedItems(ctx, db) {
		return nil, nil
	}

	result, err := dedupe.Assign(ctx, db, cfg, dedupe.Options{})
	if err != nil {
		return nil, fmt.Errorf("dedupe: %w", err)
	}
	return result, nil
}

// runCandidates executes the candidates stage if there is anything unshown
// in the window.
func runCandidates(ctx context.Context, db store.DB, cfg *config.Config) (*candidates.Result, error) {
	sinceTime := collectComputeCandidatesSince(ctx, db, cfg.Prefilter.MaxBackfillDays)

	if !collectHasUnshownStories(ctx, db, sinceTime) {
		return &candidates.Result{
			GeneratedAt:   time.Now().UTC(),
			Window:        candidates.WindowSummary{Since: sinceTime.Format(time.RFC3339), Days: int(time.Since(sinceTime).Hours() / 24)},
			TotalInWindow: 0,
			Returned:      0,
			Candidates:    []candidates.Candidate{},
		}, nil
	}

	if candidatesLimit > 0 {
		cfg.Prefilter.CandidateLimit = candidatesLimit
	}

	result, err := candidates.Run(ctx, db, cfg, sinceTime, false)
	if err != nil {
		return nil, fmt.Errorf("candidates: %w", err)
	}

	recordCandidatesRun(ctx, db, result)
	return result, nil
}

// recordCandidatesRun writes the runs row for a candidates pass. Failures
// are reported but do not fail the command.
func recordCandidatesRun(ctx context.Context, db store.DB, result *candidates.Result) {
	run, err := runstate.Begin(ctx, db, runstate.StageCandidates)
	if err != nil {
		fmt.Fprintf(stderr, "newsops: begin candidates run: %v\n", err)
		return
	}
	if err := run.End(ctx, true, runstate.Counts{In: result.TotalInWindow, Out: result.Returned}, ""); err != nil {
		fmt.Fprintf(stderr, "newsops: end candidates run: %v\n", err)
	}
}

// collectStaleOrNoFetch reports whether fetch should run (stale or no prior success).
func collectStaleOrNoFetch(ctx context.Context, db store.DB, maxAge time.Duration) bool {
	t, found, err := runstate.LastSuccess(ctx, db, runstate.StageFetch)
	if err != nil || !found {
		return true
	}
	return time.Since(t) > maxAge
}

// collectHasUnassignedItems reports whether any items have story_id IS NULL.
func collectHasUnassignedItems(ctx context.Context, db store.DB) bool {
	var count int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM items WHERE story_id IS NULL`).Scan(&count)
	return err == nil && count > 0
}

// collectHasUnshownStories reports whether any stories have shown_at IS NULL in the window.
func collectHasUnshownStories(ctx context.Context, db store.DB, sinceTime time.Time) bool {
	var count int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM stories WHERE shown_at IS NULL AND last_seen >= ?`,
		store.FormatTime(sinceTime)).Scan(&count)
	return err == nil && count > 0
}

// collectComputeCandidatesSince determines the window start for candidates:
// the last successful fetch's start, or max_backfill_days ago.
func collectComputeCandidatesSince(ctx context.Context, db store.DB, maxBackfillDays int) time.Time {
	fetchLastTime, found, err := runstate.LastSuccess(ctx, db, runstate.StageFetch)
	if err == nil && found {
		return fetchLastTime
	}
	return time.Now().UTC().Add(-time.Duration(maxBackfillDays) * 24 * time.Hour)
}
