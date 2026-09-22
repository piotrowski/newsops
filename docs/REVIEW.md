# Two-Week Review Protocol

Run this after two weeks of real use. Five measurements, each with the query
and what to do about the answer.

## 1. Did the ranking predict your picks?

The agent records its rank as `rank=<n>` on each `shown` decision (T019).

```sql
SELECT
  CAST(substr(d.note, 6) AS INTEGER) AS rank,
  COUNT(*) AS shown,
  SUM(CASE WHEN b.story_id IS NOT NULL THEN 1 ELSE 0 END) AS briefed
FROM decisions d
LEFT JOIN decisions b ON b.story_id = d.story_id AND b.action = 'briefed'
WHERE d.action = 'shown' AND d.note LIKE 'rank=%'
GROUP BY rank ORDER BY rank;
```

**Read it:** briefs should cluster in ranks 1–5. If they are uniform across
ranks, the agent's ranking is not tracking you — and the fix is
`context/interests.md` being too vague, not the prompt.

## 2. Duplicate leakage

Manual: read the last 14 digests and count entries that were the same story
twice.

**Read it:** under ~5% of digest slots and the two dedupe layers are
sufficient permanently. Above it, look at *which* kind leaked before building
anything — near-identical titles outside the 48h window are a window problem,
not a matching problem.

## 3. What the prefilter threw away

```sql
SELECT s.id, i.title, i.source, s.last_seen
FROM stories s JOIN items i ON i.id = s.canonical_item_id
WHERE s.shown_at IS NULL AND s.last_seen > datetime('now','-14 days')
ORDER BY s.last_seen DESC LIMIT 50;
```

Stories that existed and never reached a digest. Spot-check 50.

**Read it:** if good stories are dying at the `candidate_limit: 40` cut, raise
the cap before touching anything else. It is one number and costs only the
agent's skim.

## 4. The funnel

```sql
SELECT action, COUNT(DISTINCT story_id) FROM decisions
WHERE at > datetime('now','-14 days') GROUP BY action;
```

**Read it:** expect roughly 500 shown → 15 briefed → 3 drafted → 1 posted. A
briefed:shown ratio near zero means the digest is not surfacing things you
want. A drafted:briefed near zero means the briefs are not convincing — check
whether they are finding real archive connections or writing
"no prior coverage found" every time.

## 5. Voice drift

Manual: for each draft you posted, diff what you published against what the
agent wrote.

**Read it:** if you make the *same* edit every time, that is a missing rule in
`context/voice-guide.md` section 6. Add it by hand. Rules you add survive as
long as you copy them out before regenerating (T022).

## Deferred, Pending These Numbers

State explicitly at the bottom: semantic dedupe, embedding-based interest
scoring, a `publish` command, and any web UI are **not planned** and should be
built only if a measurement above says so.
