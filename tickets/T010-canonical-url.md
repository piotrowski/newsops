# T010 — URL canonicalization + aggregator resolution

**Depends on:** T005
**Size:** M
**Touches:** `internal/dedupe/canonical.go`

## Why

This is the highest-leverage code in the project. The source list is dominated
by primary sources that don't republish each other; the duplication that does
exist is mostly aggregators pointing at an original. HN, Lobsters, Reddit and
Techmeme all carry the real URL in the feed — so canonicalizing that URL
dedupes them **exactly**, for free, with no threshold to tune and no
embeddings anywhere.

Pure functions over strings, except one optional network hop. That makes them
cheap to test exhaustively, which matters because their failures are silent:
a canonicalization bug shows up as duplicate digest entries weeks later.

## Interface

```go
// Canonical normalizes a URL for equality comparison.
func Canonical(rawURL string) (string, error)

// Resolver follows one redirect hop for known shorteners. Injected so tests
// never touch the network; pass nil to disable resolution entirely.
type Resolver interface {
    Resolve(ctx context.Context, url string) (string, error)
}

type Link struct {
    URL  string
    Kind string // "article" | "discussion"
}

// ResolveAggregator returns the article link and, when the source is a known
// aggregator, the discussion link. For a non-aggregator, discussion is nil
// and article is the item's own link.
func ResolveAggregator(p feed.ParsedItem, src config.Source) (article Link, discussion *Link)
```

### Canonicalization rules

Applied in order:

1. Parse; reject non-http(s) with an error.
2. Lowercase the scheme and host. Strip a leading `www.`.
3. Drop the fragment entirely.
4. Remove tracking parameters: any key matching `utm_*`, plus `ref`,
   `ref_src`, `source`, `fbclid`, `gclid`, `mc_cid`, `mc_eid`, `igshid`,
   `_hsenc`, `_hsmi`, `spm`, `at_medium`, `at_campaign`.
5. Keep all other query parameters, but **sort them by key** so
   `?b=2&a=1` and `?a=1&b=2` canonicalize identically.
6. Strip a trailing slash, except when the path is exactly `/`.
7. Drop a default port (`:80` on http, `:443` on https).

Do **not** lowercase the path — many sites serve case-sensitive paths, and
folding case there creates false merges.

### Shortener resolution

Known hosts: `bit.ly`, `t.co`, `buff.ly`, `lnkd.in`, `ow.ly`, `tinyurl.com`,
`goo.gl`, `dlvr.it`, `trib.al`. For these only, follow **one** hop via
`Resolver` (a `HEAD`, falling back to `GET` when HEAD is refused) and
canonicalize the target. On any error, fall back to canonicalizing the
original — a failed resolution must never fail the item.

### Aggregator mapping

| Source name | Article URL | Discussion URL |
|---|---|---|
| `Hacker News` | `p.Link` | `p.CommentsURL` |
| `Lobsters` | `p.Link` | `p.CommentsURL` |
| `r/programming`, `r/MachineLearning` | first outbound `<a href>` in content whose host differs from `reddit.com` | `p.Link` |
| `Techmeme` | `p.Link` (already the source article) | none |

A Reddit self-post, an Ask HN, or any aggregator item with no outbound link is
a normal item of its own: article = `p.Link`, discussion = nil.

## Acceptance criteria

- [ ] `https://ex.com/a?utm_source=x&id=7#frag` → `https://ex.com/a?id=7`
- [ ] `https://WWW.Ex.com/A/` → `https://ex.com/A` (path case preserved)
- [ ] `https://ex.com:443/a` → `https://ex.com/a`
- [ ] `?b=2&a=1` and `?a=1&b=2` produce the identical string
- [ ] `https://ex.com/` keeps its trailing slash
- [ ] A `bit.ly` link resolves through a stub `Resolver`; a resolver error
      falls back to the canonicalized short URL without an error
- [ ] A non-shortener host is never sent to the `Resolver`
- [ ] An HN item yields article = the blog URL, discussion = the HN thread,
      and the two differ
- [ ] A Reddit link post yields the outbound article and the Reddit thread as
      discussion; a Reddit self-post yields no discussion
- [ ] `mailto:` and `ftp://` return an error rather than a mangled string

## Tests

One table test of ~25 canonicalization cases covering every rule above and
each acceptance line. One table test for `ResolveAggregator` over the four
aggregator shapes plus two non-aggregators, using the T006 fixtures. A stub
`Resolver` — no network in tests.

## Out of scope

Deciding which items belong to the same story (T012). Title comparison (T011).
