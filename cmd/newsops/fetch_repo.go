package main

import (
	"context"
	"fmt"

	"news-ops/internal/feed"
	"news-ops/internal/store"
)

// fetchRepo abstracts all database operations for the fetch workflow.
type fetchRepo interface {
	GetFeedState(ctx context.Context, source string) (etag, lastModified string, err error)
	PutFeedState(ctx context.Context, source, url, etag, lastModified string, status int, errMsg string) error
	InsertItem(ctx context.Context, item feed.ParsedItem, sourceName string, tier int, rawPath string) (bool, error)
}

// sqliteFetchRepo implements fetchRepo using a database connection.
type sqliteFetchRepo struct {
	db store.DB
}

func newFetchRepo(db store.DB) fetchRepo { return &sqliteFetchRepo{db: db} }

func (r *sqliteFetchRepo) GetFeedState(ctx context.Context, source string) (string, string, error) {
	var etag, lastModified string
	err := r.db.QueryRowContext(ctx,
		`SELECT etag, last_modified FROM feed_state WHERE source = ?`, source).Scan(&etag, &lastModified)
	if err != nil {
		return "", "", nil // no row → no conditional headers
	}
	return etag, lastModified, nil
}

func (r *sqliteFetchRepo) PutFeedState(ctx context.Context, source, url, etag, lastModified string, status int, errMsg string) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO feed_state(source, url, etag, last_modified, last_fetched, last_status, last_error)
		 VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP, ?, ?)
		 ON CONFLICT(source) DO UPDATE SET
			 url=excluded.url, etag=excluded.etag, last_modified=excluded.last_modified,
			 last_fetched=CURRENT_TIMESTAMP, last_status=excluded.last_status, last_error=excluded.last_error`,
		source, url, etag, lastModified, status, errMsg)
	return err
}

func (r *sqliteFetchRepo) InsertItem(ctx context.Context, item feed.ParsedItem, sourceName string, tier int, rawPath string) (bool, error) {
	var publishedAt interface{}
	if item.Published != nil {
		publishedAt = store.FormatTime(*item.Published)
	}
	var commentsURL interface{}
	if item.CommentsURL != "" {
		commentsURL = item.CommentsURL
	}

	res, err := r.db.ExecContext(ctx,
		`INSERT INTO items(url, canonical_url, title, source, tier, published_at, first_seen, body_hash, body_excerpt, raw_path, comments_url, story_id)
		 VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, ?, ?, ?, ?, NULL)
		 ON CONFLICT(url) DO NOTHING`,
		item.Link, item.Link, item.Title, sourceName, tier, publishedAt,
		feed.BodyHash(item), feed.Excerpt(item), rawPath, commentsURL,
	)
	if err != nil {
		return false, fmt.Errorf("insert item: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows > 0, nil
}
