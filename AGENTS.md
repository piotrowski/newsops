# Repository Guidelines

## Project Overview

**news-ops** is a Go CLI tool for automated news operations — source collection, deduplication, candidate management, and digest publishing. It inverts the typical agentic workflow: the CLI handles HTTP concurrency, feed parsing, deduplication, and persistence; the agent (human or AI) handles judgment, research, and writing. Two human gates control the loop: digest selection and post approval.

## Architecture & Data Flow

### High-Level Structure

```
cmd/newsops/        → Cobra CLI entry point + command routing
internal/store/     → SQLite layer (schema, migrations, DB open/close, locking)
internal/runstate/  → Pipeline run bookkeeping (Begin/End pattern)
internal/config/    → YAML config loading & validation
config/             → sources.yml, domains.yml, prefilter.yml
data/               → Runtime: news.db, news.lock (gitignored)
tickets/            → Implementation tickets T001–T024
```

### Key Modules

| Package | Responsibility |
|---|---|
| `cmd/newsops` | Cobra routing, global flags (`--json`, `--db`, `--config`, `--verbose`), error envelope formatting |
| `internal/store` | `DB` struct (embeds `*sql.DB` + optional lock file), `Open(ctx, dir, mode)`, `Tx()`, `Migrate()`, Flock-based process locking |
| `internal/runstate` | `Begin()`/`End()` for pipeline stage bookkeeping; `LastSuccess()` returns most recent ok=1 started_at; `Stale()` wraps with maxAge check |
| `internal/config` | `Load(dir)` reads sources.yml (required), domains.yml (required), prefilter.yml (optional → defaults); validates all fields |

### Database Schema (7 tables + FTS5)

| Table | Purpose |
|---|---|
| `items` | Raw articles fetched from feeds — one row per URL, unique by url |
| `stories` | Story clusters — one row per deduplicated news event; canonical_item_id links to items |
| `story_sources` | All sources contributing to a story (article + discussion links) |
| `decisions` | Append-only decision log: shown/briefed/drafted/posted/dismissed |
| `runs` | Runbook bookkeeping — Begin/End pattern per pipeline stage |
| `feed_state` | HTTP conditional-request state per source (ETag, Last-Modified) |
| `schema_version` | Migration tracker |

`items_fts` is an FTS5 virtual table with external content pointing to items. Three triggers (`items_ai`, `items_ad`, `items_au`) keep the index in sync on insert/update/delete.

### Data Flow Pipeline

```
collect → dedupe → candidates → decide → show
```

- **fetch**: Collects articles from RSS/Atom feeds, stores raw items, records ETag/Last-Modified
- **dedupe**: Turns unassigned items into stories (URL match first, then title similarity within 48h). Creates/joins stories, inserts story_sources rows. Story IDs are never reused or renumbered.
- **candidates**: Identifies unseen stories (shown_at IS NULL) within a time window — produces the candidate list for agent review
- **decide**: Agent records actions on stories (append-only to decisions; shown also sets shown_at). Actions: shown, briefed, drafted, posted, dismissed
- **show**: Displays a single story with all member items, sources, and decision history

### Locking Model

- `data/news.lock` uses `syscall.Flock(fd, LOCK_EX|LOCK_NB)` for exclusive process-level locking
- `ReadOnly` mode: no lock taken — concurrent readers work via WAL
- `ReadWrite` mode: acquires exclusive flock; writes `<pid> <RFC3339>` to the lock file for diagnostics
- OS releases flock on process death (SIGKILL, crash) — no stale-lock cleanup needed
- On contention, returns `*store.LockedError{PID, HeldSince}` with exit code 2

### Error Envelope Format

Three-tier error model. All errors produce JSON to stdout or human text to stderr:

```json
{"error": "<slug>", "message": "...", ...fields}
```

| Type | Slug | Source |
|---|---|---|
| `ExitError` | from `.Slug` field | Command returns `&ExitError{Code, Slug, Message, Fields}` as result value |
| `LockedError` | `"locked"` | Flock contention — includes `pid`, `held_since` fields |
| Plain error | `"internal"` | Any other error (DB, IO, parse) wrapped with Code=2 |

Global flag `--json` controls output format. Without it, errors print to stderr only; stdout carries either JSON or human-readable output, never both.

## Key Directories

| Path | Purpose |
|---|---|
| `cmd/newsops/` | CLI entry point (`main.go`), tests (`main_test.go`) |
| `internal/store/` | Database layer: schema.go (DDL), migrate.go (migrations), db.go (Open/Tx/Close), lock.go (flock) |
| `internal/runstate/` | Pipeline runbook: runstate.go + tests |
| `internal/config/` | Config loader: config.go + tests + testdata/ fixtures |
| `config/` | YAML config files: sources.yml, domains.yml, prefilter.yml |
| `tickets/` | Implementation tickets T001–T024 (T###-slug.md format) |
| `data/` | Runtime data (gitignored): news.db, news.lock |

## Development Commands

```bash
# Build static binary (no cgo required)
CGO_ENABLED=0 go build -o newsops ./cmd/newsops/

# Build with version injection
CGO_ENABLED=0 go build -ldflags="-X main.Version=v1.0.0 -X main.Commit=abc123" -o newsops ./cmd/newsops/

# Run all tests
CGO_ENABLED=0 go test ./...

# Run a specific package's tests
CGO_ENABLED=0 go test ./internal/store/

# Run with verbose output
CGO_ENABLED=0 go test -v ./internal/runstate/

# Run integration tests (requires pre-built binary)
CGO_ENABLED=0 go test -run TestIntegration_ ./cmd/newsops/
```

## Code Conventions & Common Patterns

### Error Handling

- **Three-tier model**: `ExitError` (structured, from commands), `LockedError` (flock contention), plain errors (wrapped as `internal`)
- All error wrapping uses `%w` for `errors.Is`/`errors.As` compatibility: `fmt.Errorf("open database: %w", err)`
- Commands return `(any, error)` — structured errors come back as the result value, not the error channel
- `exitCodeError{code int}` carries exit codes through Cobra's error path; implements `ExitCode() int`

### Database Access

```go
// Open with mode (ReadOnly or ReadWrite)
db, err := store.Open(ctx, "data/", store.ReadWrite)
if err != nil { /* handle */ }
defer db.Close()

// Transaction with automatic rollback on error or panic
err = db.Tx(ctx, func(tx *sql.Tx) error {
    _, err := tx.ExecContext(ctx, "INSERT INTO ...")
    return err
})
```

- `store.Open()` creates data directory, opens SQLite with pragmas (WAL, FK=ON, synchronous=NORMAL, busy_timeout=5000), verifies each pragma at runtime, runs migrations, acquires flock for ReadWrite
- `Tx()` wraps BeginTx → fn(tx) → commit on success, rollback on error; panic-safe via defer-recover that rolls back then re-panics

### CLI Command Registration

Commands are registered in a map and iterated to build Cobra subcommands:

```go
var commands = map[string]Command{
    "status": cmdStatus, "fetch": cmdFetch, "dedupe": cmdDedupe,
    // ... all subcommands
}
```

Each command signature: `func(ctx context.Context, args []string) (any, error)`

Colons in names (`sources:check`) produce nested Cobra commands (`sources check`).

### Config Loading

- `Load(dir string) (*Config, error)` reads three YAML files from the given directory
- Sources validation: unique+non-empty names, tier 1..3, http/https URLs only
- Prefilter: optional file — missing yields all defaults; partial file merges specified keys with defaults
- `IsPrimary(rawURL string) bool` strips "www." from host and checks against PrimaryDomains set

### Test Patterns

| Pattern | Used In | Description |
|---|---|---|
| Table-driven unit tests | main_test.go, config_test.go | Slice of structs driving test cases |
| File-backed SQLite (`t.TempDir()`) | db_test.go, runstate_test.go | Real DB for lock/WAL/concurrency testing via `store.Open()` |
| In-memory SQLite (`:memory:`) | migrate_test.go | Schema-only testing; no filesystem concerns |
| Binary integration tests | main_test.go | Spawns `../../newsops` binary to test CLI surface |
| Schema golden verification | migrate_test.go | Queries `sqlite_master` for exact tables/indexes/triggers |
| Constraint enforcement | migrate_test.go | Inserts invalid data, asserts SQL error returned |
| Manual step tests | db_test.go | Prints PID + shell commands; documents manual verification |

### Naming Conventions

- Package names: lowercase, single word (`store`, `runstate`, `config`)
- Struct fields: PascalCase with YAML tags matching snake_case keys (e.g., `CandidateLimit int yaml:"candidate_limit"`)
- Test functions: `Test<Function>_<Scenario>` (e.g., `TestBegin_CrashedRun`, `TestLoad_validationErrors/duplicate_name`)

## Important Files

| File | Purpose |
|---|---|
| `cmd/newsops/main.go` | CLI entry point, command routing, error envelope, global flags |
| `internal/store/schema.go` | DDL constants (V1: 6 tables + FTS5 virtual table + triggers) |
| `internal/store/migrate.go` | Migration engine — runs DDL in transactions, records version |
| `internal/store/db.go` | DB wrapper with Open/Close/Tx, pragma verification |
| `internal/store/lock.go` | Flock-based process locking on news.lock |
| `internal/runstate/runstate.go` | Pipeline runbook: Begin/End/LastSuccess/Stale |
| `internal/config/config.go` | YAML config loader with validation and IsPrimary() |
| `config/sources.yml` | 51 feed sources with tier 1/2/3 ratings |
| `config/domains.yml` | Primary domains whitelist for canonical-item selection |
| `config/prefilter.yml` | Tuning knobs: candidate_limit=40, per_source_cap=3, title_simhash_max=3, max_backfill_days=30 |
| `go.mod` | Module: news-ops, Go 1.26, dependencies: cobra, modernc.org/sqlite, yaml.v3 |
| `.gitignore` | Ignores data/, briefs/, posts/, digest.md, newsops binary |

## Runtime & Tooling Preferences

- **Go version**: 1.26 (per go.mod)
- **Build constraint**: `CGO_ENABLED=0` — must produce static binaries with no cgo dependency
- **Database driver**: `modernc.org/sqlite` — pure Go SQLite transpilation, no system libraries needed
- **CLI framework**: `spf13/cobra` — subcommand routing, persistent flags, auto-completion
- **Config format**: YAML via `gopkg.in/yaml.v3`
- **No Makefile, no CI config, no shell scripts** — single `go build` command, agent-driven workflow

## Testing & QA

### Running Tests

```bash
# All packages (cmd + internal/*)
CGO_ENABLED=0 go test ./...

# Specific package
CGO_ENABLED=0 go test ./internal/store/

# Verbose output with test names
CGO_ENABLED=0 go test -v ./internal/runstate/
```

### Coverage Summary

| Package | Tests | Key Coverage Areas |
|---|---|---|
| `cmd/newsops` | 14 tests | Routing dispatch, exit code mapping, error envelope keys, version output, unknown command handling, stub subcommands |
| `internal/store` (migrate) | 12 tests | All tables/indexes/triggers created, idempotent migration, rollback on failure, FTS sync (insert/update/delete), CHECK constraints, UNIQUE, FK enforcement |
| `internal/store` (db) | 8 tests | Directory creation, lock contention (<100ms), ReadOnly under write lock, Tx rollback on error/panic, Close releases lock, concurrent readers |
| `internal/runstate` | 10 tests | Begin/End lifecycle, failed vs success runs, crashed run handling, never-run detection, stale threshold, double-End rejection |
| `internal/config` | 17 tests | 7 validation fixtures (duplicate name, tier 0/4, bad URL, ftp, limits), real config load (51 sources), missing/partial prefilter, IsPrimary with www stripping |

**Total: 61 tests across 4 packages. All pass.**

### Test Data Fixtures

Config validation uses `internal/config/testdata/<scenario>/` directories:
- `duplicate_name/`, `tier_zero/`, `tier_four/`, `bad_url/`, `ftp_url/` — sources.yml + domains.yml only
- `candidate_limit_zero/`, `title_simhash_max_65/`, `missing_prefilter/`, `partial_prefilter/` — includes prefilter.yml
