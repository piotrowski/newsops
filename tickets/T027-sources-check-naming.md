# T027 — Reconcile the `sources:check` command name

**Depends on:** T009
**Size:** S
**Touches:** `cmd/newsops/main.go`, `AGENTS.md`, `Makefile`

## Why

Two names for one command, and the documented one does not work:

```
newsops sources:check --json → {"error":"unknown_command","message":"Unknown command \"sources:check\""}
newsops sources check --json → {"total":69,"passed":68,"failed":1,...}
```

`commands` (`main.go:42`) is keyed `"sources:check"`, and `splitCommand`
(`main.go:252`) splits that key at the colon to build a cobra parent-and-child
pair. So the map key is the colon form and the invocation is the space form.
`AGENTS.md` documents the colon form under `### \`sources:check\``, and so does
the root command's own `Long` help text (`main.go:83`). The `Makefile`
`sources` target calls the space form, because that is the one that runs.

An agent reading `AGENTS.md` — which is loaded as project instructions, so it
is the surface an agent trusts — issues the colon form, gets
`unknown_command`, and has no way to discover the working spelling short of
reading Go source. T025 deferred this deliberately; it is now the only
documented command that cannot be invoked as documented.

## Decide first, then implement

This ticket is one decision and a small edit. The decision is which surface is
real:

**Option A — the space form wins.** `sources check` is cobra-idiomatic, reads
as a noun-verb pair, and leaves room for `sources list`, `sources add`. Rename
the map key, drop `splitCommand` and `spaceOrEmpty` if nothing else uses them,
fix `AGENTS.md` and the `Long` text.

**Option B — the colon form wins.** Register the literal `sources:check` as a
single cobra command, delete the splitting machinery, leave `AGENTS.md`
untouched, fix the `Makefile`.

**Recommendation: A.** The colon exists only because the routing table wanted
a namespaced key, and `splitCommand` is machinery serving exactly one caller.
Option B keeps a name no other subcommand uses and blocks the `sources <verb>`
family. Whichever is chosen, accept an alias for the other spelling rather
than a bare `unknown_command` — one line, and it forecloses this exact
confusion for anyone working from the older docs.

## Acceptance criteria

- [ ] The form documented in `AGENTS.md` runs and exits 0 against reachable
      feeds.
- [ ] The other form either runs as an alias or returns an error naming the
      correct spelling — never a bare `unknown_command`.
- [ ] `main.go`'s `Long` help text lists the same spelling as `AGENTS.md`.
- [ ] The `Makefile` `sources` target uses that spelling.
- [ ] `sources check --source <name>` still filters to one source (it works
      today; do not regress it).
- [ ] If `splitCommand`/`spaceOrEmpty` lose their last caller, they are
      deleted, not left behind.

## Tests

One test asserting the canonical spelling dispatches, one asserting the other
spelling produces either the same dispatch or an error message containing the
canonical spelling. Both offline — no network. Feed reachability stays in
`make sources`, which is deliberately outside `make check`.

## Out of scope

Anything about what `sources:check` *reports*. T009 defined that and it works.
