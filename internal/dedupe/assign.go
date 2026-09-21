package dedupe

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"news-ops/internal/config"
	"news-ops/internal/runstate"
	"news-ops/internal/store"
)

// itemRow represents a row from the items table as returned by our queries.
type itemRow struct {
	ID           int64
	URL          string
	CanonicalURL string
	Title        string
	Source       string
	Tier         int
	Published    *string
	FirstSeen    string
	StoryID      sql.NullInt64
	CommentsURL  sql.NullString
}

// AssignResult is the JSON output from the dedupe command.
type AssignResult struct {
	ItemsIn           int            `json:"items_in"`
	StoriesCreated    int            `json:"stories_created"`
	StoriesUpdated    int            `json:"stories_updated"`
	Methods           map[string]int `json:"methods"`
	DiscussionsLinked int            `json:"discussions_linked"`
}

// Assign processes all items with story_id IS NULL, assigning them to stories.
func Assign(ctx context.Context, db store.TxDB, cfg *config.Config) (*AssignResult, error) {
	const window = 48 * time.Hour

	run, err := runstate.Begin(ctx, db, runstate.StageDedupe)
	if err != nil {
		return nil, fmt.Errorf("begin dedupe run: %w", err)
	}

	var result AssignResult
	result.Methods = make(map[string]int)

	err = db.Tx(ctx, func(tx *sql.Tx) error {
		repo := newRepo(tx)

		count, err := repo.CountUnassigned(ctx)
		if err != nil {
			return fmt.Errorf("count unassigned: %w", err)
		}
		result.ItemsIn = count

		if count == 0 {
			return nil
		}

		items, err := repo.ListUnassigned(ctx)
		if err != nil {
			return fmt.Errorf("list unassigned: %w", err)
		}

		var sourceNames []string
		for _, s := range cfg.Sources {
			sourceNames = append(sourceNames, s.Name)
		}

		sm, err := repo.LoadStoryMap(ctx)
		if err != nil {
			return fmt.Errorf("load story map: %w", err)
		}

		for _, item := range items {
			canonicalURL, err := Canonical(item.CanonicalURL)
			if err != nil {
				canonicalURL = item.CanonicalURL
			}

			src := findSource(cfg.Sources, item.Source)
			_, discussionLink := ResolveAggregator(
				ParsedItem{Link: item.URL, CommentsURL: item.CommentsURL.String}, src,
			)

			if err := repo.UpdateCanonicalURL(ctx, item.ID, canonicalURL); err != nil {
				return fmt.Errorf("update canonical_url: %w", err)
			}

			// Layer 1 — URL match.
			storyID := urlMatch(sm, canonicalURL)
			if storyID > 0 {
				repo.InsertArticleSource(ctx, storyID, item.Source, canonicalURL)
				repo.AssignItemToStory(ctx, item.ID, storyID)
				if discussionLink != nil && discussionLink.URL != "" {
					_ = repo.InsertDiscussionSource(ctx, storyID, item.Source, discussionLink.URL)
					result.DiscussionsLinked++
				}
				recomputeStory(ctx, repo, cfg, storyID)
				result.StoriesUpdated++
				continue
			}

			// Layer 2 — Title match.
			storyID = titleMatch(ctx, repo, cfg, sm, item.Title, item.FirstSeen, window, sourceNames)
			if storyID > 0 {
				si := sm.stories[storyID]
				if si.Method == "url" {
					_ = repo.UpdateStoryMethod(ctx, storyID, "title")
				}
				repo.InsertArticleSource(ctx, storyID, item.Source, canonicalURL)
				repo.AssignItemToStory(ctx, item.ID, storyID)
				if discussionLink != nil && discussionLink.URL != "" {
					_ = repo.InsertDiscussionSource(ctx, storyID, item.Source, discussionLink.URL)
					result.DiscussionsLinked++
				}
				recomputeStory(ctx, repo, cfg, storyID)
				result.StoriesUpdated++
				continue
			}

			// No match — create new story.
			storyID, err = repo.CreateStory(ctx, item, canonicalURL)
			if err != nil {
				return fmt.Errorf("create story for item %d: %w", item.ID, err)
			}
			sm.canonicalURLs[canonicalURL] = storyID
			createdTime, _ := parseTime(item.FirstSeen)
			sm.stories[storyID] = storyInfo{ID: storyID, CanonicalID: item.ID, Method: "url", LastSeen: createdTime}

			result.StoriesCreated++
			result.Methods["url"]++
			repo.InsertArticleSource(ctx, storyID, item.Source, canonicalURL)
			repo.AssignItemToStory(ctx, item.ID, storyID)
			if discussionLink != nil && discussionLink.URL != "" {
				_ = repo.InsertDiscussionSource(ctx, storyID, item.Source, discussionLink.URL)
				result.DiscussionsLinked++
			}
			recomputeStory(ctx, repo, cfg, storyID)
		}

		return nil
	})

	if err != nil {
		run.End(ctx, false, runstate.Counts{In: result.ItemsIn}, err.Error())
		return nil, fmt.Errorf("dedupe transaction: %w", err)
	}

	out := result.StoriesCreated + result.StoriesUpdated
	run.End(ctx, true, runstate.Counts{In: result.ItemsIn, Out: out}, "")

	return &result, nil
}

func findSource(sources []config.Source, name string) config.Source {
	for _, s := range sources {
		if s.Name == name {
			return s
		}
	}
	return config.Source{}
}

type storyMap struct {
	canonicalURLs map[string]int64
	stories       map[int64]storyInfo
}

type storyInfo struct {
	ID          int64
	CanonicalID int64
	SourceCount int
	Method      string
	LastSeen    time.Time
}

func urlMatch(sm storyMap, canonicalURL string) int64 {
	if storyID, ok := sm.canonicalURLs[canonicalURL]; ok {
		return storyID
	}
	return 0
}

func titleMatch(ctx context.Context, repo Repo, cfg *config.Config, sm storyMap,
	itemTitle string, firstSeen string, window time.Duration, sourceNames []string) int64 {

	itemTime, err := parseTime(firstSeen)
	if err != nil {
		return 0
	}

	maxDist := cfg.Prefilter.TitleSimhashMax

	for _, si := range sm.stories {
		lastSeenTime, err := parseTime(si.LastSeen.Format(time.RFC3339))
		if err != nil {
			continue
		}

		diff := itemTime.Sub(lastSeenTime)
		if diff < -window || diff > window {
			continue
		}

		canonTitle, err := repo.GetItemTitle(ctx, si.CanonicalID)
		if err != nil {
			continue
		}

		if SimilarTitles(itemTitle, canonTitle, sourceNames, maxDist) {
			return si.ID
		}
	}

	return 0
}

func recomputeStory(ctx context.Context, repo Repo, cfg *config.Config, storyID int64) {
	_ = repo.RecomputeStory(ctx, cfg, storyID)
}

func parseTime(s string) (time.Time, error) {
	for _, layout := range []string{
		time.RFC3339,
		"2006-01-02T15:04:05Z",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized time format: %q", s)
}

// CmdDedupe is the entry point for the "dedupe" subcommand.
func CmdDedupe(ctx context.Context, db store.TxDB, cfg *config.Config) (any, error) {
	result, err := Assign(ctx, db, cfg)
	if err != nil {
		return nil, err
	}
	return result, nil
}
