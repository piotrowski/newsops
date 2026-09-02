package feed

import (
	"bytes"
	"time"

	"github.com/mmcdole/gofeed"
	"github.com/mmcdole/gofeed/atom"
	rss2 "github.com/mmcdole/gofeed/rss"
	"news-ops/internal/config"
)

// ParsedItem is a single article extracted from an RSS/Atom/RDF feed.
type ParsedItem struct {
	Title       string
	Link        string     // as published
	Content     string     // best available body text, markup stripped
	Published   *time.Time // nil when the feed omits it or it is unparseable
	Categories  []string
	CommentsURL string     // aggregator discussion link, "" when absent
	GUID        string
}

// Parse handles RSS 2.0, Atom and RDF via gofeed.
func Parse(body []byte, src config.Source) ([]ParsedItem, error) {
	p := gofeed.NewParser()
	feed, err := p.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	items := make([]ParsedItem, 0, len(feed.Items))
	for _, it := range feed.Items {
		items = append(items, parseItem(it))
	}

	// Merge comments URLs from feed-specific parsers.
	// gofeed's universal type doesn't expose RSS <comments> or Atom rel="replies".
	switch feed.FeedType {
	case "rss":
		mergeRSSComments(body, items)
	case "Atom":
		mergeAtomReplies(body, items)
	}

	return items, nil
}

func parseItem(it *gofeed.Item) ParsedItem {
	p := ParsedItem{
		Title:       it.Title,
		Link:        it.Link,
		GUID:        it.GUID,
		Categories:  it.Categories,
	}

	// Content precedence: item.Content → item.Description → "".
	content := it.Content
	if content == "" {
		content = it.Description
	}
	p.Content = content

	// Published precedence: PublishedParsed → UpdatedParsed → nil.
	if it.PublishedParsed != nil {
		p.Published = it.PublishedParsed
	} else if it.UpdatedParsed != nil {
		p.Published = it.UpdatedParsed
	}

	return p
}

func mergeRSSComments(body []byte, items []ParsedItem) {
	rp := &rss2.Parser{}
	feed, err := rp.Parse(bytes.NewReader(body))
	if err != nil {
		return // best-effort; don't fail on secondary parse
	}
	for i, item := range feed.Items {
		if i < len(items) && item.Comments != "" {
			items[i].CommentsURL = item.Comments
		}
	}
}

func mergeAtomReplies(body []byte, items []ParsedItem) {
	ap := &atom.Parser{}
	feed, err := ap.Parse(bytes.NewReader(body))
	if err != nil {
		return // best-effort
	}
	for i, entry := range feed.Entries {
		if i < len(items) {
			items[i].CommentsURL = findReplyLink(entry.Links)
		}
	}
}

func findReplyLink(links []*atom.Link) string {
	for _, l := range links {
		if l.Rel == "replies" {
			return l.Href
		}
	}
	return ""
}
