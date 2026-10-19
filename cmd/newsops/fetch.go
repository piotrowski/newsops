package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"news-ops/internal/config"
	"news-ops/internal/feed"
	"news-ops/internal/runstate"
	"news-ops/internal/store"
)

// fetchResult is the JSON output for the fetch command.
type fetchResult struct {
	Skipped   bool             `json:"skipped,omitempty"`
	Reason    string           `json:"reason,omitempty"`
	LastSuccess *string        `json:"last_success,omitempty"`
	Window    windowInfo       `json:"window,omitempty"`
	Sources   sourceCounts     `json:"sources,omitempty"`
	Items     itemCounts       `json:"items,omitempty"`
	Failures  []failureRecord  `json:"failures,omitempty"`
}

type windowInfo struct {
	Since      string `json:"since"`
	Backfilled bool   `json:"backfilled"`
}

type sourceCounts struct {
	Total         int `json:"total"`
	OK            int `json:"ok"`
	NotModified   int `json:"not_modified"`
	Failed        int `json:"failed"`
}

type itemCounts struct {
	Seen      int `json:"seen"`
	New       int `json:"new"`
	DroppedOld int `json:"dropped_old"`
}

type failureRecord struct {
	Source string `json:"source"`
	Error  string `json:"error"`
}

// dbStateStore implements feed.StateStore backed by the SQLite database.
type dbStateStore struct {
	repo fetchRepo
}

func (s *dbStateStore) Get(ctx context.Context, source string) (string, string, error) {
	return s.repo.GetFeedState(ctx, source)
}

func (s *dbStateStore) Put(ctx context.Context, source, feedURL, etag, lastModified string, status int, errMsg string) error {
	return s.repo.PutFeedState(ctx, source, feedURL, etag, lastModified, status, errMsg)
}

// cmdFetch implements the fetch subcommand.
func cmdFetch(ctx context.Context, args []string) (any, error) {
	// Parse --since flag.
	var sinceFlag *time.Time
	if fetchSince != "" {
		t, err := time.Parse(time.RFC3339, fetchSince)
		if err != nil {
			return nil, &ExitError{Code: 2, Slug: "invalid_argument", Message: fmt.Sprintf("invalid --since value %q: %v", fetchSince, err)}
		}
		sinceFlag = &t
	}

	// Open database ReadWrite.
	db, err := store.Open(ctx, dbDir, store.ReadWrite)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	// Load configuration.
	cfg, err := config.Load(configDir)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	// Determine window start.
	windowStart, backfilled, err := computeWindow(ctx, db, cfg.Prefilter.MaxBackfillDays, sinceFlag)
	if err != nil {
		return nil, fmt.Errorf("compute window: %w", err)
	}

	// --max-age guard.
	if fetchMaxAge > 0 {
		lastSuccessTime, found, err := runstate.LastSuccess(ctx, db, runstate.StageFetch)
		if err != nil {
			return nil, fmt.Errorf("query last success: %w", err)
		}
		if found && time.Since(lastSuccessTime) < fetchMaxAge {
			ts := lastSuccessTime.Format(time.RFC3339)
			return &fetchResult{
				Skipped:     true,
				Reason:      "fresh",
				LastSuccess: &ts,
			}, nil
		}
	}

	// Filter sources if --source specified.
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

	// Begin runbook.
	run, err := runstate.Begin(ctx, db, runstate.StageFetch)
	if err != nil {
		return nil, fmt.Errorf("begin run: %w", err)
	}

	// Set up fetcher.
	f := &feed.Fetcher{
		Client:   &http.Client{Timeout: 20 * time.Second},
		UserAgent: "newsops/" + Version + " (+personal feed reader)",
		Limit:    10,
		PerHost:  time.Second,
		CacheDir: "",
	}
	if !fetchNoCache {
		f.CacheDir = dbDir // store cache alongside database
	}

	repo := newFetchRepo(db)
	state := &dbStateStore{repo: repo}
	// Fetch all sources.
	results, err := f.Fetch(ctx, sources, state)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}

	// Insert items and count outcomes.
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
			// Drop items older than window (but keep null dates).
			if item.Published != nil && item.Published.Before(windowStart) {
				droppedOld++
				continue
			}

			// Insert with ON CONFLICT(url) DO NOTHING.
			inserted, err := repo.InsertItem(ctx, item, r.Source.Name, r.Source.Tier, r.RawPath)
			if err != nil {
				// Log but don't fail the run — one bad insert shouldn't abort everything.
				fmt.Fprintf(stderr, "newsops: insert %s: %v\n", item.Link, err)
				continue
			}
			if inserted {
				newCount++
			}
		}
	}

	// End runbook. A partial fetch still advances the window, but a run in
	// which every source failed must not: otherwise the next fetch would
	// treat everything published during the outage as old and drop it.
	ok := okSources > 0
	note := ""
	if !ok {
		note = "all sources failed"
	}
	c := runstate.Counts{In: seen, Out: newCount}
	if err := run.End(ctx, ok, c, note); err != nil {
		fmt.Fprintf(stderr, "newsops: end run: %v\n", err)
	}

	// Determine exit code.
	totalSources := len(sources)
	failedCount := totalSources - okSources

	var exitCode int
	if len(failures) == 0 || failedCount == 0 {
		exitCode = 0 // all succeeded or skipped
	} else if okSources > 0 {
		exitCode = 1 // partial: some failed but some succeeded
	} else {
		exitCode = 2 // all sources failed
	}

	if exitCode != 0 {
		return &ExitError{Code: exitCode, Slug: "partial_fetch", Message: fmt.Sprintf("%d/%d sources failed", failedCount, totalSources), Fields: map[string]any{
			"total":    totalSources,
			"ok":       okSources,
			"failed":   failedCount,
			"sources":  sourceCounts{Total: totalSources, OK: okSources, NotModified: notModifiedCount, Failed: failedCount},
			"items":    itemCounts{Seen: seen, New: newCount, DroppedOld: droppedOld},
			"failures": failures,
		}}, nil
	}

	return &fetchResult{
		Window: windowInfo{Since: windowStart.UTC().Format(time.RFC3339), Backfilled: backfilled},
		Sources: sourceCounts{Total: totalSources, OK: okSources, NotModified: notModifiedCount, Failed: failedCount},
		Items:   itemCounts{Seen: seen, New: newCount, DroppedOld: droppedOld},
	}, nil
}

// computeWindow determines the collection window start time.
func computeWindow(ctx context.Context, db store.DB, maxBackfillDays int, sinceFlag *time.Time) (time.Time, bool, error) {
	if sinceFlag != nil {
		return *sinceFlag, false, nil
	}

	windowStart := time.Now().AddDate(0, 0, -maxBackfillDays)
	backfilled := true

	lastSuccessTime, found, err := runstate.LastSuccess(ctx, db, runstate.StageFetch)
	if err != nil {
		return windowStart, backfilled, err
	}
	if !found {
		return windowStart, backfilled, nil
	}

	// Use last success if it's within the backfill window.
	if lastSuccessTime.After(windowStart) {
		windowStart = lastSuccessTime
		backfilled = false
	}

	return windowStart, backfilled, nil
}


