# T026 — Per-subcommand flag registration + flag-error contract

**Depends on:** T008, T012, T014, T016, T017 (the commands whose flags are missing)
**Size:** L
**Touches:** `cmd/newsops/main.go`, `cmd/newsops/query.go`,
`cmd/newsops/decide.go`, `cmd/newsops/dedupe.go`, `cmd/newsops/candidates.go`,
`cmd/newsops/main_test.go`, `AGENTS.md`

## Why

`main.go:117` builds every subcommand from the `commands` routing table and
attaches exactly four flags to each one: `--json`, `--db`, `--config`,
`--verbose`. Nothing else is ever registered. The subcommand-specific
variables exist — `queryText` (`query.go:16`), `decideNote` (`decide.go:14`),
`dedupeWindow` (`dedupe.go:14`) — and nothing binds them, so every flag that
reaches them is rejected by `root.ParseFlags` at `main.go:135` before cobra
dispatches anything.

Measured on `ad9fcbd`:

```
newsops query --text postgres        → flag error: unknown flag: --text   exit 2
newsops query --domain danluu.com    → flag error: unknown flag: --domain exit 2
newsops query --story 1 --related    → flag error: unknown flag: --story  exit 2
newsops decide 1 shown --note "x"    → flag error: unknown flag: --note   exit 2
newsops dedupe --window 48h          → flag error: unknown flag: --window exit 2
```

`query` has **no reachable invocation at all**. Every documented form errors,
and the flagless form exits 2 on its own "at least one of --text, --domain, or
--story is required" guard (`query.go:66`). The archive is unqueryable.

This is not cosmetic. Three mode steps are dead as written:

- `modes/collect.md` rules — `query --text "<key terms>" --days 90`, the
  follow-up detection that produces the "you covered their July incident"
  reason lines.
- `modes/brief.md:5` — `query --story <id> --related`, the archive-connection
  step. Without it a brief is a summary of one URL, which is the thing the
  brief format explicitly is not.
- `modes/collect.md:6` — `decide <ids> shown --note "rank=<n>"`. Exits 2, so
  no ranks are ever recorded, so `docs/REVIEW.md` measurement 1 has no data
  to read.

**The flags that work do so by accident.** `--source`, `--max-age`, `--since`,
`--no-cache`, `--include-shown` and `--limit` are registered as *root
persistent* flags (`main.go:110-115`) — global to every subcommand. That is
what forces the ugliest bug in the set:

```
newsops candidates --since 2026-09-01T00:00:00Z --json
→ {"window":{"since":"2026-09-22T17:32:23Z","days":0}, "returned":0, ...}
```

Exit 0, no error, wrong window. `--since` writes to `fetchSince`; `candidates`
reads `candidatesSince`, which is bound to `--candidates-since`
(`main.go:113`) — a name invented solely to dodge the collision, and the one
name `AGENTS.md` does not document. An agent following the documented surface
gets a silently empty candidate list and no way to know why.

## What to build

### 1. A flag registry keyed by command name

Replace the blanket four-flag loop with a per-command registration hook. Shape
is open; the constraint is that the routing table and the flag table are
declared next to each other, so adding a command without its flags is visibly
incomplete rather than silently broken.

```go
// flagsFor registers subcommand-specific flags. Commands absent from this
// map take the four global flags and nothing else.
var flagsFor = map[string]func(*pflag.FlagSet){
	"fetch": func(f *pflag.FlagSet) {
		f.StringVar(&fetchSource, "source", "", "restrict fetch to one source by name")
		f.DurationVar(&fetchMaxAge, "max-age", 0, "skip if last success is fresher than this")
		f.StringVar(&fetchSince, "since", "", "override fetch window start with RFC3339 time")
		f.BoolVar(&fetchNoCache, "no-cache", false, "skip raw-body writes")
	},
	"query": func(f *pflag.FlagSet) {
		f.StringVar(&queryText, "text", "", "FTS5 search text")
		f.StringVar(&queryDomain, "domain", "", "restrict to one domain")
		f.Int64Var(&queryStoryID, "story", 0, "story id for --related")
		f.BoolVar(&queryRelated, "related", false, "find stories related to --story")
		f.IntVar(&queryDays, "days", 0, "lookback window in days (0 = 90)")
		f.IntVar(&queryLimit, "limit", 0, "max results (0 = 20)")
	},
	// dedupe: --window, --limit
	// candidates: --since, --include-shown, --limit
	// decide: --note
	// sources check: --source
}
```

**Move the six accidentally-global flags off the root command** as part of
this. They stay working, but scoped. Once `--since` is registered on `fetch`
and on `candidates` separately, the names stop colliding and
`--candidates-since` is deleted — it exists only to work around the collision
this ticket removes.

`--limit` lands on three commands (`dedupe`, `candidates`, `query`) with three
different meanings and three different backing variables. That is correct and
is exactly what the root-persistent approach could not express.

### 2. Flag errors must honour the error contract

`main.go:135` bails before the JSON error path is reachable:

```go
if err := root.ParseFlags(os.Args[1:]); err != nil {
	fmt.Fprintln(stderr, "flag error:", err)   // plain text, stderr, empty stdout
	os.Exit(2)
}
```

`tickets/README.md` promises that on a non-zero exit stdout carries
`{"error": "<slug>", "message": "<sentence>"}`, and that "an agent must never
have to parse a Go error string." Routing errors already honour it —
`newsops nope --json` returns `{"error":"unknown_command",...}`. Flag errors
do not: stdout is empty, so an agent parsing stdout sees nothing at all and
has no slug to branch on. It cannot distinguish a typo'd flag from a crash.

Route this failure through the same emitter as every other error, with slug
`invalid_flag`. Preserve exit 2 and the human-readable fallback when `--json`
is absent.

Audit the file for other pre-dispatch `os.Exit` calls while in here —
`main.go:141` (`len(os.Args) < 2` → `root.Usage()`) has the same shape and
should emit a JSON error under `--json` rather than a usage block on stderr.

### 3. Reconcile `AGENTS.md`

`AGENTS.md` is the flag contract; the tests below assert against it, so it has
to be right. Correct the `candidates` entry (`--since`, not the
soon-to-be-deleted `--candidates-since`) and verify every other documented
flag against the new registry. Do not add flags to the docs to make tests
pass — if a documented flag has no reason to exist, delete it from the docs
and say so here.

## Acceptance criteria

- [ ] `query --text postgres --json` returns a result object, exit 0.
- [ ] `query --domain danluu.com --json` and `query --story <id> --related
      --json` both return result objects, exit 0.
- [ ] `query --days 30 --limit 5 --text x --json` honours both bounds.
- [ ] `query --json` with no selector still exits 2 with
      `{"error":"invalid_argument"}` — the guard at `query.go:66` is correct
      and stays.
- [ ] `decide 1 shown --note "rank=3" --json` exits 0 and the note round-trips
      through `show 1 --json`.
- [ ] `dedupe --window 48h --json` exits 0 and the window reaches the
      title-match path.
- [ ] `candidates --since <RFC3339> --json` reports **that** window in
      `window.since`, not the fetch window.
- [ ] `--candidates-since` no longer exists anywhere in the tree.
- [ ] `fetch --since` still works and is unaffected by `candidates --since`.
- [ ] `newsops query --nonsense --json` writes
      `{"error":"invalid_flag","message":...}` to **stdout**, exit 2, nothing
      on stderr.
- [ ] `newsops --json` with no subcommand writes a JSON error to stdout, exit 2.
- [ ] Every flag documented in `AGENTS.md` parses on the command it is
      documented under.

## Tests

`cmd/newsops/main_test.go`, table-driven, one row per `(command, flag)` pair
documented in `AGENTS.md` — assert the flag parses, not that the command
succeeds. The point is to fail loudly the next time a flag is declared but not
bound.

Add two behavioural tests that a parse-only test would miss:

1. **`candidates --since` sets the candidates window.** Seed stories either
   side of a boundary, pass `--since`, assert the returned set. A parse test
   passes today for `--since` and the command is still wrong.
2. **`decide --note` round-trips.** Write with `--note`, read back with
   `show`, compare. This is the link `docs/REVIEW.md` measurement 1 depends on.

And one contract test: `query --nonsense --json` produces valid JSON on
stdout with slug `invalid_flag` and an empty stderr.

## Out of scope

- The `sources:check` vs `sources check` naming mismatch — T027.
- Any change to what `query` *returns*. T016 defined the ranking and snippet
  behaviour; this ticket only makes it reachable.
- New flags. Bind what is documented; propose additions separately.
