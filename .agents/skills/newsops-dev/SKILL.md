---
name: newsops-dev
description: >-
  Conventions for changing the newsops Go CLI. Use when editing anything
  under cmd/ or internal/, adding a subcommand or flag, changing the SQLite
  schema, or when `make check`, `make test` or `make e2e` fails.
user_invocable: true
argument-hint: "[add-command <name> | add-flag <command> | migration | tests]"
---

# newsops development conventions

## Layout

| Path | Owns |
|---|---|
| `cmd/newsops/main.go` | cobra root, the `commands` routing table, the `commandFlags` map, exit-code and envelope handling |
| `cmd/newsops/<cmd>.go` | one thin command each; `*_repo.go` holds that command's SQL |
| `internal/store` | `Open` (modes, pragmas, flock), `Migrate`, schema, `FormatTime`/`ParseTime` |
| `internal/runstate` | `runs` table bookkeeping: `Begin`, `End`, `LastSuccess`, `Stale` |
| `internal/config` | `sources.yml`, `domains.yml`, `prefilter.yml` loading and validation |
| `internal/feed` | fetcher (concurrency, ETag, rate limit, readability), gofeed parsing, excerpt |
| `internal/dedupe` | URL canonicalization, aggregator links, title SimHash, `Assign` |
| `internal/candidates` | prefilter: window → drop shown → exclude → order → cap → limit |
| `e2e/` | binary-level test of every command (`e2e` build tag) |

The CLI reference an agent reads is `AGENTS.md`; keep it in sync with any
flag or JSON change in the same edit.

## Contracts

- **Timestamps.** One on-disk format, `store.TimeLayout`
  (`YYYY-MM-DD HH:MM:SS` UTC, what `CURRENT_TIMESTAMP` writes). Every write and
  every comparison parameter goes through `store.FormatTime`. Reading uses
  `store.ParseTime`. Comparing an RFC3339 string against a stored value is a
  string comparison and sorts wrongly; migration 2 exists because of that.
  Output to users is RFC3339 (`time.Time.Format(time.RFC3339)` after a scan).
- **Flags.** Register per command in `commandFlags` in `main.go`. Global flags
  (`--json`, `--db`, `--config`, `--digest`, `--verbose`) are root persistent
  flags only. Never bind two commands' flags to one variable with different
  defaults: cobra applies defaults at registration and map order decides the
  winner (that is why `collectMaxAge` and `fetchMaxAge` are separate).
- **Results and errors.** A command returns `(any, error)`.
  - Success: return the result struct; it is marshalled as-is.
  - User-facing failure with a slug: return `&ExitError{Code, Slug, Message,
    Fields}` as the *result* with a nil error; `Fields` are merged into the
    envelope (that is how `partial_fetch` carries its counts).
  - Usable output that still needs a non-zero exit (collect): implement
    `ExitCode() int` on the result (`exitCoder`).
  - Plain `error` → `internal`, exit 2. `*store.LockedError` anywhere in the
    chain → `locked` envelope; wrap with `%w`, match with `errors.As`.
- **Exit codes.** 0 ok or skipped, 1 partial (some feeds failed, output
  usable), 2 hard failure or bad input. `AGENTS.md` documents this; keep it true.
- **Database.** `store.Open(ctx, dir, store.ReadOnly)` never creates the file
  (`store.ErrNoDatabase`) and never locks. `store.ReadWrite` runs `MkdirAll`,
  migrations, then takes a non-blocking flock on `data/news.lock`. Any command
  that writes a row, including a `runs` row, opens ReadWrite.
- **Run bookkeeping.** Each stage writes one `runs` row via
  `runstate.Begin`/`End`. `dedupe.Assign` owns its own row and transaction;
  callers must not wrap it in another. Mark a run `ok=false` when it produced
  nothing usable (fetch with zero successful sources), because
  `LastSuccess` drives the fetch window and the collect gates.
- **Schema changes.** Append `{N, schemaVN}` to `migrations` in
  `internal/store/migrate.go` with the DDL in `schema.go`. Migrations run in a
  transaction on every open. Data rewrites belong in a migration too (see
  `schemaV2`). Tests assert `MAX(version)` equals the last entry, so no test
  edit is needed.
- **SQL lives in repos.** `cmd/newsops/*_repo.go` and `internal/*/…_repo.go`.
  FTS5 queries: auxiliary functions (`snippet`, `rank`) cannot sit under
  `GROUP BY`; collapse per story in Go (see `cmd/newsops/query.go`).

## Tests

| Target | What | Notes |
|---|---|---|
| `make test` | `go test ./...` | `cmd/newsops` integration tests build their own binary in `TestMain`; never rely on `./newsops` in the repo root, it may be stale |
| `make e2e` | `go test -tags e2e ./e2e/` against `./newsops` | one `TestEndToEnd` with a subtest per command, local `httptest` feeds, no network, no fixed ports |
| `make check` | build + test + e2e + skill symlinks + mode paths | the CI entrypoint; must stay offline |
| `make sources` | live feed check | network; not in `check` |

Fixture rules the suite relies on: items are published "now" (the fetch
window starts at the last successful fetch, so older items drop as
`dropped_old`), and the fixture server's ETag changes with the body (a fixed
ETag makes every re-fetch a 304).

## Checklist: adding a subcommand

1. `cmd/newsops/<name>.go`: `func cmd<Name>(ctx, args) (any, error)`, result
   struct with `json` tags, open the DB in the right mode.
2. `main.go`: add to `commands`; add its flags to `commandFlags`; add a line
   to `commandSummary` and the `Long` help text.
3. `AGENTS.md`: a `### \`<name>\`` section with usage, flags and the JSON shape.
4. `e2e/e2e_test.go`: a subtest that runs it and asserts the exit code and the
   fields an agent will read.
5. If agents should call it by name, a row in `.agents/skills/news-ops/SKILL.md`
   (router) and a block in `.agents/skills/newsops-cli/SKILL.md` (cookbook).
6. `make check`.

Adding a flag is steps 2–4 only. Renaming anything a mode file or skill quotes
means grepping `modes/ .agents/ AGENTS.md` in the same change.
