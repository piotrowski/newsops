# T006 — Feed parser + excerpt builder

**Depends on:** T005
**Size:** M
**Touches:** `internal/feed/parser.go`, `internal/feed/excerpt.go`, `internal/feed/testdata/`

## Why

`body_excerpt` is what the agent actually reads when it skims 40 candidates.
If it contains raw HTML, navigation boilerplate, or a truncated
"Continue reading →", the agent's judgment degrades and no amount of prompting
fixes it. This ticket is upstream of the quality of every digest.

Pure parsing only: bytes in, structs out. No HTTP, so it is fully testable
against fixtures.

## Interface

```go
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
func Parse(body []byte, src config.Source) ([]ParsedItem, error)

// Excerpt builds the ~1500-char string the agent skims.
func Excerpt(p ParsedItem) string

// BodyHash is the dedupe/change-detection hash of the normalized content.
func BodyHash(p ParsedItem) string
```

Dependency: `github.com/mmcdole/gofeed`.

**Field precedence.** Content: `item.Content` → `item.Description` → "".
Published: `item.PublishedParsed` → `item.UpdatedParsed` → nil. Never
substitute "now" for a missing date — `published_at` is nullable precisely so
that a missing date stays visibly missing rather than becoming a lie the
prefilter then sorts on.

**Comments URL.** Take `item.Custom["comments"]` (RSS `<comments>`) when
present; Atom feeds may carry it as a `<link rel="replies">`. Populate it for
every feed, not just known aggregators — T010 decides what to do with it.

**Excerpt construction:**

1. Strip HTML tags from `Content`; decode entities; drop `<script>`/`<style>`
   content entirely.
2. Collapse all whitespace runs to single spaces; trim.
3. Compose `Title + "\n" + strings.Join(Categories, ", ") + "\n" + text`,
   omitting the categories line when there are none.
4. Truncate to 1500 characters **at a word boundary, on a rune boundary**,
   appending "…" when truncated. Never split a multi-byte rune.

**BodyHash:** SHA-256 of the whitespace-normalized, tag-stripped content
(step 2 output), hex-encoded. Same content from two feeds must produce the
same hash.

## Fixtures

Save real captured bodies under `internal/feed/testdata/`:

| File | Covers |
|---|---|
| `rss2-basic.xml` | Ordinary RSS 2.0 with full content |
| `atom-basic.xml` | Atom with `<content type="html">` |
| `hn.xml` | hnrss — `<link>` is the article, `<comments>` the thread |
| `reddit.xml` | Reddit `.rss` — content is an HTML blob with an outbound link |
| `truncated.xml` | Description ends in "Continue reading →" |
| `no-date.xml` | Items with no `pubDate` at all |
| `entities.xml` | Titles with `&amp;`, `&#8217;`, and CDATA |

## Acceptance criteria

- [ ] All seven fixtures parse without error and yield ≥1 item each.
- [ ] Categories are extracted from both RSS `<category>` and Atom
      `<category term=...>`.
- [ ] `hn.xml` yields `Link` = the article URL and `CommentsURL` = the HN
      thread; they are different.
- [ ] `no-date.xml` yields `Published == nil`, not a zero time and not now.
- [ ] `entities.xml` yields decoded titles — no `&amp;` survives into `Title`.
- [ ] Excerpts contain no `<` or `>` characters and no runs of whitespace.
- [ ] An excerpt from a 50KB body is ≤1500 characters and ends at a word
      boundary; the string is valid UTF-8.
- [ ] `BodyHash` is stable across two parses of the same fixture and differs
      when one character of body text changes.

## Tests

Table-driven over the fixtures, asserting the criteria above. Add one
property-style test: for every fixture item, `utf8.ValidString(Excerpt(p))`
is true.

## Out of scope

HTTP, caching, conditional requests (T007). The readability fallback for
truncated feeds — T007 owns it, since it needs to fetch the article page.
