# T025 — Agent packaging: skill router, invocation surface, bootstrap

**Depends on:** T018–T024 (the modes must exist to be routed to)
**Size:** M
**Touches:** `.agents/skills/news-ops/SKILL.md`, `Makefile`, `README.md`,
`AGENTS.md`

## Why

T018 made the repo *readable* as an agentic workflow. It did not make it
*invocable* as one. Three things are missing, and each one breaks a fresh
session before it starts:

1. **Nothing triggers.** Mode selection depends on the agent noticing a
   four-row table at the bottom of `AGENTS.md`. There is no skill and no
   manifest anywhere. "What's in the news today" routes to nothing.
2. **The first command fails.** Every mode opens with
   `newsops status --json`. The binary is `./newsops`, gitignored, and not on
   `PATH`. `modes/_shared.md` step 1 is unrunnable from a clean checkout.
3. **Relative paths resolve against the wrong root.** Modes reference
   `context/interests.md`, `briefs/`, `posts/` bare. Invoked from a
   subdirectory, the agent reads nothing and writes briefs into the wrong
   tree.

The harness-neutrality rule from T018 still holds and this ticket is where it
is most likely to be violated: **the wrapper contains no behavior.** All
judgment stays in `AGENTS.md` and `modes/*.md`. The router maps an argument to
a mode file and stops.

**One real file, symlinks per harness.** `SKILL.md` lives at
`.agents/skills/news-ops/SKILL.md` — the vendor-neutral location that pairs
with `AGENTS.md` — and every harness-specific directory is a symlink to it,
never a copy. Two copies of a router drift, and the drift is invisible until
one harness starts routing to a mode the other deleted. Harness directories
hold a symlink and nothing else -- `.claude/skills/news-ops/SKILL.md` is as
vendor-neutral as `.cursor/`'s, because a pointer carries no behavior. What
stays out is anything that *does* carry behavior or vendor coupling: no
`.claude-plugin/`, no `.claude/commands/`, no `CLAUDE.md` (deleted in
`ad9fcbd`, correctly).

## Deliverables

### `.agents/skills/news-ops/SKILL.md`

Frontmatter:

```yaml
---
name: news-ops
description: >-
  Personal news pipeline -- collect and skim a daily digest, research a story
  into a brief, draft a LinkedIn or blog post. Use when the user asks what is
  in the news, asks to brief or research a story ID, asks for a draft, or runs
  any news-ops mode.
arguments: mode
user_invocable: true
argument-hint: "[collect | brief <id> | draft <id> --linkedin|--blog | voice | status]"
---
```

Body, in order:

1. **Project root resolution.** Start at this file's directory and walk
   upward to the nearest directory containing both `AGENTS.md` and `modes/`.
   That is `PROJECT_ROOT`. Resolve `modes/`, `config/`, `context/`, `data/`,
   `briefs/`, `posts/` and `digest.md` against it, never against the process
   working directory. If the two sentinels are not found, stop and say so.
2. **Binary resolution.** Prefer `newsops` on `PATH`; fall back to
   `$PROJECT_ROOT/newsops`. If neither exists, stop and point at
   `make install`. Never `go run` it — a compile on every call is a silent
   30-second tax.
3. **Mode routing table.**

   | Input | Mode |
   |---|---|
   | (empty) | `modes/collect.md` |
   | `collect`, `digest`, `news` | `modes/collect.md` |
   | `brief <id>` | `modes/brief.md` |
   | `draft <id> [--linkedin\|--blog]` | `modes/draft.md` |
   | `voice` | `modes/voice.md` |
   | `status` | run `newsops status --json`, report, stop |

4. **Always read `modes/_shared.md` first**, then the routed mode file, then
   follow it exactly.
5. **The gates.** Restate both human gates verbatim. A router that forgets
   them routes straight past them.

### Harness symlinks

For each harness in `$(HARNESSES)`, a symlink:

```
.<harness>/skills/news-ops/SKILL.md -> ../../../.agents/skills/news-ops/SKILL.md
```

Created by `make skills` (below) and committed as symlinks, not copies.
Adding a harness later is one `make skills` run with a longer variable.

If a harness needs a manifest or a command file of its own, it goes in that
harness's directory and stays a pointer — a name, a description, and the path
to `SKILL.md`. Behavior in a manifest is the same drift problem as a copied
router.

**Caveat:** git stores these as symlinks only when `core.symlinks` is true.
On a checkout where it is false they land as text files containing a path,
and the skill silently fails to load. `make skills` is idempotent and fixes
that — say so in `README.md`.

### `Makefile`

```
build     # go build -ldflags "-X main.Version=$(VERSION) -X main.Commit=$(COMMIT)" -o newsops ./cmd/newsops
install   # build, then install to $(PREFIX)/bin, default ~/.local/bin
skills    # symlink .agents/skills/news-ops into each $(HARNESSES) dir, idempotent
test      # go test ./...
check     # build + test + skills link check (offline, deterministic)
sources   # ./newsops sources check --json -- network reachability, kept out of check
clean
```

`VERSION` from `git describe --tags --always --dirty`, `COMMIT` from
`git rev-parse --short HEAD`. `HARNESSES` is a space-separated list of
directory names, overridable on the command line
(`make skills HARNESSES="opencode cursor"`).

`main.go:15` already declares `Version` and `Commit` as ldflags targets and nothing has ever injected them — `newsops
version` reports `{"commit":"none","version":"dev"}` today.

### `README.md`

Currently one line. Needs: what this is (one paragraph, lifted from
`AGENTS.md`), `make install`, where `PATH` needs to point, the three-command
daily loop, and a pointer to `AGENTS.md` for everything else. Not a second
copy of the CLI reference.

### `AGENTS.md`

Add a **Getting started** section above **The Loop**: `make install`, verify
with `newsops version`, then one line saying the skill router is the
entrypoint and the mode table below is the contract it routes to.

## Acceptance criteria

- [ ] `SKILL.md` contains no judgment, no ranking rules, no output formats —
      only resolution, routing, and the gates. Diff it against `modes/*.md`
      and find no overlap.
- [ ] The project-root walk-up is stated as an instruction, not assumed.
      Verify by invoking the skill from `internal/feed/` and confirming the
      digest lands at `$PROJECT_ROOT/digest.md`.
- [ ] `make install` on a clean clone produces a working `newsops` on `PATH`,
      and `newsops version --json` reports a real tag and commit, not
      `dev`/`none`.
- [ ] Every path in the routing table exists. `for f in $(grep -o 'modes/[a-z_]*\.md' SKILL.md); do test -f $f; done`.
- [ ] Exactly one `SKILL.md` has content. `find . -name SKILL.md -type f`
      returns one path; every other hit is a symlink resolving to it.
- [ ] No Claude-specific file carries behavior. `git ls-files | grep -iE
      'claude'` returns only `.claude/skills/news-ops/SKILL.md`, and it is a
      symlink (mode `120000`).
- [ ] `make skills` is idempotent and repairs a checkout where symlinks
      landed as plain files.
- [ ] Both human gates appear in `SKILL.md`.
- [ ] `AGENTS.md` and `modes/*.md` are unchanged except for the new
      **Getting started** section — grep them for `Skill`, `subagent`,
      `Task(` and still find nothing (T018's criterion, still binding).

## Tests

Manual, in a fresh session with no prior context:

1. `make install`, then `newsops version` — real version, not `dev`.
2. Ask "what's in the news today". The agent should route to
   `modes/collect.md`, run `status` then `collect`, write `digest.md`, call
   `decide ... shown`, and **stop at the first gate** without briefing.
3. Ask to brief one ID. It should route to `modes/brief.md` and stop at the
   second gate without drafting.
4. Repeat step 2 with the session started from `cmd/newsops/`. Same result,
   same file locations.

## Out of scope

- **The voice corpus.** `context/voice/` holds only `README.md` and
  `EXAMPLE.md`, so `modes/voice.md` will correctly refuse (it requires 5
  samples) and `modes/draft.md` will correctly stop on the missing
  `context/voice-guide.md`. That is the designed behavior, not a packaging
  bug. Adding real samples is a human data task, not a ticket.
- **The `sources:check` naming mismatch.** `main.go:121` splits the routing
  key at the colon, so the real invocation is `newsops sources check` while
  `AGENTS.md` documents `newsops sources:check` — which returns
  `unknown_command`. The Makefile now calls the form that works. Deciding
  which surface is correct, and fixing the other, is its own ticket.
- **Harnesses not in `$(HARNESSES)`.** Adding one later is a symlink, which
  is why the router is behavior-free. Do not pre-create directories for
  harnesses nobody runs.
- Any change to CLI behavior. This ticket ships no Go beyond the ldflags
  wiring in the Makefile.
