package main

import (
	"context"
	"database/sql"
	"fmt"

	"news-ops/internal/store"
)

// showResult is the JSON output from the show subcommand.
type showResult struct {
	Story     storySummary    `json:"story"`
	Canonical itemSummary     `json:"canonical"`
	Items     []itemSummary   `json:"items"`
	Sources   []sourceInfo    `json:"sources"`
	Decisions []decisionInfo  `json:"decisions"`
}

// storySummary is a summary of the story row.
type storySummary struct {
	ID          int64   `json:"id"`
	SourceCount int     `json:"source_count"`
	Method      string  `json:"method"`
	FirstSeen   string  `json:"first_seen"`
	LastSeen    string  `json:"last_seen"`
	ShownAt     *string `json:"shown_at,omitempty"`
}

// itemSummary is a summary of an item row.
type itemSummary struct {
	ItemID      int64   `json:"item_id"`
	Title       string  `json:"title"`
	URL         string  `json:"url"`
	Source      string  `json:"source"`
	Tier        int     `json:"tier"`
	PublishedAt *string `json:"published_at,omitempty"`
	Excerpt     string  `json:"excerpt"`
}

// sourceInfo is a story_sources entry.
type sourceInfo struct {
	Source string `json:"source"`
	URL    string `json:"url"`
	Kind   string `json:"kind"`
}

// decisionInfo is a decisions log entry.
type decisionInfo struct {
	Action string  `json:"action"`
	Note   *string `json:"note,omitempty"`
	At     string  `json:"at"`
}

func cmdShow(ctx context.Context, args []string) (any, error) {
	if len(args) == 0 {
		return &ExitError{Code: 2, Slug: "invalid_argument", Message: "usage: newsops show <story_id>"}, nil
	}

	storyID, err := parseStoryID(args[0])
	if err != nil {
		return &ExitError{Code: 2, Slug: "invalid_argument", Message: fmt.Sprintf("invalid story ID %q", args[0])}, nil
	}

	db, err := store.Open(ctx, dbDir, store.ReadOnly)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	// Load the story row.
	var ss storySummary
	var shownAt sql.NullString
	err = db.QueryRowContext(ctx, `SELECT id, source_count, method, first_seen, last_seen, shown_at FROM stories WHERE id=?`,
		storyID).Scan(&ss.ID, &ss.SourceCount, &ss.Method, &ss.FirstSeen, &ss.LastSeen, &shownAt)
	if err != nil {
		return &ExitError{Code: 2, Slug: "unknown_story", Message: fmt.Sprintf("story %d not found", storyID)}, nil
	}
	if shownAt.Valid {
		t := shownAt.String
		ss.ShownAt = &t
	}

	// Load the canonical item.
	var canonical itemSummary
	var publishedAt sql.NullString
	err = db.QueryRowContext(ctx, `SELECT i.id, i.title, i.url, i.source, i.tier, i.published_at, i.body_excerpt FROM stories s JOIN items i ON i.id = s.canonical_item_id WHERE s.id=?`,
		storyID).Scan(&canonical.ItemID, &canonical.Title, &canonical.URL, &canonical.Source, &canonical.Tier, &publishedAt, &canonical.Excerpt)
	if err != nil {
		return nil, fmt.Errorf("query canonical item: %w", err)
	}
	if publishedAt.Valid {
		t := publishedAt.String
		canonical.PublishedAt = &t
	}

	// Load all items for this story.
	rows, err := db.QueryContext(ctx, `SELECT id, title, url, source, tier, published_at, body_excerpt FROM items WHERE story_id=? ORDER BY first_seen`,
		storyID)
	if err != nil {
		return nil, fmt.Errorf("query items: %w", err)
	}
	defer rows.Close()

	var items []itemSummary
	for rows.Next() {
		var it itemSummary
		var pub sql.NullString
		if err := rows.Scan(&it.ItemID, &it.Title, &it.URL, &it.Source, &it.Tier, &pub, &it.Excerpt); err != nil {
			return nil, fmt.Errorf("scan item: %w", err)
		}
		if pub.Valid {
			t := pub.String
			it.PublishedAt = &t
		}
		items = append(items, it)
	}

	// Load story_sources.
	srcRows, err := db.QueryContext(ctx, `SELECT source, url, kind FROM story_sources WHERE story_id=? ORDER BY source`,
		storyID)
	if err != nil {
		return nil, fmt.Errorf("query sources: %w", err)
	}
	defer srcRows.Close()

	var sources []sourceInfo
	for srcRows.Next() {
		var si sourceInfo
		if err := srcRows.Scan(&si.Source, &si.URL, &si.Kind); err != nil {
			return nil, fmt.Errorf("scan source: %w", err)
		}
		sources = append(sources, si)
	}

	// Load decisions.
	decRows, err := db.QueryContext(ctx, `SELECT action, note, at FROM decisions WHERE story_id=? ORDER BY at`,
		storyID)
	if err != nil {
		return nil, fmt.Errorf("query decisions: %w", err)
	}
	defer decRows.Close()

	var decisions []decisionInfo
	for decRows.Next() {
		var di decisionInfo
		var note sql.NullString
		if err := decRows.Scan(&di.Action, &note, &di.At); err != nil {
			return nil, fmt.Errorf("scan decision: %w", err)
		}
		if note.Valid {
			n := note.String
			di.Note = &n
		}
		decisions = append(decisions, di)
	}

	return &showResult{
		Story:     ss,
		Canonical: canonical,
		Items:     items,
		Sources:   sources,
		Decisions: decisions,
	}, nil
}

func parseStoryID(s string) (int64, error) {
	var id int64
	_, err := fmt.Sscanf(s, "%d", &id)
	return id, err
}
