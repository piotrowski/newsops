# T019 — `modes/collect.md` — skim and digest

**Depends on:** T018, T015
**Size:** M
**Touches:** `modes/collect.md`

## Why

The digest is the daily product. A bare list of headlines is what an RSS
reader already gives you — the value added here is **a reason per line**,
written against what this specific person cares about right now.

This mode also owns the first human gate. It must stop after writing the
digest. An agent that helpfully starts researching burns tokens on things you
would not have picked, and trains you to stop reading its output.

## Deliverable

`modes/collect.md`, specifying:

**Procedure**

1. Read `modes/_shared.md`.
2. `newsops collect --max-age 6h --json`.
3. Read `context/interests.md` (the section marked `(current)`) and
   `context/beats.md`.
4. Skim all candidates. For each, judge: does this matter **to this person,
   given those two files** — not is it generally newsworthy.
5. Rank. Write `digest.md`.
6. `newsops decide <all candidate ids> shown --note "rank=<n>"` — one call
   with a comma-separated list, the note carrying each story's rank.
7. **Stop.** Report the top few in the conversation and ask which are worth
   briefing. Do not brief anything unasked.

**Digest format**

```markdown
# Digest — 2026-08-30
_287 new items → 40 candidates_

1. **[4213]** Cloudflare postmortem on the 26 Aug outage · cloudflare.com · 4 sources
   Primary source, and you covered their July incident — this is the follow-up.
2. **[4198]** Postgres 18 ships async I/O · postgresql.org · 2 sources
   Directly in your infra beat; the benchmark numbers are the story, not the release.
```

Each entry: rank, story ID in brackets, title, domain, source count, and **one
line saying why it might matter to you**. The reason line is the whole point;
an entry without one is a failed entry.

**Rules to state explicitly**

- Rank against `interests.md` and `beats.md`, not general importance.
- Anything in the "avoid" list in `beats.md` goes last or is omitted, with a
  count of what was omitted.
- Flag follow-ups: when a story looks connected to something previously
  covered, say so. Use `newsops query --text "<key terms>" --days 90` to
  check rather than guessing — but at most 3 such queries per run, or the
  skim becomes a research session.
- Never invent detail beyond the excerpt. If the excerpt is too thin to judge,
  say so in the reason line rather than guessing.
- Write the whole digest even when many candidates are dull — omitting them
  silently hides that the source list needs work.

## Acceptance criteria

- [ ] Running the mode on real data produces `digest.md` with a reason line
      for every entry.
- [ ] It calls `newsops status` or `collect` **first**, before anything else.
- [ ] It calls `decide ... shown --note "rank=<n>"` for every listed story,
      and a subsequent `collect` returns none of them again.
- [ ] It stops after the digest and asks which to brief — no `briefs/` file is
      created by this mode, ever.
- [ ] Reason lines reference `interests.md` or `beats.md` content, not generic
      newsworthiness. Judge this by reading five of them.
- [ ] With an empty candidate list, it says so plainly and does not invent
      entries or re-list yesterday's.
- [ ] Story IDs in `digest.md` match real ids in the database.

## Tests

Manual, twice: once on a full candidate list, once on an empty one. Then check
`sqlite3 data/news.db "SELECT story_id, note FROM decisions WHERE action='shown' ORDER BY at DESC LIMIT 5"`
shows rank notes.

## Out of scope

Researching anything (T020). Ranking inside the CLI — `candidates` emits
presentation order only, and the agent re-ranks.
