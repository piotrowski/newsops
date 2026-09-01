# T016 — `query` — FTS5 archive search

**Depends on:** T002, T003
**Size:** M
**Touches:** `cmd/newsops/query.go`, `internal/store/query.go`

## Why

This is the capability a chat window does not have, and the reason an agent
earns its place in this system at all.

*"This vendor had an outage three weeks ago and shipped a related fix last
week"* is not something you can paste into a prompt, because you don't know
it's there — that's why you're asking. It is also what makes a post worth
reading rather than another summary of the same press release. Every brief
(T020) calls this.

## Interface

```
newsops query [--json] [--text "..."] [--domain example.com]
              [--story N --related] [--days 90] [--limit 20]
```

At least one of `--text`, `--domain`, or `--story --related` is required;
they combine with AND when several are given.

```json
{
  "query": {"text": "cloudflare outage", "days": 90, "limit": 20},
  "total": 7,
  "results": [
    {
      "story_id": 3907,
      "title": "Cloudflare edge outage affects 12% of requests",
      "url": "https://blog.cloudflare.com/2026/07/incident",
      "source": "Cloudflare",
      "published_at": "2026-07-14T09:20:00Z",
      "first_seen": "2026-07-14T10:02:00Z",
      "source_count": 6,
      "snippet": "...began returning <b>522</b>s during the <b>outage</b> window..."
    }
  ]
}
```

Results are **stories**, not items — the agent addresses stories everywhere
else, and returning items would leak a different ID space into briefs. Match
against any member item, return the story once, ranked by FTS5 `rank`.

Use FTS5 `snippet(items_fts, ...)` for the excerpt with `<b>` delimiters, 30
tokens of context.

Opens `ReadOnly`, no lock — briefs are written while nothing else is running,
but there is no reason to block.

### `--related`

Builds a query from the story itself, for "what else do we know about this":

1. Take the canonical item's normalized title (reuse `NormalizeTitle` from
   T011), drop stopwords, keep the 5 longest remaining tokens.
2. OR them together in FTS syntax.
3. Also OR in the canonical URL's host as a domain filter.
4. Exclude the subject story from results.

This is deliberately crude. It is a recall tool for an agent that will read
the results and judge them, not a precision ranker.

### Input safety

User text reaches FTS5's query parser, where a stray `"` or `*` is a syntax
error. Tokenize the input on whitespace, strip FTS operators, wrap each token
in double quotes, and join with `OR` (or `AND` for multi-word phrases — pick
one and document it). A malformed input must return zero results, never an
error mentioning SQL.

## Acceptance criteria

- [ ] `--text "outage"` over a seeded archive returns the seeded stories with
      snippets containing `<b>outage</b>`.
- [ ] Each story appears **once**, even when three of its items match.
- [ ] `--domain cloudflare.com` matches `blog.cloudflare.com` (subdomain) and
      not `notcloudflare.com`.
- [ ] `--days 30` excludes a story seeded 60 days ago; `--days 90` includes it.
- [ ] `--story N --related` never returns story N itself.
- [ ] `--story N --related` on a story with no archive neighbours returns
      `{"total": 0, "results": []}` and exit 0 — "nothing found" is a real,
      useful answer here.
- [ ] Input `cloudflare" OR 1=1 --` returns 0 results and exit 0, with no SQL
      error text anywhere in the output.
- [ ] Passing none of the three query flags exits 2 with a message naming them.
- [ ] `--limit` caps results; default is 20.
- [ ] Completes in under 200ms over a 10k-item archive.

## Tests

Seeded in-memory archive of ~50 items across ~30 stories, spanning 120 days:

- text match, dedupe-to-one-story, domain match, subdomain edge case
- day-window boundaries
- `--related` including the self-exclusion and the empty case
- the injection-shaped input
- a benchmark or timing assertion for the 10k case

## Out of scope

How the agent uses the results — that is `modes/brief.md` (T020).
