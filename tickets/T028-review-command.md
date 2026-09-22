# T028 — `review` subcommand: make the two-week protocol runnable

**Depends on:** T017, T024, T026 (measurement 1 needs `--note` to work)
**Size:** M
**Touches:** `cmd/newsops/review.go`, `cmd/newsops/main.go`, `docs/REVIEW.md`,
`AGENTS.md`, `.agents/skills/news-ops/SKILL.md`

## Why

`docs/REVIEW.md` is five measurements, three of which are raw SQL against
`data/news.db`. Both `modes/_shared.md:12` and the skill router say, verbatim:

> Never run raw SQL (sqlite3) or access `data/news.db` directly — the CLI is
> the only correct interface.

So an agent that follows the shared rules must refuse to run the project's own
review protocol. The document is also unreachable: grep `review` across
`AGENTS.md` and `SKILL.md` and there are no hits — no mode, no routing row, no
mention in "Where Things Live". T024 produced a document that nothing points
at and nothing is allowed to execute.

This matters more than a docs inconsistency. The review protocol is the only
mechanism that asks whether the ranking is tracking the human at all. If it is
the one thing never run, the system has no feedback loop and the digest
quality drifts unobserved — which is precisely the failure T024 existed to
prevent.

The rule itself is right and stays. Direct SQL means an agent inventing joins
against a schema it half-remembers, and silently wrong numbers are worse than
no numbers. The fix is to give the protocol a CLI surface.

## What to build

### `newsops review [--json] [--days 14]`

One read-only command emitting every machine-computable measurement in one
object. `ReadOnly`, no lock — it must run while a collect holds the write lock.

```json
{
  "window": {"days": 14, "since": "2026-09-08T00:00:00Z"},
  "rank_accuracy": [
    {"rank": 1, "shown": 14, "briefed": 6},
    {"rank": 2, "shown": 14, "briefed": 4}
  ],
  "funnel": {"shown": 487, "briefed": 15, "drafted": 3, "posted": 1, "dismissed": 22},
  "prefilter_dropped": [
    {"story_id": 4301, "title": "...", "source": "...", "last_seen": "..."}
  ],
  "totals": {"digests": 14, "stories_in_window": 612}
}
```

Mapping to the protocol's five sections:

| § | Measurement | Surface |
|---|---|---|
| 1 | Rank predicted picks | `rank_accuracy` — parses `rank=<n>` from `shown` notes, left-joins `briefed` |
| 2 | Duplicate leakage | stays manual; needs reading digests, not the DB |
| 3 | What the prefilter threw away | `prefilter_dropped` — unshown stories in window, capped at 50 |
| 4 | The funnel | `funnel` — distinct story ids per action |
| 5 | Voice drift | stays manual; needs the published text, which the DB never sees |

Sections 2 and 5 are genuinely human work. Say so in the output or the docs
rather than inventing a number for them.

**`rank_accuracy` depends on T026.** `decide --note` is unreachable today, so
no `rank=` note has ever been written and this array will be empty on any
existing database. That is correct behaviour, not a bug — but emit
`"rank_accuracy": []` with a `notes` field explaining the cause rather than an
ambiguous empty array, or the first person to run this will chase a phantom.

### Rewrite `docs/REVIEW.md` against the command

Each section keeps its prose and its **Read it** paragraph — that interpretive
guidance is the value of the document and none of it changes. Replace the SQL
blocks with the `newsops review --json` field to read, and mark sections 2 and
5 explicitly as manual. Keep the "Deferred, Pending These Numbers" closing
section as it stands.

### Route it

- `AGENTS.md`: a `### \`review\`` entry in the CLI reference, and
  `docs/REVIEW.md` added to "Where Things Live".
- `SKILL.md`: one routing row — `review` → run `newsops review --json`, read
  `docs/REVIEW.md` for interpretation, report, stop. It is a reporting mode,
  not a judgment mode, so it needs no file in `modes/`.

## Acceptance criteria

- [ ] `newsops review --json` exits 0 on a seeded database and returns all
      five top-level keys.
- [ ] `rank_accuracy` groups correctly across `rank=1`..`rank=40` and counts a
      story briefed twice once.
- [ ] Notes that do not match `rank=<int>` are ignored, not parsed as 0.
- [ ] `funnel` counts distinct story ids, so a story shown on three days
      counts once.
- [ ] `prefilter_dropped` returns only `shown_at IS NULL` stories inside the
      window, capped at 50.
- [ ] `--days` moves every window; default is 14.
- [ ] Runs while a write lock is held.
- [ ] `docs/REVIEW.md` contains no SQL and no reference to `sqlite3` or
      `data/news.db`.
- [ ] `grep -rn 'review' AGENTS.md .agents/skills/news-ops/SKILL.md` returns
      hits.

## Tests

Seeded in-memory database with a known decision history: one story shown at
rank 1 and briefed, one shown at rank 1 and not briefed, one briefed twice,
one with a malformed note, one unshown inside the window and one outside it.
Assert each number. The malformed-note row and the briefed-twice row are the
two that a naive implementation gets wrong.

## Out of scope

- Any judgment about what the numbers mean — that is `docs/REVIEW.md` prose
  and it is already written.
- Acting on the findings. The protocol's closing section is explicit that
  semantic dedupe, embedding-based scoring and a `publish` command are not
  planned until a measurement demands them. This ticket ships the
  measurement, nothing downstream of it.
- Measurements 2 and 5. They need artifacts the database does not hold.
