package feed_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"news-ops/internal/feed"
	"news-ops/internal/config"
)

var src = config.Source{} // zero-value; unused by Parse itself

func TestParse_fixtures(t *testing.T) {
	fixtures := []string{
		"rss2-basic.xml",
		"atom-basic.xml",
		"hn.xml",
		"reddit.xml",
		"truncated.xml",
		"no-date.xml",
		"entities.xml",
	}

	for _, f := range fixtures {
		t.Run(f, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", f))
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}

			items, err := feed.Parse(data, src)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if len(items) < 1 {
				t.Fatalf("expected ≥1 items, got %d", len(items))
			}

			for i, item := range items {
				t.Run("", func(t *testing.T) {
					// Titles should never be empty for real feeds.
					if item.Title == "" {
						t.Error("empty title")
					}
					// Excerpt must not contain angle brackets or whitespace runs.
					ex := feed.Excerpt(item)
					if strings.ContainsAny(ex, "<>") {
						t.Errorf("excerpt contains < or >")
					}
					if strings.Contains(ex, "  ") {
						t.Error("excerpt has double-space (whitespace run)")
					}
					// Excerpt must be valid UTF-8.
					if !utf8.ValidString(ex) {
						t.Error("excerpt is not valid UTF-8")
					}
					// Excerpt must not exceed 1500 chars (unless source text < 1500).
					if len(ex) > 1500 && len(strings.TrimSpace(item.Content)) > 1500 {
						t.Errorf("excerpt %d chars exceeds 1500", len(ex))
					}

					// BodyHash is stable across parses.
					hash1 := feed.BodyHash(item)
					items2, err := feed.Parse(data, src)
					if err != nil {
						t.Fatalf("re-parse: %v", err)
					}
					hash2 := feed.BodyHash(items2[i])
					if hash1 != hash2 {
						t.Errorf("BodyHash unstable: %s vs %s", hash1, hash2)
					}
				})
			}
		})
	}
}

func TestParse_categories(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "rss2-basic.xml"))
	if err != nil {
		t.Fatal(err)
	}
	items, err := feed.Parse(data, src)
	if err != nil {
		t.Fatal(err)
	}
	// First item should have categories from RSS <category>.
	cats := items[0].Categories
	if len(cats) == 0 {
		t.Fatal("expected categories from RSS <category>")
	}

	data, err = os.ReadFile(filepath.Join("testdata", "atom-basic.xml"))
	if err != nil {
		t.Fatal(err)
	}
	items, err = feed.Parse(data, src)
	if err != nil {
		t.Fatal(err)
	}
	cats = items[0].Categories
	if len(cats) == 0 {
		t.Fatal("expected categories from Atom <category term=...>")
	}
}

func TestParse_hn_commentsURL(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "hn.xml"))
	if err != nil {
		t.Fatal(err)
	}
	items, err := feed.Parse(data, src)
	if err != nil {
		t.Fatal(err)
	}

	for _, item := range items {
		if item.Link == "" {
			t.Error("Link should be set")
		}
		if item.CommentsURL == "" {
			t.Error("CommentsURL should be set from <comments>")
		}
		if item.Link != "" && item.CommentsURL != "" && item.Link == item.CommentsURL {
			t.Errorf("Link and CommentsURL are the same: %s", item.Link)
		}
	}
}

func TestParse_noDate(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "no-date.xml"))
	if err != nil {
		t.Fatal(err)
	}
	items, err := feed.Parse(data, src)
	if err != nil {
		t.Fatal(err)
	}

	for _, item := range items {
		if item.Published != nil {
			t.Errorf("expected Published == nil, got %v", *item.Published)
		}
	}
}

func TestParse_entities(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "entities.xml"))
	if err != nil {
		t.Fatal(err)
	}
	items, err := feed.Parse(data, src)
	if err != nil {
		t.Fatal(err)
	}

	title := items[0].Title
	if strings.Contains(title, "&amp;") {
		t.Errorf("Title still contains &amp;: %s", title)
	}
	if !strings.Contains(title, "C++ & Rust") {
		t.Errorf("expected decoded '&', got: %s", title)
	}
}

func TestExcerpt_wordBoundary(t *testing.T) {
	// Build a long content (>1500 chars) to test truncation.
	longContent := strings.Repeat("Lorem ipsum dolor sit amet. ", 200)
	item := feed.ParsedItem{
		Title:   "Test",
		Content: "<p>" + longContent + "</p>",
	}

	ex := feed.Excerpt(item)
	if len(ex) > 1500 {
		t.Errorf("excerpt %d chars exceeds 1500", len(ex))
	}
	// If truncated, should end with "…" and the char before it should be a non-space
	// (the last character of the word at the boundary). The truncation point is a space.
	if strings.HasSuffix(ex, "…") {
		cut := strings.TrimSuffix(ex, "…")
		r, _ := utf8.DecodeLastRuneInString(cut)
		if r == ' ' {
			t.Error("excerpt ends with space before ellipsis — should cut at word boundary")
		}
	}
	if !utf8.ValidString(ex) {
		t.Error("excerpt is not valid UTF-8")
	}
}

func TestBodyHash_stability(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "rss2-basic.xml"))
	if err != nil {
		t.Fatal(err)
	}
	items1, err := feed.Parse(data, src)
	if err != nil {
		t.Fatal(err)
	}

	// Parse again — hash must be identical.
	items2, err := feed.Parse(data, src)
	if err != nil {
		t.Fatal(err)
	}

	for i := range items1 {
		h1 := feed.BodyHash(items1[i])
		h2 := feed.BodyHash(items2[i])
		if h1 != h2 {
			t.Errorf("item %d: hashes differ: %s vs %s", i, h1, h2)
		}
	}

	// Tamper with content — hash must change.
	items2[0].Content = items2[0].Content + "x"
	hSame := feed.BodyHash(items1[0])
	hDiff := feed.BodyHash(items2[0])
	if hSame == hDiff {
		t.Error("BodyHash should differ when content changes")
	}
}

func TestBodyHash_correctness(t *testing.T) {
	item := feed.ParsedItem{
		Title:   "Test",
		Content: "<p>Hello world</p>",
	}
	h := feed.BodyHash(item)
	if len(h) != 64 {
		t.Fatalf("expected 64-char hex SHA-256, got %d chars", len(h))
	}

	// Verify independently.
	expected := sha256.Sum256([]byte("Hello world"))
	want := hex.EncodeToString(expected[:])
	if h != want {
		t.Errorf("BodyHash mismatch: got %s, want %s", h, want)
	}
}

func TestExcerpt_categoriesOmittedWhenEmpty(t *testing.T) {
	item := feed.ParsedItem{
		Title:   "Test Title",
		Content: "Some content here.",
	}
	ex := feed.Excerpt(item)
	lines := strings.Split(ex, "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least title + content lines, got %d", len(lines))
	}
	// No categories line between title and content.
	if lines[1] != "Some content here." {
		t.Errorf("second line should be content, got: %q", lines[1])
	}
}

func TestExcerpt_categoriesIncludedWhenPresent(t *testing.T) {
	item := feed.ParsedItem{
		Title:      "Test Title",
		Content:    "Some content here.",
		Categories: []string{"Go", "Systems"},
	}
	ex := feed.Excerpt(item)
	lines := strings.Split(ex, "\n")
	if len(lines) < 3 {
		t.Fatalf("expected title + categories + content lines, got %d", len(lines))
	}
	if lines[1] != "Go, Systems" {
		t.Errorf("categories line: got %q, want %q", lines[1], "Go, Systems")
	}
}

func TestParse_property_utf8AllExcerpts(t *testing.T) {
	fixtures := []string{
		"rss2-basic.xml", "atom-basic.xml", "hn.xml", "reddit.xml",
		"truncated.xml", "no-date.xml", "entities.xml",
	}
	for _, f := range fixtures {
		data, err := os.ReadFile(filepath.Join("testdata", f))
		if err != nil {
			t.Fatal(err)
		}
		items, err := feed.Parse(data, src)
		if err != nil {
			t.Fatalf("%s: parse: %v", f, err)
		}
		for i, item := range items {
			if !utf8.ValidString(feed.Excerpt(item)) {
				t.Errorf("%s item %d: excerpt not valid UTF-8", f, i)
			}
		}
	}
}

func TestParse_publishedPrecedence(t *testing.T) {
	// Use Atom — gofeed populates UpdatedParsed for Atom entries.
	const xml = `<?xml version="1.0"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>T</title>
  <entry>
    <title>Parsed First</title><link href="https://example.com/1"/>
    <id>https://example.com/1</id>
    <published>2025-09-01T12:00:00Z</published>
    <updated>2025-09-02T12:00:00Z</updated>
  </entry>
  <entry>
    <title>Updated Only</title><link href="https://example.com/2"/>
    <id>https://example.com/2</id>
    <updated>2025-09-03T12:00:00Z</updated>
  </entry>
</feed>`

	items, err := feed.Parse([]byte(xml), src)
	if err != nil {
		t.Fatal(err)
	}

	// PublishedParsed should take precedence over UpdatedParsed.
	if items[0].Published == nil {
		t.Fatal("first item: expected Published from published")
	}
	expected := time.Date(2025, 9, 1, 12, 0, 0, 0, time.UTC)
	if !items[0].Published.Equal(expected) {
		t.Errorf("first item: got %v, want %v", *items[0].Published, expected)
	}

	// Second item has no published — UpdatedParsed should be used.
	if items[1].Published == nil {
		t.Fatal("second item: expected Published from updated")
	}
	expected2 := time.Date(2025, 9, 3, 12, 0, 0, 0, time.UTC)
	if !items[1].Published.Equal(expected2) {
		t.Errorf("second item: got %v, want %v", *items[1].Published, expected2)
	}
}

func TestParse_contentPrecedence(t *testing.T) {
	const xml = `<?xml version="1.0"?>
<rss version="2.0">
  <channel><title>T</title>
  <item><title>Covered</title><link>https://example.com/1</link>
    <content:encoded><![CDATA[Full content]]></content:encoded>
    <description>Short desc</description>
  </item>
  <item><title>Description Only</title><link>https://example.com/2</link>
    <description>Only description available</description>
  </item>
  </channel>
</rss>`

	items, err := feed.Parse([]byte(xml), src)
	if err != nil {
		t.Fatal(err)
	}

	if items[0].Content != "Full content" {
		t.Errorf("content precedence: got %q, want %q", items[0].Content, "Full content")
	}
	if items[1].Content != "Only description available" {
		t.Errorf("description fallback: got %q, want %q", items[1].Content, "Only description available")
	}
}

func TestParse_atomContentHTML(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "atom-basic.xml"))
	if err != nil {
		t.Fatal(err)
	}
	items, err := feed.Parse(data, src)
	if err != nil {
		t.Fatal(err)
	}

	// First entry has content type="html" — Content field should have raw HTML.
	if !strings.Contains(items[0].Content, "CSS Grid") {
		t.Errorf("Atom content: expected 'CSS Grid' in Content, got %q", items[0].Content)
	}
}
