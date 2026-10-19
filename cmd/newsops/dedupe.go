package main

import (
	"context"
	"fmt"
	"time"

	"news-ops/internal/config"
	"news-ops/internal/dedupe"
	"news-ops/internal/store"
)

// dedupeWindow is the title-match time window flag.
var dedupeWindow time.Duration

// dedupeLimit caps how many unassigned items to process (0 = unlimited).
var dedupeLimit int

func cmdDedupe(ctx context.Context, args []string) (any, error) {
	cfg, err := config.Load(configDir)
	if err != nil {
		return &ExitError{Code: 2, Slug: "config_error", Message: fmt.Sprintf("load config: %v", err)}, nil
	}

	db, err := store.Open(ctx, dbDir, store.ReadWrite)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	result, err := dedupe.Assign(ctx, db, cfg, dedupe.Options{Window: dedupeWindow, Limit: dedupeLimit})
	if err != nil {
		return nil, err
	}

	if !jsonOut {
		fmt.Fprintf(stderr, "dedupe: %d items in → %d stories created, %d updated\n",
			result.ItemsIn, result.StoriesCreated, result.StoriesUpdated)
		for method, count := range result.Methods {
			fmt.Fprintf(stderr, "  method=%s: %d\n", method, count)
		}
		if result.DiscussionsLinked > 0 {
			fmt.Fprintf(stderr, "  discussions linked: %d\n", result.DiscussionsLinked)
		}
	}

	return result, nil
}
