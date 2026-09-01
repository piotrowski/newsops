# T021 — `context/` templates

**Depends on:** T001
**Size:** S
**Touches:** `context/interests.md`, `context/beats.md`

## Why

`interests.md` is the actual product. Everything else in this repo is
scaffolding around it: the agent's entire notion of what is worth your
attention comes from this one file, read fresh every run.

It is prose, not weights, deliberately. A paragraph gets rewritten in thirty
seconds when your interests shift; a set of numeric weights takes an evening
to retune and you will never do it.

## Deliverables

### `context/interests.md`

Dated sections, appended, never overwritten. Exactly one carries `(current)`.

```markdown
# Interests

Rewrite the current section whenever your attention shifts. Add a new dated
section rather than editing an old one — the archived sections are what let
the review ask whether what you said you cared about matches what you actually
briefed. They usually differ, and the gap is the interesting part.

Be specific. "AI" ranks everything and therefore nothing. "Where retrieval
beats fine-tuning for agent memory" is a filter.

## 2026-09 (current)

Agent memory and context engineering — specifically where retrieval beats
fine-tuning, and what breaks at long context. Watching eval tooling
consolidate. Interested in postmortems from anyone operating at scale,
particularly the ones that name the real cause instead of "human error".

## 2026-06 (archived)

Inference cost curves, quantization, the economics of serving open models.
```

### `context/beats.md`

```markdown
# Beats

## Standing topics
Things you return to. A story touching one of these starts ahead.

- Distributed systems failure modes and postmortems
- Postgres and the storage layer generally
- Developer tooling that changes how work feels, not just benchmarks
- The operational reality of running LLMs in production

## Avoid
Things that are technically in your field but that you have no interest in
posting about. The agent ranks these last or omits them with a count.

- Funding rounds and valuations
- Leadership changes and org charts
- Conference announcements without technical content
- Benchmark leaderboard shuffles
- Anything whose only hook is a company name
```

Both files ship with this example content filled in, not as empty stubs —
a blank template gets left blank, and a filled one gets edited on first read.

## Acceptance criteria

- [ ] `context/interests.md` has exactly one section marked `(current)`.
- [ ] Section headers are `## YYYY-MM` and sort chronologically.
- [ ] The file explains why to append rather than overwrite, in the file
      itself — the instruction has to live where the editing happens.
- [ ] `context/beats.md` has both a standing-topics list and an avoid list,
      each with at least four entries.
- [ ] Both files are readable by `modes/collect.md` (T019) with no parsing —
      they are prose the agent reads, not config the CLI parses.

## Tests

None automated. Verification is that `modes/collect.md` produces reason lines
that visibly reference this content rather than generic newsworthiness.

## Out of scope

Any code that parses these files. Nothing in `internal/` reads `context/` —
if that changes, the file has become config and should move to `config/`.
