// Package candidates implements the prefilter stage: narrowing ~300 stories to
// ~40 using deterministic, defensible rules — volume control, not quality
// judgment. The ordering it emits is presentation order, not a quality claim.
package candidates

import (
	"context"
	"fmt"
	"strings"
	"sort"
	"time"

	"news-ops/internal/config"
	"news-ops/internal/store"
)

// Result holds the output of a candidates run.
type Result struct {
	GeneratedAt time.Time     `json:"generated_at"`
	Window      WindowSummary `json:"window"`
	TotalInWindow int        `json:"total_in_window"`
	Returned    int           `json:"returned"`
	Dropped     DropCounts    `json:"dropped"`
	Candidates  []Candidate   `json:"candidates"`
}

// WindowSummary describes the time window used.
type WindowSummary struct {
	Since string `json:"since"`
	Days  int    `json:"days"`
}

// DropCounts tracks how many stories were removed at each stage.
type DropCounts struct {
	Shown      int `json:"shown"`
	Excluded   int `json:"excluded"`
	SourceCap  int `json:"source_cap"`
}

// Candidate is one story that passed all prefilter rules.
type Candidate struct {
	StoryID     int64    `json:"story_id"`
	Title       string   `json:"title"`
	URL         string   `json:"url"`
	Source      string   `json:"source"`
	Tier        int      `json:"tier"`
	SourceCount int      `json:"source_count"`
	Sources     []string `json:"sources"`
	PublishedAt string   `json:"published_at"`
	Primary     bool     `json:"primary"`
	Excerpt     string   `json:"excerpt"`
}

// Run produces a candidate list from the given database. It opens ReadOnly,
// writes nothing (including no shown_at), and records a runstate row for stage
// "candidates".
func Run(ctx context.Context, db store.DB, cfg *config.Config, since time.Time, includeShown bool) (*Result, error) {
	// Step 1: Load all stories in the window.
	stories, err := loadStories(ctx, db, since, includeShown)
	if err != nil {
		return nil, fmt.Errorf("load stories: %w", err)
	}

	totalInWindow := len(stories)
	var droppedShown int

	// Step 2: Drop shown (unless --include-shown).
	if !includeShown {
		before := len(stories)
		stories = dropShown(stories)
		droppedShown = before - len(stories)
	}

	// Step 3: Drop excluded (exclude_terms on canonical title).
	excludedBefore := len(stories)
	stories = dropExcluded(stories, cfg.Prefilter.ExcludeTerms)
	droppedExcluded := excludedBefore - len(stories)

	// Step 4: Order — tier asc, source_count desc, last_seen desc, story_id asc.
	orderByPriority(stories)

	// Step 5: Per-source cap on canonical item's source.
	stories = perSourceCap(stories, cfg.Prefilter.PerSourceCap)
	droppedSourceCap := excludedBefore - droppedExcluded - len(stories)

	// Step 6: Limit.
	limit := cfg.Prefilter.CandidateLimit
	if limit <= 0 {
		limit = 40 // safety fallback
	}
	if len(stories) > limit {
		stories = stories[:limit]
	}

	// Build output candidates.
	candidates := make([]Candidate, len(stories))
	for i, s := range stories {
		candidates[i] = toCandidate(s, cfg)
	}

	return &Result{
		GeneratedAt:   time.Now().UTC(),
		Window:        WindowSummary{Since: since.Format(time.RFC3339), Days: int(time.Since(since).Hours() / 24)},
		TotalInWindow: totalInWindow,
		Returned:      len(candidates),
		Dropped: DropCounts{
			Shown:     droppedShown,
			Excluded:  droppedExcluded,
			SourceCap: droppedSourceCap,
		},
		Candidates: candidates,
	}, nil
}

// storyRow is the internal representation loaded from the database.
type storyRow struct {
	ID          int64
	Title       string
	URL         string
	Source      string
	Tier        int
	SourceCount int
	Sources     []string
	PublishedAt string
	Primary     bool
	Excerpt     string
	LastSeen    string
	FirstSeen   string
	Shown       bool
}

func dropShown(stories []storyRow) []storyRow {
	out := make([]storyRow, 0, len(stories))
	for _, s := range stories {
		if !s.Shown {
			out = append(out, s)
		}
	}
	return out
}

func dropExcluded(stories []storyRow, terms []string) []storyRow {
	if len(terms) == 0 {
		return stories
	}
	out := make([]storyRow, 0, len(stories))
	for _, s := range stories {
		lowerTitle := strings.ToLower(s.Title)
		excluded := false
		for _, term := range terms {
			if strings.Contains(lowerTitle, strings.ToLower(term)) {
				excluded = true
				break
			}
		}
		if !excluded {
			out = append(out, s)
		}
	}
	return out
}

// orderByPriority sorts stories: tier asc, source_count desc, last_seen desc, story_id asc.
func orderByPriority(stories []storyRow) {
	// stable sort: each key in reverse priority order so earlier keys break ties.
	sortByStoryID(stories)
	sortByLastSeen(stories)
	sortBySourceCount(stories)
	sortByTier(stories)
}

type byTier []storyRow
func (x byTier) Len() int      { return len(x) }
func (x byTier) Less(i, j int) bool { return x[i].Tier < x[j].Tier }
func (x byTier) Swap(i, j int) { x[i], x[j] = x[j], x[i] }

type bySourceCountDesc []storyRow
func (x bySourceCountDesc) Len() int      { return len(x) }
func (x bySourceCountDesc) Less(i, j int) bool { return x[i].SourceCount > x[j].SourceCount }
func (x bySourceCountDesc) Swap(i, j int) { x[i], x[j] = x[j], x[i] }

type byLastSeenDesc []storyRow
func (x byLastSeenDesc) Len() int      { return len(x) }
func (x byLastSeenDesc) Less(i, j int) bool { return x[i].LastSeen > x[j].LastSeen }
func (x byLastSeenDesc) Swap(i, j int) { x[i], x[j] = x[j], x[i] }

type byStoryIDAsc []storyRow
func (x byStoryIDAsc) Len() int      { return len(x) }
func (x byStoryIDAsc) Less(i, j int) bool { return x[i].ID < x[j].ID }
func (x byStoryIDAsc) Swap(i, j int) { x[i], x[j] = x[j], x[i] }

func sortByTier(stories []storyRow)      { sort.Sort(byTier(stories)) }
func sortBySourceCount(stories []storyRow) { sort.Sort(bySourceCountDesc(stories)) }
func sortByLastSeen(stories []storyRow)    { sort.Sort(byLastSeenDesc(stories)) }
func sortByStoryID(stories []storyRow)     { sort.Sort(byStoryIDAsc(stories)) }

// perSourceCap keeps at most maxPerSource stories per canonical source.
func perSourceCap(stories []storyRow, maxPerSource int) []storyRow {
	if maxPerSource <= 0 {
		return stories
	}
	count := make(map[string]int)
	out := make([]storyRow, 0, len(stories))
	for _, s := range stories {
		if count[s.Source] < maxPerSource {
			out = append(out, s)
			count[s.Source]++
		}
	}
	return out
}

func toCandidate(s storyRow, cfg *config.Config) Candidate {
	primary := cfg.IsPrimary(s.URL)
	return Candidate{
		StoryID:     s.ID,
		Title:       s.Title,
		URL:         s.URL,
		Source:      s.Source,
		Tier:        s.Tier,
		SourceCount: s.SourceCount,
		Sources:     s.Sources,
		PublishedAt: s.PublishedAt,
		Primary:     primary,
		Excerpt:     s.Excerpt,
	}
}
