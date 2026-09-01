# T008 — `fetch` subcommand

**Depends on:** T007, T003
**Size:** M
**Touches:** `cmd/newsops/fetch.go`

## Why

This is where the collection window is decided, and the window is the
difference between a tool that works when you use it twice a week and one
that only works if you use it daily. There is no cron: whatever the gap since
you last ran it, `fetch` has to do the right thing.

## Behaviour

**Window.** Start from `runstate.LastSuccess(fetch)` — the `started_at` of the
last successful run. If there is none, or it is older than
`max_backfill_days` (default 30), use `now - max_backfill_days`. Items whose
`published_at` is older than the window start are dropped; items with a null
`published_at` are kept (a missing date must not silently exclude a real
item — that is a feed's failing, not the item's).

**`--max-age` guard.** When the last successful fetch is younger than
`--max-age`, do nothing and exit 0 with
`{"skipped": true, "reason": "fresh", "last_success": "..."}`. This makes
"just run it" safe: the agent never has to reason about whether it already
ran.

**Insert.** For each item: canonical URL is left empty at this stage (T012
fills it), `story_id` null, `first_seen` now. Insert with
`ON CONFLICT(url) DO NOTHING` — re-seeing an item is the normal case, not an
error. Count inserted rows as `new`.

Opens the database `ReadWrite` (takes the lock).

## Interface

```
newsops fetch [--json] [--max-age 6h] [--since 2026-08-01T00:00:00Z]
              [--source "Hacker News"] [--no-cache]
```

`--since` overrides the computed window. `--source` restricts to one source by
name (debugging). `--no-cache` skips raw-body writes.

Output:

```json
{
  "skipped": false,
  "window": {"since": "2026-08-29T06:55:00Z", "backfilled": false},
  "sources": {"total": 51, "ok": 48, "not_modified": 30, "failed": 3},
  "items": {"seen": 312, "new": 287, "dropped_old": 25},
  "failures": [
    {"source": "Temporal", "error": "http 404"},
    {"source": "Databricks", "error": "parse: unexpected EOF"}
  ]
}
```

**Exit codes.** 0 when every source succeeded or the run was skipped. 1 when
at least one source failed and at least one succeeded — a partial fetch is
still worth having. 2 when every source failed, or the database is locked
(`{"error": "locked", ...}`), or arguments are invalid.

The run is recorded via `runstate.Begin/End` with `items_in` = seen,
`items_out` = new, and `ok` = true for exit 0 **and** exit 1 — a partial fetch
still advances the window, otherwise one permanently-broken feed would make
every subsequent run re-scan from the same point forever.

## Acceptance criteria

- [ ] First run on an empty database backfills `max_backfill_days` and reports
      `"backfilled": true`.
- [ ] Second run immediately after, with `--max-age 6h`, exits 0 with
      `{"skipped": true}` and makes zero HTTP requests.
- [ ] Same run without `--max-age` fetches, and every source returns 304 (all
      ETags stored), inserting 0 new items.
- [ ] An item already in the database by URL is not duplicated and not counted
      as new.
- [ ] An item with `published_at` older than the window is dropped and counted
      in `dropped_old`; an item with a null date is kept.
- [ ] With 3 of 51 sources failing, exit code is 1, `failures` has 3 entries,
      and the other 48 are stored.
- [ ] With all sources failing, exit code is 2.
- [ ] A second concurrent `fetch` exits 2 with `{"error": "locked"}` in under
      100ms.
- [ ] `--source "Hacker News"` fetches exactly one feed.

## Tests

`httptest` servers plus an in-memory database:

- window computation: no prior run, recent prior run, ancient prior run
- `--max-age` skip path asserts zero requests via a counting handler
- duplicate-URL insert is a no-op
- exit code mapping for all-ok / partial / all-failed
- `runstate` row written with correct counts, and `ok = true` on partial

## Out of scope

Canonicalization and story assignment (T010–T012). The chained command (T015).
