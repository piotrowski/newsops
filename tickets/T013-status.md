# T013 — `status` subcommand

**Depends on:** T004
**Size:** S
**Touches:** `cmd/newsops/status.go`

## Why

This is the first call the agent makes in every session, and it decides
whether to collect. It must be fast, read-only, network-free, and — critically
— **it must work on a database that has never been written to**. An agent that
has to special-case a first run will get it wrong, and the failure looks like
"the tool is broken" on someone's first five minutes with the system.

## Behaviour

Opens the database `ReadOnly`: **takes no lock**, so it works while a
`collect` is running. That is exactly when you want to check on it. If
`data/news.db` does not exist, create nothing — report the never-run shape.

`stale` is true when the stage has never succeeded, or its last success is
older than `--max-age` (default 6h).

Counts:
- `items_total` — all rows in `items`.
- `new_since_fetch` — items with `first_seen >= started_at` of the last
  successful fetch; 0 when there is none.
- `unshown_stories` — stories with `shown_at IS NULL` and `last_seen` within
  the last 7 days. This is the number that tells the agent whether there is
  anything to look at.

`digest` reports the path and the file's mtime, or null when absent.

## Interface

```
newsops status [--json] [--max-age 6h]
```

Populated:

```json
{
  "stages": {
    "fetch":      {"last_success": "2026-08-30T07:12:00Z", "age_min": 143, "stale": false},
    "dedupe":     {"last_success": "2026-08-30T07:13:00Z", "age_min": 142, "stale": false},
    "candidates": {"last_success": "2026-08-30T07:13:00Z", "age_min": 142, "stale": false}
  },
  "counts": {"items_total": 4102, "new_since_fetch": 287, "unshown_stories": 12},
  "digest": {"path": "digest.md", "generated_at": "2026-08-30T07:20:00Z"}
}
```

Virgin database — **identical shape**, never a missing key and never an error:

```json
{
  "stages": {
    "fetch":      {"last_success": null, "age_min": null, "stale": true},
    "dedupe":     {"last_success": null, "age_min": null, "stale": true},
    "candidates": {"last_success": null, "age_min": null, "stale": true}
  },
  "counts": {"items_total": 0, "new_since_fetch": 0, "unshown_stories": 0},
  "digest": {"path": "digest.md", "generated_at": null}
}
```

Exit 0 in both cases. Exit 2 only on a corrupt or unreadable database.

## Acceptance criteria

- [x] On a directory with no `data/` at all: valid JSON, every `stale` true,
      exit 0, and **no files created** — assert `data/` still does not exist.
- [x] `newsops status --json | jq .stages.fetch.stale` prints `true` (a JSON
      boolean, not a string, not null).
- [x] Succeeds while another process holds the write lock.
- [x] `age_min` is an integer count of minutes, and null exactly when
      `last_success` is null.
- [x] `--max-age 1h` flips `stale` to true for a stage that succeeded 2h ago.
- [x] `unshown_stories` excludes stories with `shown_at` set and stories older
      than 7 days.
- [x] Completes in under 100ms on a database with 10k items.

## Tests
In-memory and on-disk fixtures — 9 tests total:

- TestStatus_VirginShape — virgin database shape, asserted key by key against the JSON above
- TestStatus_NeverRunShape — exact JSON shape for never-run DB (all nulls, booleans)
- TestStatus_JsonOutput — populated shape with seeded `runs` table
- TestStatus_StaleBoundary — `stale` boundary at exactly `--max-age`
- TestStatus_AgeMin — age_min is integer when last_success exists, null otherwise
- TestStatus_ConcurrentRead — concurrent read while a write lock is held
- TestStatus_UnshownStories — excludes shown and old stories
- TestStatus_NewSinceFetch — counts items since last fetch
- TestStatus_Perf — completes in under 100ms on 10k items
- TestStatus_DigestFile — digest.md detection


## Completed

**Files created:** `cmd/newsops/status.go`
**Files modified:** `cmd/newsops/main.go` (removed stub), `cmd/newsops/main_test.go` (updated integration test)

**Tests:** 10 tests in `cmd/newsops/status_test.go` covering all acceptance criteria. Full suite: 71 tests across 4 packages, all pass.
## Out of scope

Deciding what to do about staleness — that is the agent's freshness protocol
(T018).
