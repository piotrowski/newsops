# T003 — DB open, WAL, transactions, process lock

**Depends on:** T002
**Size:** M
**Touches:** `internal/store/db.go`, `internal/store/lock.go`

## Why

Two agent sessions in two terminals will eventually run `collect` at the same
time. The failure has to be immediate and legible, not a hang — a
conversational tool that blocks indefinitely is worse than one that errors,
because the human is sitting there watching it.

**Lock `data/news.lock`, not `data/news.db`.** SQLite manages its own locking
on the database file; an advisory lock on top of it is redundant at best and
interacts badly with WAL at worst.

**Read-only commands take no lock.** `status`, `query` and `show` must work
while a `collect` is running — that is exactly when you want to check on it.
WAL allows concurrent readers.

## Interface

```go
type DB struct {
    *sql.DB
    lock *os.File // nil for read-only opens
}

type Mode int
const (
    ReadOnly Mode = iota // no lock taken
    ReadWrite            // exclusive, non-blocking lock on data/news.lock
)

// Open creates dir if needed, opens data/news.db, applies pragmas, runs
// Migrate, and for ReadWrite takes the lock.
func Open(ctx context.Context, dir string, mode Mode) (*DB, error)

func (db *DB) Close() error

// Tx runs fn in a transaction, rolling back on error or panic.
func (db *DB) Tx(ctx context.Context, fn func(*sql.Tx) error) error

// LockedError is returned when another process holds the write lock.
type LockedError struct {
    PID        int
    HeldSince  time.Time
}
```

Pragmas on open: `journal_mode = WAL`, `foreign_keys = ON`,
`busy_timeout = 5000`, `synchronous = NORMAL`.

Lock implementation: `syscall.Flock(fd, LOCK_EX|LOCK_NB)` on
`data/news.lock`. On acquiring, truncate and write `<pid> <RFC3339 start>` so
the error message can say who holds it. On contention, read that line and
return `LockedError`. The OS releases `flock` when a process dies, so a stale
lock from a crashed run needs no cleanup — do **not** write pid-file logic
that second-guesses this.

The CLI maps `LockedError` to
`{"error": "locked", "message": "...", "pid": 4711, "held_since": "..."}`,
exit 2.

## Acceptance criteria

- [ ] `Open(ReadWrite)` on a fresh directory creates `data/`, the database,
      the lock file, and applies migrations.
- [ ] A second `Open(ReadWrite)` while the first is held fails in under 100ms
      with `LockedError` carrying the holder's pid and start time.
- [ ] `Open(ReadOnly)` succeeds while a write lock is held.
- [ ] Killing the lock holder (SIGKILL) leaves the next `Open(ReadWrite)` able
      to acquire immediately.
- [ ] `Tx` rolls back on a returned error and on a panic (re-panicking after
      rollback).
- [ ] After a forced-kill mid-write, `PRAGMA integrity_check` returns `ok`.
- [ ] `Close` releases the lock and closes the file handle.

## Tests

- Concurrency test: goroutine holds the lock, second `Open` returns
  `LockedError` — assert elapsed time is under 100ms, not just that it failed.
- `Tx` rollback on error and on panic.
- ReadOnly open succeeds under a held lock.
- Subprocess test for the SIGKILL case if practical; otherwise document it as
  a manual check in the acceptance run.

## Out of scope

Which commands use which mode — each subcommand ticket states its own.
