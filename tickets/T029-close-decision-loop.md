# T029 — Close the decision loop: `posted` and `dismissed`

**Depends on:** T017, T028
**Size:** S
**Touches:** `modes/collect.md`, `modes/draft.md`, `modes/_shared.md`,
`.agents/skills/news-ops/SKILL.md`, `AGENTS.md`

## Why

`decide` accepts five actions — `shown`, `briefed`, `drafted`, `posted`,
`dismissed` (`AGENTS.md`, T017). The modes emit three:

```
modes/collect.md:12  decide <ids> shown --note "rank=<n>"
modes/brief.md:15    decide <id> briefed
modes/draft.md:10    decide <story_id> drafted
```

Nothing ever writes `posted` or `dismissed`. Two consequences:

**The funnel has no bottom.** `docs/REVIEW.md` measurement 4 expects
`500 shown → 15 briefed → 3 drafted → 1 posted` and reads the drafted:briefed
ratio as evidence about brief quality. The `posted` count is structurally
always zero, so the one number that says whether the pipeline produced
anything the human actually stood behind cannot be measured. A draft written
and abandoned is indistinguishable from a draft written and published — and
that difference is the entire output of the system.

**Rejection is invisible.** Every candidate is marked `shown` in one batch at
`modes/collect.md:12`, which suppresses repeats but records no signal about
*which* ones were rejected and why. `dismissed` with a note is the only place a
negative judgment could be captured, and it would be the highest-value column
in the archive for tuning `context/interests.md`.

This is a mode-file ticket. The CLI already accepts both actions; T017 built
them. Nothing calls them.

## What to build

### `posted` — at the human gate, not automatically

The human publishes by hand; the agent never does, and `modes/_shared.md:9`
("Never publish anywhere") stays exactly as written. So `posted` cannot be
recorded at draft time — at draft time nothing has been posted.

Add to `modes/draft.md`, after the existing step 5, an instruction to tell the
human that once they publish, `newsops decide <id> posted` closes the loop.
And add a routing row to `SKILL.md`: `posted <id>` → run
`newsops decide <id> posted --note "<url or empty>"`, confirm, stop. A bare
CLI call needs no mode file.

Recording the published URL in `--note` costs nothing and makes measurement 5
(voice drift, currently "diff what you published against what the agent
wrote") mechanically possible later. Do not build that diff here.

### `dismissed` — where the judgment already happens

`modes/collect.md` already ranks every candidate and already omits the
"avoid"-list entries with a count. That omission **is** a dismissal and it is
currently thrown away.

Extend `modes/collect.md` step 6: stories omitted or ranked below the digest
cut are recorded with `decide <ids> dismissed --note "<one-clause reason>"`
in a second batched call, alongside the existing `shown` call. The reason is
the point — `dismissed` with no note is a row nobody can learn from.

Constrain it in the mode's Rules section: the note is one clause, drawn from
the same judgment the reason line is drawn from, and never speculative. An
agent writing forty paragraphs of justification has turned the skim into a
research session, which is the failure `modes/collect.md` already guards
against elsewhere.

### Reflect both in the review

`newsops review` (T028) already surfaces `funnel.posted` and
`funnel.dismissed`. Once this ticket lands they stop being structurally zero.
Update `docs/REVIEW.md` measurement 4 to say what a healthy dismissed:shown
ratio looks like, and what a `posted` count of zero across two weeks means —
either the drafts are not good enough, or the human is not using the tool, and
the protocol should name both readings rather than assume the first.

## Acceptance criteria

- [ ] `modes/draft.md` tells the human how to record `posted` and does not
      record it itself.
- [ ] `modes/_shared.md:9` is unchanged — the agent still never publishes.
- [ ] `SKILL.md` has a `posted <id>` routing row.
- [ ] `modes/collect.md` records `dismissed` in one batched call with a
      per-story note.
- [ ] The dismissal note rule is stated in `modes/collect.md` Rules: one
      clause, from the skim, never speculative.
- [ ] `docs/REVIEW.md` measurement 4 interprets both new numbers.
- [ ] `grep -rn 'Skill\|subagent\|Task(' modes/ AGENTS.md` still returns
      nothing — T018's harness-neutrality criterion, still binding.

## Tests

Manual, in a fresh session, since this is mode text and not Go:

1. Run collect. Confirm two `decide` calls, one `shown` and one `dismissed`,
   and that the dismissed notes are one clause each.
2. Run draft. Confirm it records `drafted`, states that the draft is a
   starting point, and asks the human to record `posted` after publishing —
   without publishing anything.
3. `newsops decide <id> posted --note "https://..."`, then
   `newsops review --json`, and confirm `funnel.posted` is 1.

## Out of scope

- Any Go change. The CLI accepts all five actions today.
- A `publish` command. `docs/REVIEW.md` explicitly defers it until a
  measurement demands it, and this ticket is what makes that measurement
  possible.
- Automating the voice-drift diff. Recording the URL enables it; building it
  is separate.
