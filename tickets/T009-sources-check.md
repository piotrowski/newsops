# T009 — `sources:check` subcommand

**Depends on:** T007
**Size:** S
**Touches:** `cmd/newsops/sourcescheck.go`

## Why

The source list is unverified and feed paths drift constantly. Without this,
a dead feed shows up as "the digest feels thin lately" — a symptom you will
misattribute to the prefilter, the agent, or your own interests file. Twenty
lines of code that turn a silent failure into a table.

Build it early and run it before the first real fetch. Then monthly.

## Behaviour

GET every source. A source **passes** when the response is 200 and `gofeed`
parses at least one item. No database writes, no cache writes, no ETag use —
this is a fresh unconditional check every time.

Concurrency 10, per-request timeout 15s, same `User-Agent` as T007.

Opens the database `ReadOnly`, or not at all — this command must work before
any database exists.

## Interface

```
newsops sources:check [--json] [--source "Name"]
```

Human output is a table:

```
PASS  Hacker News          200   30 items
PASS  Cloudflare           200   20 items
FAIL  Temporal             404   —
FAIL  Databricks           200    0 items   (parsed, empty)
FAIL  Discord Engineering  —     —          (dial tcp: no such host)

48/51 passed
```

JSON:

```json
{
  "total": 51, "passed": 48, "failed": 3,
  "results": [
    {"name": "Hacker News", "url": "https://hnrss.org/...", "status": "ok",
     "http_status": 200, "items": 30, "error": null},
    {"name": "Temporal", "url": "https://temporal.io/blog/rss.xml",
     "status": "http_error", "http_status": 404, "items": 0, "error": "http 404"},
    {"name": "Databricks", "url": "https://www.databricks.com/feed",
     "status": "empty", "http_status": 200, "items": 0, "error": "parsed 0 items"}
  ]
}
```

`status` is one of `ok` · `http_error` · `parse_error` · `empty` ·
`network_error`. Distinguishing them matters: a 404 means the path moved, an
`empty` means the path is fine but the feed is broken or seasonal, and a
`parse_error` usually means the URL now returns HTML instead of a feed.

**Exit codes.** 0 when all pass, 1 when any fail, 2 on bad arguments or an
unreadable config.

## Acceptance criteria

- [ ] Runs successfully with no `data/` directory present.
- [ ] Each of the five `status` values is produced by the corresponding
      `httptest` handler.
- [ ] Exit 1 when any source fails, 0 when all pass.
- [ ] No files are created anywhere — assert `data/` and the cache directory
      do not exist afterward.
- [ ] Sends the descriptive `User-Agent`.
- [ ] `--source "Name"` checks exactly one and still reports totals of 1.
- [ ] Completes 51 sources in under 30s wall clock (concurrency actually on).

## Tests

`httptest` servers for each status class, plus a no-such-host case for
`network_error`. Assert the JSON shape exactly, and assert no filesystem
writes.

## Out of scope

Fixing the URLs it finds broken. Scheduling it — you run it by hand, or the
agent does when the digest looks thin.
