package dedupe

import (
	"context"
	"database/sql"
	"fmt"

	"news-ops/internal/config"
)
type Repo interface {
	CountUnassigned(ctx context.Context) (int, error)
	ListUnassigned(ctx context.Context) ([]itemRow, error)
	UpdateCanonicalURL(ctx context.Context, itemID int64, canonicalURL string) error
	LoadStoryMap(ctx context.Context) (storyMap, error)
	CreateStory(ctx context.Context, item itemRow, canonicalURL string) (int64, error)
	UpdateStoryMethod(ctx context.Context, storyID int64, method string) error
	InsertArticleSource(ctx context.Context, storyID int64, source, url string) error
	AssignItemToStory(ctx context.Context, itemID, storyID int64) error
	InsertDiscussionSource(ctx context.Context, storyID int64, source, url string) error
	UpdateStoryStats(ctx context.Context, storyID int64, sourceCount int, lastSeen string) error
	RecomputeCanonicalItem(ctx context.Context, cfg *config.Config, storyID int64) (int64, error)
	GetItemTitle(ctx context.Context, itemID int64) (string, error)
	RecomputeStory(ctx context.Context, cfg *config.Config, storyID int64) error
}

// sqliteRepo implements Repo using a database transaction.
type sqliteRepo struct {
	tx *sql.Tx
}

func newRepo(tx *sql.Tx) Repo { return &sqliteRepo{tx: tx} }

func (r *sqliteRepo) CountUnassigned(ctx context.Context) (int, error) {
	var count int
	err := r.tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM items WHERE story_id IS NULL`).Scan(&count)
	return count, err
}

func (r *sqliteRepo) ListUnassigned(ctx context.Context) ([]itemRow, error) {
	rows, err := r.tx.QueryContext(ctx,
		`SELECT id, url, canonical_url, title, source, tier, published_at,
		        first_seen, story_id, comments_url
		 FROM items WHERE story_id IS NULL
		 ORDER BY first_seen ASC`)
	if err != nil {
		return nil, fmt.Errorf("query unassigned: %w", err)
	}
	defer rows.Close()

	var items []itemRow
	for rows.Next() {
		var ir itemRow
		if err := rows.Scan(&ir.ID, &ir.URL, &ir.CanonicalURL, &ir.Title,
			&ir.Source, &ir.Tier, &ir.Published, &ir.FirstSeen, &ir.StoryID, &ir.CommentsURL); err != nil {
			return nil, fmt.Errorf("scan item: %w", err)
		}
		items = append(items, ir)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate items: %w", err)
	}
	return items, nil
}

func (r *sqliteRepo) UpdateCanonicalURL(ctx context.Context, itemID int64, canonicalURL string) error {
	_, err := r.tx.ExecContext(ctx, `UPDATE items SET canonical_url=? WHERE id=?`, canonicalURL, itemID)
	return err
}

func (r *sqliteRepo) LoadStoryMap(ctx context.Context) (storyMap, error) {
	sm := storyMap{
		canonicalURLs: make(map[string]int64),
		stories:       make(map[int64]storyInfo),
	}

	rows, err := r.tx.QueryContext(ctx, `
		SELECT s.id, s.canonical_item_id, s.source_count, s.method, s.last_seen,
		       i.url
		FROM stories s
		JOIN items i ON i.id = s.canonical_item_id`)
	if err != nil {
		return sm, fmt.Errorf("query stories: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var si storyInfo
		var canonicalURL string
		if err := rows.Scan(&si.ID, &si.CanonicalID, &si.SourceCount,
			&si.Method, &si.LastSeen, &canonicalURL); err != nil {
			return sm, fmt.Errorf("scan story: %w", err)
		}
		sm.stories[si.ID] = si
		sm.canonicalURLs[canonicalURL] = si.ID
	}
	if err := rows.Err(); err != nil {
		return sm, fmt.Errorf("iterate stories: %w", err)
	}
	return sm, nil
}

func (r *sqliteRepo) CreateStory(ctx context.Context, item itemRow, canonicalURL string) (int64, error) {
	res, err := r.tx.ExecContext(ctx,
		`INSERT INTO stories(canonical_item_id, source_count, method, first_seen, last_seen)
		 VALUES (?, 1, 'url', ?, ?)`,
		item.ID, item.FirstSeen, item.FirstSeen)
	if err != nil {
		return 0, fmt.Errorf("insert story: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("get story id: %w", err)
	}

	_, err = r.tx.ExecContext(ctx,
		`INSERT INTO story_sources(story_id, source, url, kind) VALUES (?, ?, ?, 'article')`,
		id, item.Source, canonicalURL)
	if err != nil {
		return 0, fmt.Errorf("insert story_source: %w", err)
	}

	return id, nil
}

func (r *sqliteRepo) UpdateStoryMethod(ctx context.Context, storyID int64, method string) error {
	_, err := r.tx.ExecContext(ctx, `UPDATE stories SET method=? WHERE id=?`, method, storyID)
	return err
}

func (r *sqliteRepo) InsertArticleSource(ctx context.Context, storyID int64, source, url string) error {
	_, err := r.tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO story_sources(story_id, source, url, kind) VALUES (?, ?, ?, 'article')`,
		storyID, source, url)
	return err
}

func (r *sqliteRepo) AssignItemToStory(ctx context.Context, itemID, storyID int64) error {
	_, err := r.tx.ExecContext(ctx, `UPDATE items SET story_id=? WHERE id=?`, storyID, itemID)
	return err
}

func (r *sqliteRepo) InsertDiscussionSource(ctx context.Context, storyID int64, source, url string) error {
	_, err := r.tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO story_sources(story_id, source, url, kind) VALUES (?, ?, ?, 'discussion')`,
		storyID, source, url)
	return err
}

func (r *sqliteRepo) UpdateStoryStats(ctx context.Context, storyID int64, sourceCount int, lastSeen string) error {
	_, err := r.tx.ExecContext(ctx, `UPDATE stories SET source_count=?, last_seen=? WHERE id=?`,
		sourceCount, lastSeen, storyID)
	return err
}

func (r *sqliteRepo) RecomputeCanonicalItem(ctx context.Context, cfg *config.Config, storyID int64) (int64, error) {
	rows, err := r.tx.QueryContext(ctx, `
		SELECT i.id, i.published_at, i.url
		FROM items i
		WHERE i.story_id=? AND i.published_at IS NOT NULL
		ORDER BY i.published_at ASC, i.id ASC`, storyID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	type candidate struct {
		ID          int64
		PublishedAt string
		URL         string
	}

	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.ID, &c.PublishedAt, &c.URL); err != nil {
			return 0, err
		}
		candidates = append(candidates, c)
	}

	if len(candidates) == 0 {
		var minID int64
		err := r.tx.QueryRowContext(ctx,
			`SELECT MIN(id) FROM items WHERE story_id=?`, storyID).Scan(&minID)
		return minID, err
	}

	earliest := candidates[0].PublishedAt
	var best candidate
	for _, c := range candidates {
		if c.PublishedAt != earliest {
			break
		}
		if best.ID == 0 {
			best = c
			continue
		}

		bestPrimary := cfg.IsPrimary(best.URL)
		candPrimary := cfg.IsPrimary(c.URL)

		if candPrimary && !bestPrimary {
			best = c
		} else if candPrimary == bestPrimary {
			if c.ID < best.ID {
				best = c
			}
		}
	}

	return best.ID, nil
}

func (r *sqliteRepo) GetItemTitle(ctx context.Context, itemID int64) (string, error) {
	var title string
	err := r.tx.QueryRowContext(ctx, `SELECT title FROM items WHERE id=?`, itemID).Scan(&title)
	return title, err
}
func (r *sqliteRepo) RecomputeStory(ctx context.Context, cfg *config.Config, storyID int64) error {
	var sourceCount int
	if err := r.tx.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT source) FROM story_sources WHERE story_id=?`, storyID).Scan(&sourceCount); err != nil {
		return err
	}

	var lastSeen string
	if err := r.tx.QueryRowContext(ctx,
		`SELECT MAX(first_seen) FROM items WHERE story_id=?`, storyID).Scan(&lastSeen); err != nil {
		return err
	}

	newCanonID, err := r.RecomputeCanonicalItem(ctx, cfg, storyID)
	if err != nil || newCanonID == 0 {
		return err
	}

	_, err = r.tx.ExecContext(ctx,
		`UPDATE stories SET source_count=?, last_seen=?, canonical_item_id=? WHERE id=?`,
		sourceCount, lastSeen, newCanonID, storyID)
	return err
}
