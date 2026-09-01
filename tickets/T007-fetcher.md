# T007 — Concurrent fetcher + raw cache

**Depends on:** T006, T004
**Size:** L
**Touches:** `internal/feed/fetcher.go`, `internal/feed/cache.go`

## Why

Fifty feeds fetched serially is slow enough to be annoying in a conversation;
fetched rudely, it gets you rate-limited or blocked. Conditional requests mean
the ~60% of feeds that haven't changed cost one round trip and no parsing.
Caching raw bodies means you can re-run dedupe and prefilter changes all
afternoon without touching the network — which you will, while tuning.

## Interface

```go
type Result struct {
    Source      config.Source
    Items       []ParsedItem
    NotModified bool   // 304
    RawPath     string // "" when not modified or on error
    HTTPStatus  int
    Err         error  // per-source; never aborts the batch
}

type Fetcher struct {
    Client    *http.Client
    UserAgent string
    Limit     int           // concurrent requests, default 10
    PerHost   time.Duration // min gap between requests to one host, default 1s
    CacheDir  string
}

// Fetch returns one Result per source, in input order. The returned error is
// non-nil only for a whole-batch failure (context cancelled).
func (f *Fetcher) Fetch(ctx context.Context, srcs []config.Source, state StateStore) ([]Result, error)

// StateStore reads and writes the feed_state table (T002).
type StateStore interface {
    Get(ctx context.Context, source string) (etag, lastModified string, err error)
    Put(ctx context.Context, source, url, etag, lastModified string, status int, errMsg string) error
}
```

**Requirements:**

- `errgroup` with `SetLimit(10)`. One slow feed must not block the rest.
- **Per-host rate limit**, keyed by hostname, minimum 1s between requests.
  Several sources share hosts, and arXiv is strict about this.
- **`User-Agent: newsops/<version> (+personal feed reader)`.** Reddit and
  several CDNs 429 the default Go agent. This is not optional.
- Send `If-None-Match` from the stored ETag and `If-Modified-Since` from the
  stored `Last-Modified`. A 304 sets `NotModified`, returns no items, and
  writes nothing to the cache.
- Per-request timeout 20s; honour the caller's context throughout.
- Follow up to 3 redirects; record the final status.
- **A failing source is a `Result.Err`, never a returned error.** One dead
  feed must not cost you the other fifty.
- Always call `state.Put` — including on failure, so `last_error` and
  `last_status` are visible in the archive.

**Cache:** write the raw body to
`<CacheDir>/<sha1(source.URL)>/<YYYY-MM-DD>.xml`, creating directories as
needed, and set `RawPath`. If the file already exists for today, overwrite it.
Cache writes are best-effort: a failure logs to stderr and does not fail the
fetch.

**Readability fallback.** When a parsed item's `Content` is shorter than 400
characters *and* the source is tier 1 or 2, fetch the article URL and extract
with `github.com/go-shiori/go-readability`, replacing `Content`. Cap this at
10 such fetches per run — it is a fallback, not a crawler — and skip it
entirely when the source is an aggregator (the link points at someone else's
site, and T010 will resolve it anyway).

## Acceptance criteria

- [ ] 50 fixture sources fetch concurrently with no more than 10 in flight.
- [ ] Two sources on the same host are separated by ≥1s.
- [ ] A 304 response yields `NotModified: true`, zero items, no cache file.
- [ ] A 200 response stores the new ETag and `Last-Modified` via `StateStore`.
- [ ] A 500, a connection refusal, and a body that fails to parse each yield a
      `Result.Err` while every other source still succeeds.
- [ ] `User-Agent` is present on every request.
- [ ] Cancelling the context mid-batch returns promptly.
- [ ] The readability fallback fires for a short-content tier-1 item and does
      not fire for an aggregator.
- [ ] Cache files land at the documented path and contain the exact bytes
      received.

## Tests

`httptest` servers throughout — no network in tests:

- concurrency ceiling (a counting handler asserting max in-flight ≤10)
- per-host spacing (timestamps on two same-host requests)
- 304 path, 200-with-ETag path, 500 path, malformed-body path
- header assertions for UA / `If-None-Match` / `If-Modified-Since`
- cache file written with correct bytes and path

## Out of scope

Inserting into the database, the collection window, `--max-age` (all T008).
Whether a source list entry is valid (T009).
