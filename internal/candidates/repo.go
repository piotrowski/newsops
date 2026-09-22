package candidates

import (
	"context"
	"database/sql"
	"time"
	"fmt"
	"strings"

	"news-ops/internal/store"
)

// loadStories queries the database for all stories in the given time window.
func loadStories(ctx context.Context, db store.DB, sinceTime time.Time, includeShown bool) ([]storyRow, error) {
	var where strings.Builder
	where.WriteString(`SELECT s.id, i.title, i.url, i.source, i.tier,
		s.source_count, s.first_seen, s.last_seen, s.shown_at,
		i.published_at, i.body_excerpt
	FROM stories s
	JOIN items i ON i.id = s.canonical_item_id
	WHERE s.last_seen >= ?`)
	args := []any{sinceTime.Format(time.RFC3339)}

	if !includeShown {
		where.WriteString(" AND s.shown_at IS NULL")
	}

	where.WriteString(` ORDER BY s.id`)

	rows, err := db.QueryContext(ctx, where.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("query stories: %w", err)
	}
	defer rows.Close()

	var result []storyRow
	for rows.Next() {
		var r storyRow
		var shownAt sql.NullString
		if err := rows.Scan(
			&r.ID, &r.Title, &r.URL, &r.Source, &r.Tier,
			&r.SourceCount, &r.FirstSeen, &r.LastSeen, &shownAt,
			&r.PublishedAt, &r.Excerpt,
		); err != nil {
			return nil, fmt.Errorf("scan story row: %w", err)
		}
		r.Shown = shownAt.Valid
		result = append(result, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate stories: %w", err)
	}

	// Enrich with distinct source names from story_sources.
	for i := range result {
		sources, err := loadStorySources(ctx, db, result[i].ID)
		if err != nil {
			return nil, fmt.Errorf("load sources for story %d: %w", result[i].ID, err)
		}
		result[i].Sources = sources
	}

	return result, nil
}

// loadStorySources returns distinct source names for a story.
func loadStorySources(ctx context.Context, db store.DB, storyID int64) ([]string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT DISTINCT source FROM story_sources WHERE story_id=? ORDER BY source`, storyID)
	if err != nil {
		return nil, fmt.Errorf("query sources: %w", err)
	}
	defer rows.Close()

	var sources []string
	for rows.Next() {
		var src string
		if err := rows.Scan(&src); err != nil {
			return nil, fmt.Errorf("scan source: %w", err)
		}
		sources = append(sources, src)
	}
	return sources, nil
}
