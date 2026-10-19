# Collect Mode — Skim and Digest

## Procedure

1. Read `modes/_shared.md`.
2. `newsops collect --max-age 6h --json`.
3. Read `context/interests.md` (the section marked `(current)`) and
   `context/beats.md`.
4. Skim all candidates. For each, judge: does this matter **to this person,
   given those two files** — not is it generally newsworthy.
5. Rank. Write `digest.md`.
6. `newsops decide <all candidate ids> shown --note "rank=<n>"` — one call
   with a comma-separated list, the note carrying each story's rank.
   Then, in a second batched call, record the stories you omitted or ranked
   below the digest cut: `newsops decide <ids> dismissed --note "<one-clause
   reason>"`. Group IDs that share a reason into one call.
7. **Stop.** Report the top few in the conversation and ask which are worth
   briefing. Do not brief anything unasked.

## Digest Format

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

## Rules

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
- A `dismissed` note is one clause, drawn from the same judgment as the reason
  line, never speculative ("funding round, on the avoid list"; "duplicate of
  4213"). Forty paragraphs of justification turn the skim into a research
  session.
