# T015 — `collect` — the chained command

**Depends on:** T008, T012, T014
**Size:** M
**Touches:** `cmd/newsops/collect.go`

## Why

This is the agent's one command. Making it a single call means the mode files
don't have to encode stage ordering, error handling, or freshness logic — and
whatever the agent doesn't have to reason about, it can't get wrong.

The subtlety is the gating rule. A naive "skip everything that ran recently"
strands new items: fetch runs at hour 5, dedupe skips itself as fresh, and 300
items sit unassigned until both clocks happen to align.

> **A stage runs if its last success is older than `--max-age`, OR it has
> pending input.**

| Stage | Pending input |
|---|---|
| `fetch` | age only — it is the network stage, nothing upstream of it |
| `dedupe` | any item with `story_id IS NULL` |
| `candidates` | any story with `shown_at IS NULL` in the window |

## Behaviour

Run fetch → dedupe → candidates in order, each gated as above, each recording
its own `runstate` row.

**Exit-code propagation.** Continue past a stage that exits 1 — a partial
fetch is still worth reading — and abort immediately on 2. Return the worst
code any stage returned. A stage that was skipped contributes 0.

Takes the write lock **once** for the whole chain rather than per stage, so a
concurrent `collect` fails at the start rather than halfway through.

## Interface

```
newsops collect [--json] [--max-age 6h] [--limit N]
```

Output embeds each stage's own output object, so the agent can inspect any
part without a second call:

```json
{
  "exit": 0,
  "stages": {
    "fetch":  {"ran": true,  "skipped": false, "result": { /* T008 output */ }},
    "dedupe": {"ran": true,  "skipped": false, "result": { /* T012 output */ }},
    "candidates": {"ran": true, "skipped": false, "result": { /* T014 output */ }}
  },
  "candidates": [ /* the T014 candidate array, hoisted for convenience */ ]
}
```

A skipped stage is `{"ran": false, "skipped": true, "reason": "fresh", "result": null}`.

`candidates` is hoisted to the top level because it is what the agent actually
reads; the nested copy stays for diagnostics.

## Acceptance criteria

- [ ] All three stages run on a cold database.
- [ ] Immediately re-run with `--max-age 6h`: fetch is skipped, and dedupe and
      candidates are **also** skipped (no pending input) — exit 0.
- [ ] With fetch fresh but items having `story_id IS NULL`, dedupe **runs**
      anyway. This is the central test of this ticket.
- [ ] With everything fresh but unshown stories present, `candidates` still
      returns them — the agent must be able to re-read today's list after a
      session restart without refetching.
- [ ] Fetch exiting 1 (some feeds failed): dedupe and candidates still run,
      overall exit is 1, and `candidates` is populated.
- [ ] Fetch exiting 2: the chain aborts, later stages report `"ran": false`,
      overall exit 2.
- [ ] A second concurrent `collect` fails at the start with
      `{"error": "locked"}`, exit 2, having run no stages.
- [ ] The hoisted `candidates` array is identical to the nested one.

## Tests

`httptest` feeds plus a seeded database, driving each acceptance path. The
partial-failure and locked cases need explicit tests — they are the two that
break in real use.

## Out of scope

Anything the agent decides. `collect` never writes `shown_at`.
