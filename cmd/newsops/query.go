package main

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"news-ops/internal/config"
	"news-ops/internal/dedupe"
	"news-ops/internal/store"
)

// queryFlags hold the subcommand-specific flags.
var (
	queryText     string
	queryDomain   string
	queryStoryID  int64
	queryRelated  bool
	queryDays     int
	queryLimit    int
)

// queryResult is the JSON output from the query subcommand.
type queryResult struct {
	Query   QueryParams `json:"query"`
	Total   int         `json:"total"`
	Results []QueryItem `json:"results"`
}

// QueryParams holds the parameters used for a query.
type QueryParams struct {
	Text    string `json:"text,omitempty"`
	Domain  string `json:"domain,omitempty"`
	StoryID *int64 `json:"story_id,omitempty"`
	Related bool   `json:"related,omitempty"`
	Days    int    `json:"days"`
	Limit   int    `json:"limit"`
}

// QueryItem is one story match from a query.
type QueryItem struct {
	StoryID     int64  `json:"story_id"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	Source      string `json:"source"`
	PublishedAt string `json:"published_at"`
	FirstSeen   string `json:"first_seen"`
	SourceCount int    `json:"source_count"`
	Snippet     string `json:"snippet"`
}

func cmdQuery(ctx context.Context, args []string) (any, error) {
	if queryDays == 0 {
		queryDays = 90
	}
	if queryLimit == 0 {
		queryLimit = 20
	}

	// At least one of --text, --domain, or --story/--related is required.
	hasText := queryText != ""
	hasDomain := queryDomain != ""
	hasRelated := queryRelated && queryStoryID > 0
	if !hasText && !hasDomain && !hasRelated {
		return &ExitError{Code: 2, Slug: "invalid_argument", Message: "at least one of --text, --domain, or --story with --related is required"}, nil
	}

	db, err := store.Open(ctx, dbDir, store.ReadOnly)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	cfg, err := config.Load(configDir)
	if err != nil {
		return &ExitError{Code: 2, Slug: "config_error", Message: fmt.Sprintf("load config: %v", err)}, nil
	}

	var textQuery string

	if hasText {
		textQuery = sanitizeFTSQuery(queryText)
	}

	if hasRelated {
		// Look up the story's canonical item to build a related query.
		canonicalItem, err := lookupCanonicalItem(ctx, db, queryStoryID)
		if err != nil {
			return nil, fmt.Errorf("lookup story %d: %w", queryStoryID, err)
		}
		if canonicalItem == nil {
			return &ExitError{Code: 2, Slug: "unknown_story", Message: fmt.Sprintf("story %d not found", queryStoryID)}, nil
		}

		// Build related query from normalized title + URL host.
		sourceNames := make([]string, len(cfg.Sources))
		for i, s := range cfg.Sources {
			sourceNames[i] = s.Name
		}
		normalized := dedupe.NormalizeTitle(canonicalItem.Title, sourceNames)
		textQuery = buildRelatedQuery(normalized, canonicalItem.Host)
	}

	// Build the SQL query.
	var sql strings.Builder
	sql.WriteString(`SELECT DISTINCT s.id, i.title, i.url, i.source, i.published_at,
		s.first_seen, s.source_count,
		snippet(items_fts, '...', '<b>', '</b>', '...', 30) AS snippet
	FROM stories s
	JOIN items i ON i.id = s.canonical_item_id
	WHERE s.last_seen >= datetime('now', ?)`)

	var queryArgs []any
	queryArgs = append(queryArgs, fmt.Sprintf("-%d days", queryDays))

	if textQuery != "" {
		sql.WriteString(" AND items_fts MATCH ?")
		queryArgs = append(queryArgs, textQuery)
	}

	if hasDomain {
		domain := strings.ToLower(queryDomain)
		sql.WriteString(` AND (LOWER(i.source) LIKE ? OR LOWER(i.url) LIKE ?)`)
		queryArgs = append(queryArgs, domain+"%", "%."+domain)
	}

	sql.WriteString(` ORDER BY rank LIMIT ?`)
	queryArgs = append(queryArgs, queryLimit)

	rows, err := db.QueryContext(ctx, sql.String(), queryArgs...)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	var results []QueryItem
	for rows.Next() {
		var item QueryItem
		if err := rows.Scan(&item.StoryID, &item.Title, &item.URL, &item.Source,
			&item.PublishedAt, &item.FirstSeen, &item.SourceCount, &item.Snippet); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		results = append(results, item)
	}

	var total int
	err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM stories WHERE last_seen >= datetime('now', ?)`,
		fmt.Sprintf("-%d days", queryDays)).Scan(&total)
	if err != nil {
		total = len(results) // fallback
	}

	qp := QueryParams{Days: queryDays, Limit: queryLimit}
	if hasText {
		qp.Text = queryText
	}
	if hasDomain {
		qp.Domain = queryDomain
	}
	if hasRelated {
		qp.StoryID = &queryStoryID
		qp.Related = true
	}

	return &queryResult{Query: qp, Total: total, Results: results}, nil
}

// sanitizeFTSQuery sanitizes user text for safe FTS5 consumption.
// Strips FTS operators (-, *, ", parentheses), wraps tokens in quotes, joins with OR.
func sanitizeFTSQuery(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}

	// Strip known FTS operators: -, *, ", (, )
	cleaned := strings.Builder{}
	for i := 0; i < len(text); i++ {
		ch := text[i]
		switch ch {
		case '-', '*', '"', '(', ')':
			continue // skip FTS operators
		default:
			cleaned.WriteByte(ch)
		}
	}
	text = cleaned.String()

	// Tokenize on whitespace, wrap each token in double quotes
	tokens := strings.Fields(text)
	if len(tokens) == 0 {
		return ""
	}

	quoted := make([]string, len(tokens))
	for i, t := range tokens {
		quoted[i] = `"` + t + `"`
	}

	// Join with OR for multi-word phrases
	return strings.Join(quoted, " OR ")
}

// buildRelatedQuery builds a query from a normalized title and URL host.
// Drops stopwords, keeps 5 longest remaining tokens, ORs them together,
// and also includes the URL host.
func buildRelatedQuery(normalized string, urlHost string) string {
	tokens := strings.Fields(normalized)

	// Drop stopwords
	stopwords := map[string]bool{
		"a": true, "an": true, "the": true, "and": true, "or": true,
		"but": true, "in": true, "on": true, "at": true, "to": true,
		"for": true, "of": true, "with": true, "by": true, "from": true,
	}

	var kept []string
	for _, t := range tokens {
		lower := strings.ToLower(t)
		if stopwords[lower] || len(t) <= 2 {
			continue
		}
		kept = append(kept, t)
	}

	// Keep the 5 longest remaining tokens (already in order by position)
	if len(kept) > 5 {
		kept = kept[:5]
	}

	var parts []string
	for _, t := range kept {
		parts = append(parts, `"`+t+`"`)
	}

	if urlHost != "" {
		parts = append(parts, `"`+urlHost+`"`)
	}

	return strings.Join(parts, " OR ")
}

// canonicalItem holds the info needed for related queries.
type canonicalItem struct {
	Title string
	URL   string
	Host  string
}

// lookupCanonicalItem fetches the canonical item for a story.
func lookupCanonicalItem(ctx context.Context, db store.DB, storyID int64) (*canonicalItem, error) {
	var title, rawURL string
	err := db.QueryRowContext(ctx, `SELECT i.title, i.url FROM stories s JOIN items i ON i.id = s.canonical_item_id WHERE s.id = ?`,
		storyID).Scan(&title, &rawURL)
	if err != nil {
		return nil, err
	}

	var host string
	if u, err := url.Parse(rawURL); err == nil {
		host = u.Host
	}

	return &canonicalItem{Title: title, URL: rawURL, Host: host}, nil
}
