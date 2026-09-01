# T004 — Run bookkeeping (`runstate`)

**Depends on:** T003
**Size:** S
**Touches:** `internal/runstate/runstate.go`

## Why

There is no cron here, so nothing implicitly records "when did this last
run". `status` and every `--max-age` guard read from this table; without it
the agent cannot tell fresh data from stale, which is the first question it
asks every session.

## Interface

```go
type Stage string
const (
    StageFetch      Stage = "fetch"
    StageDedupe     Stage = "dedupe"
    StageCandidates Stage = "candidates"
)

type Counts struct{ In, Out int }

type Run struct { /* id, stage, started */ }

// Begin inserts a row with started_at set and ended_at null.
func Begin(ctx context.Context, db *store.DB, s Stage) (*Run, error)

// End fills ended_at, ok, counts and note. Safe to call once only.
func (r *Run) End(ctx context.Context, ok bool, c Counts, note string) error

// LastSuccess returns the started_at of the most recent run with ok = 1.
// found is false when the stage has never succeeded.
func LastSuccess(ctx context.Context, db *store.DB, s Stage) (t time.Time, found bool, err error)

// Age is a helper: time.Since(LastSuccess), and stale when not found.
func Stale(ctx context.Context, db *store.DB, s Stage, maxAge time.Duration) (bool, error)
```

`LastSuccess` returns `started_at`, not `ended_at` — the window for the next
fetch is "everything since we last began collecting", and using `ended_at`
would silently drop anything published during a long run.

A crashed run leaves `ended_at` null and `ok` null. That row must never be
treated as a success, and it must not break `LastSuccess`.

## Acceptance criteria

- [ ] `Begin` writes a row with `started_at` set, `ended_at` and `ok` null.
- [ ] `End(true, ...)` fills `ended_at`, `ok = 1`, both counts and the note.
- [ ] `End(false, ...)` records `ok = 0`; `LastSuccess` ignores that row.
- [ ] `LastSuccess` ignores rows with null `ok` (a crashed run).
- [ ] `LastSuccess` on a stage that has never run returns `found = false` and
      no error — never a zero time that a caller might mistake for 1970.
- [ ] `Stale` returns true when `found` is false, regardless of `maxAge`.

## Tests

In-memory database:

- never-run → `found = false`, `Stale` true
- one successful run → `found = true`, correct timestamp
- a failed run after a successful one → `LastSuccess` still returns the
  successful one
- a crashed run (`Begin` with no `End`) → ignored

## Out of scope

The `status` command (T013). The `--max-age` flag plumbing — each subcommand
ticket owns its own.
