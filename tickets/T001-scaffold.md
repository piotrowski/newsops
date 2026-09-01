# T001 — Project scaffold + CLI skeleton

**Depends on:** nothing
**Size:** M
**Touches:** `go.mod`, `cmd/newsops/`, `.gitignore`, directory tree

## Why

Every later ticket adds a subcommand. Getting the routing, the `--json`
contract, and the exit-code discipline right once means none of them has to
think about it again — and an agent parsing this CLI depends on all three
being uniform. Doing it per-command guarantees drift.

## Deliverables

- `go.mod` — module `news-ops`, Go 1.23 or newer. No dependencies yet.
- Directory tree with a `.gitkeep` where empty:

```
cmd/newsops/main.go
internal/{feed,store,dedupe,candidates,runstate}/
config/
context/voice/
modes/
data/          (gitignored)
briefs/        (gitignored)
posts/         (gitignored)
```

- `.gitignore` — `/data/`, `/briefs/`, `/posts/`, `/digest.md`, `/newsops`.
- `cmd/newsops/main.go` — routing, flag parsing, output, exit codes.

## Interface

```go
// A command returns the value to serialize as JSON, or an error.
type Command func(ctx context.Context, args []string) (any, error)

var commands = map[string]Command{
    "status": cmdStatus, "sources:check": cmdSourcesCheck,
    "fetch": cmdFetch, "dedupe": cmdDedupe, "candidates": cmdCandidates,
    "collect": cmdCollect, "query": cmdQuery, "decide": cmdDecide,
    "show": cmdShow, "version": cmdVersion,
}

// ExitError carries an explicit exit code and a machine-readable slug.
type ExitError struct {
    Code    int    // 1 = partial, 2 = hard failure
    Slug    string // "locked", "no_sources", "bad_args", ...
    Message string
    Fields  map[string]any // merged into the error JSON
}
```

Register every subcommand in this ticket, each returning
`&ExitError{Code: 2, Slug: "not_implemented"}`. Later tickets replace bodies
without touching routing.

Output rules:
- Success → marshal the returned value, print to stdout, exit 0.
- `ExitError` → print `{"error": slug, "message": msg, ...fields}` to stdout,
  exit with `Code`.
- Any other error → same shape with slug `internal`, exit 2.
- Without `--json`, print a human rendering; the JSON path must still be the
  one that is tested.

Global flags parsed before the subcommand: `--json`, `--db <dir>` (default
`data/`), `--config <dir>` (default `config/`), `--verbose` (stderr only).

## Acceptance criteria

- [ ] `go build ./cmd/newsops/` produces a static binary with no cgo.
- [ ] `newsops` with no arguments prints usage listing all subcommands, exit 2.
- [ ] `newsops nonsense` exits 2 with `{"error": "unknown_command"}`.
- [ ] `newsops version --json` returns `{"version": "...", "commit": "..."}`,
      exit 0 — this is the one command T001 fully implements.
- [ ] Every registered subcommand accepts `--json` without a flag error.
- [ ] Diagnostics appear on stderr; stdout is always either valid JSON or the
      human rendering, never both.

## Tests

- Routing table test: known command dispatches, unknown returns
  `unknown_command`.
- Exit-code mapping: `ExitError{Code: 1}` → 1, plain error → 2, nil → 0.
- Golden test that the error envelope has exactly the documented keys.

## Out of scope

Any real subcommand behavior. Config loading (T005). Database (T002).
