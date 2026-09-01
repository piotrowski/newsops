# T018 — AGENTS.md, CLAUDE.md, modes/_shared.md

**Depends on:** T013–T017 (the CLI contract must exist to be documented)
**Size:** M
**Touches:** `AGENTS.md`, `CLAUDE.md`, `modes/_shared.md`, `.claude/commands/`

## Why

This is where the system stops being a CLI and becomes an agentic workflow.
Everything the agent knows lives here.

**Harness-neutral by construction.** All behavior goes in `AGENTS.md` and
`modes/*.md` as plain markdown any harness can read. `CLAUDE.md` is a
two-line import. Claude Code slash commands, if added, contain no behavior —
each is a one-liner pointing at a mode file. The test of this ticket is that
moving to Pi requires changing nothing but the wrapper.

## Deliverables

### `AGENTS.md`

Sections, in order:

1. **What this is** — a personal news pipeline; the agent judges, researches
   and drafts, the CLI collects and remembers, the human decides. Three
   sentences.
2. **The loop** — the diagram, with the two human gates marked:

```
newsops collect       →  ~40 candidates (deterministic, no LLM)
    ↓  you skim and rank them
digest.md             →  one line per candidate, with a reason
    ↓  ── HUMAN GATE: which are worth researching? ──
briefs/<id>-<slug>.md →  research, primary sources, archive connections
    ↓  ── HUMAN GATE: which is worth writing about? ──
posts/<date>-<slug>-<target>.md
    ↓  the human edits and publishes by hand
```

3. **CLI reference** — every subcommand, its flags, and the shape of its JSON
   output. Written so the agent never has to run `--help` to discover a flag.
   Cover: `status`, `collect`, `fetch`, `dedupe`, `candidates`,
   `sources:check`, `query`, `decide`, `show`.
4. **Exit codes** — 0 success or skipped · 1 partial, output still usable ·
   2 hard failure. **Exit 1 is not a reason to stop**; exit 2 is.
5. **Where things live** — `config/`, `context/`, `modes/`, `briefs/`,
   `posts/`, `data/`.
6. **Which mode to load** — collect → `modes/collect.md`, research →
   `modes/brief.md`, writing → `modes/draft.md`, voice →
   `modes/voice.md`. Always read `modes/_shared.md` first.

### `CLAUDE.md`

Exactly:

```markdown
@AGENTS.md
```

Nothing else. If it grows a second instruction, that instruction belongs in
`AGENTS.md`.

### `modes/_shared.md`

The non-negotiables, loaded by every mode:

```markdown
# Shared rules

Before answering anything about current news:
1. Run `newsops status --json`.
2. If fetch is stale (>6h), run `newsops collect --max-age 6h --json`.

Never re-fetch a feed by hand; the CLI owns collection.
Never reproduce source paragraphs. Everything you write is written fresh.
Never publish anywhere. Drafts go to disk; the human posts them.
Prefer the primary source over coverage of it.
Record what happens with `newsops decide`.
Stop at each human gate. Do not brief unasked, and do not draft unasked.
```

### `.claude/commands/` (optional)

`collect.md`, `brief.md`, `draft.md` — each a single line pointing at the
corresponding mode file. No behavior.

## Acceptance criteria

- [ ] `CLAUDE.md` is one line.
- [ ] `AGENTS.md` and `modes/_shared.md` contain no Claude Code-specific
      syntax — no skills, no subagent references, no tool names. Grep for
      `Task(`, `subagent`, `Skill` and find nothing.
- [ ] Every subcommand documented in `AGENTS.md` matches the real binary:
      run each with `--help` and diff the flag list.
- [ ] Every JSON shape in `AGENTS.md` matches actual output from a real run.
- [ ] The two human gates appear explicitly in both `AGENTS.md` and
      `modes/_shared.md`.
- [ ] An agent handed only `AGENTS.md` can run `status` and `collect` and
      interpret both outputs without asking anything.

## Tests

Manual, in a session: start fresh, load `AGENTS.md` only, ask for the current
news. The agent should call `status`, then `collect`, without further
prompting — and should not brief anything.

## Out of scope

The mode files themselves (T019, T020, T022, T023).
