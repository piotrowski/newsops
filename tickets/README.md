# news-ops tickets

Generated from `docs/PLAN.md` (v4). Each ticket is self-contained: it repeats
the schema, interfaces, and JSON shapes it needs so you can execute it without
reading the plan or the neighbouring tickets. Where two tickets share a
contract, both state it.

## Build order

Tickets are numbered in dependency order. T001–T017 are Go; T018–T024 are
markdown the agent reads; T025 is the wrapper that makes it invocable.
T026–T031 come from the post-T025 review, not from `docs/PLAN.md` — they fix
what shipped broken and close the loops the plan left open.

| # | Ticket | Size | Depends on |
|---|---|---|---|
| T001 | Project scaffold + CLI skeleton | M | — |
| T002 | SQLite schema + migrations | L | T001 |
| T003 | DB open, WAL, transactions, process lock | M | T002 |
| T004 | Run bookkeeping (`runstate`) | S | T003 |
| T005 | Config files + loaders | M | T001 |
| T006 | Feed parser + excerpt builder | M | T005 |
| T007 | Concurrent fetcher + raw cache | L | T006, T004 |
| T008 | `fetch` subcommand | M | T007, T003 |
| T009 | `sources:check` subcommand | S | T007 |
| T010 | URL canonicalization + aggregator resolution | M | T005 |
| T011 | Title normalization + SimHash | M | T005 |
| T012 | `dedupe` — story assignment | L | T010, T011, T004 |
| T013 | `status` subcommand | S | T004 |
| T014 | `candidates` — prefilter | L | T012, T005 |
| T015 | `collect` — the chained command | M | T008, T012, T014 |
| T016 | `query` — FTS5 archive search | M | T002, T003 |
| T017 | `decide` + `show` subcommands | S | T012 |
| T018 | AGENTS.md, CLAUDE.md, modes/_shared.md | M | T013–T017 |
| T019 | `modes/collect.md` — skim and digest | M | T018, T015 |
| T020 | `modes/brief.md` — research one story | M | T018, T016, T017 |
| T021 | `context/` templates | S | T001 |
| T022 | Voice corpus + `modes/voice.md` | M | T018 |
| T023 | `modes/draft.md` — LinkedIn and blog | M | T022, T020 |
| T024 | Two-week review protocol | S | T019, T020, T023 |
| T025 | Agent packaging: skill router, invocation surface, bootstrap | M | T018–T024 |
| T026 | Per-subcommand flag registration + flag-error contract | L | T008, T012, T014, T016, T017 |
| T027 | Reconcile the `sources:check` command name | S | T009 |
| T028 | `review` subcommand: make the two-week protocol runnable | M | T017, T024, T026 |
| T029 | Close the decision loop: `posted` and `dismissed` | S | T017, T028 |
| T030 | CI: run `make check` on every push | S | T025, T026 |
| T031 | Bare invocation: discovery mode + undocumented `voice-guide.md` | S | T025 |

**Milestones.** After T009 you can collect real data. After T017 the CLI is
complete and drivable by hand. After T021 the daily loop works end to end.
After T023 the system does what it was built for. After T025 it starts on its
own from a clean checkout.

**The post-review milestone.** T001–T025 shipped a pipeline whose archive is
unreachable: `query` has no working invocation, so `modes/brief.md` cannot
find prior coverage and `modes/collect.md` cannot flag follow-ups. T026 is the
one that makes the system do what T023 claimed it already did. Everything
after it is feedback: T028 makes the review runnable, T029 gives it a bottom
to the funnel, T030 keeps the whole thing from regressing unobserved.

## Shared conventions

Every ticket assumes these. They are restated in the tickets where they
matter.

**JSON contract.** Every subcommand accepts `--json` and writes a single JSON
object to stdout. Human-readable output is the fallback, not the contract.
Diagnostics go to stderr, never stdout — the agent parses stdout.

**Exit codes.** `0` success or deliberately skipped · `1` partial (some feeds
failed, useful output still produced) · `2` hard failure (bad args, locked
database, nothing usable).

**Error shape.** On a non-zero exit, stdout carries
`{"error": "<machine-readable-slug>", "message": "<human sentence>"}` plus any
useful fields. An agent must never have to parse a Go error string.

**Time.** All timestamps are RFC3339 UTC, stored as `DATETIME` text in SQLite.

**No LLM in T001–T017.** The Go half is deterministic. Judgment starts at T018.
