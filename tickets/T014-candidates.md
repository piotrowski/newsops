# T014 — `candidates` — prefilter

**Depends on:** T012, T005
**Size:** L
**Touches:** `internal/candidates/`, `cmd/newsops/candidates.go`

## Why

The agent's judgment is the ranking mechanism, but it cannot skim 300 items
per run and stay sharp. This command narrows ~300 to ~40 using only
deterministic, defensible rules — volume control, not quality judgment.

The ordering it emits is **presentation order, not a quality claim**. The
agent re-ranks against `interests.md`. Do not be tempted to sneak scoring in
here; that was v3's design and it was removed on purpose.

## Algorithm

In order:

1. **Window.** Stories with `last_seen >= ` the `started_at` of the last
   successful fetch. When there is no prior fetch, use
   `now - max_backfill_days`. Deliberately `last_seen`-based, not
   `published_at`: arXiv, aggregators and several vendor blogs publish null or
   plainly wrong dates, and `published_at` is display-only.
2. **Drop shown.** Exclude stories with `shown_at` set. This — not similarity
   scoring — is what stops the same story reappearing every day. Exact,
   trivial, no threshold.
3. **Drop excluded.** Exclude stories whose canonical item title contains any
   `exclude_terms` entry (case-insensitive substring).
4. **Order.** Tier ascending, then `source_count` descending, then `last_seen`
   descending, then `story_id` ascending. The final key makes the output
   deterministic, which the golden test depends on.
5. **Per-source cap.** Walking the ordered list, keep at most
   `per_source_cap` stories per canonical item's source. This is what stops
   arXiv and TechCrunch owning the list.
6. **Limit.** Take the first `candidate_limit`.

Opens `ReadOnly` — this command writes nothing, including no `shown_at`.
Marking a story shown is the agent's job via `newsops decide` (T017), after it
has actually put the story in a digest. Marking here would burn stories the
agent never saw if a session died mid-run.

Records a `runstate` row for stage `candidates`.

## Interface

```
newsops candidates [--json] [--limit N] [--since RFC3339] [--include-shown]
```

```json
{
  "generated_at": "2026-08-30T07:13:00Z",
  "window": {"since": "2026-08-29T06:55:00Z", "days": 1},
  "total_in_window": 231,
  "returned": 40,
  "dropped": {"shown": 158, "excluded": 4, "source_cap": 29},
  "candidates": [
    {
      "story_id": 4213,
      "title": "Cloudflare publishes postmortem on the 26 Aug outage",
      "url": "https://blog.cloudflare.com/2026/08/postmortem",
      "source": "Cloudflare",
      "tier": 1,
      "source_count": 4,
      "sources": ["Cloudflare", "Hacker News", "Techmeme", "The Register"],
      "published_at": "2026-08-30T05:40:00Z",
      "primary": true,
      "excerpt": "On 26 August at 14:02 UTC our edge began returning 522s for..."
    }
  ]
}
```

`sources` is the distinct source names on the story — the agent uses it to see
coverage breadth at a glance. `primary` is `config.IsPrimary(url)`.

Exit 0 always on success, **including when `candidates` is empty** — nothing
new is a normal outcome, not an error. Exit 2 on an unreadable database.

## Acceptance criteria

- [ ] Golden test: a seeded database of 60 stories produces an exact expected
      ordering and membership.
- [ ] A story with `shown_at` set never appears; `--include-shown` includes it.
- [ ] A title containing an `exclude_terms` entry is dropped and counted.
- [ ] With 10 arXiv stories and `per_source_cap: 3`, exactly 3 appear, and
      they are the first 3 in the ordering.
- [ ] The cap counts the **canonical item's** source, not every source on the
      story — an HN-boosted Cloudflare story counts against Cloudflare.
- [ ] `returned` never exceeds `candidate_limit`; `--limit` overrides it.
- [ ] An empty window returns `{"candidates": [], "returned": 0}` and exit 0.
- [ ] Two consecutive runs with no intervening writes produce byte-identical
      output.
- [ ] `excerpt` is present and non-empty for every candidate.
- [ ] Nothing is written: `shown_at` is unchanged after a run.

## Tests

Seeded in-memory database with a fixed clock. Golden file for the ordering
test. One test per acceptance line; the determinism test compares two
serializations byte for byte.

## Out of scope

Ranking by interest, and the digest itself — both belong to the agent (T019).
