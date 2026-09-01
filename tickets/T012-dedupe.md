# T012 — `dedupe` — story assignment

**Depends on:** T010, T011, T004
**Size:** L
**Touches:** `internal/dedupe/assign.go`, `cmd/newsops/dedupe.go`

## Why

This turns a pile of items into the unit everything downstream addresses: a
**story**. The digest numbers them, briefs are named after them, and your
recorded decisions reference them.

Which is why the central requirement here is not the matching logic — it is
the invariant:

> **Dedupe is incremental and append-only. A story ID is never reused and
> never renumbered.** An existing row may have `source_count`, `last_seen`,
> and `canonical_item_id` updated. Nothing else.

If a re-run ever renumbered, every brief on disk and every row in `decisions`
would silently point at a different story, and nothing in the system would
tell you.

## Behaviour

Process items with `story_id IS NULL`, oldest `first_seen` first, in a single
transaction per batch.

For each item:

1. **Canonicalize** — compute `canonical_url` via `dedupe.Canonical` (T010)
   and store it on the item. For aggregators, use `ResolveAggregator`: the
   article URL becomes `canonical_url`, and the discussion link is remembered
   for step 5.
2. **Layer 1 — URL match.** Look for an existing story having any item with
   the same `canonical_url`. On a hit, join it with `method` unchanged.
3. **Layer 2 — title match.** Among stories whose `last_seen` is within 48h,
   compare `SimilarTitles` (T011) against the story's canonical item title at
   `title_simhash_max`. First match wins; join with `method = "title"` if the
   story was created by URL, otherwise leave `method` as is.
4. **No match** — create a story: `canonical_item_id` = this item,
   `source_count` = 1, `method` = `"url"`, `first_seen` = `last_seen` = the
   item's `first_seen`.
5. **Update the story** — set the item's `story_id`; recompute `source_count`
   as the count of **distinct sources** across its items plus its
   `story_sources` rows; set `last_seen` = max of member `first_seen`; insert
   `story_sources` rows for the article (source name + canonical URL,
   `kind = "article"`) and, when present, the discussion link
   (`kind = "discussion"`).
6. **Recompute `canonical_item_id`** — earliest non-null `published_at` among
   member items; ties and all-null broken by preferring an item whose host is
   in `domains.yml` `primary_domains`, then by lowest item id (deterministic).

Opens the database `ReadWrite`. Records a `runstate` row for stage `dedupe`
with `items_in` = items processed, `items_out` = stories touched.

## Interface

```
newsops dedupe [--json] [--window 48h] [--limit N]
```

```json
{
  "items_in": 287,
  "stories_created": 231,
  "stories_updated": 56,
  "methods": {"url": 40, "title": 16},
  "discussions_linked": 22
}
```

Exit 0 on success, 2 on a locked database or a transaction failure. There is
no partial state: the batch commits or it does not.

## Acceptance criteria

- [ ] An HN item plus the article it links to produce **one** story with
      `source_count: 2`, `method: "url"`, and a `story_sources` row with
      `kind: "discussion"` for the HN thread.
- [ ] Two outlets with differing headlines about one event, within 48h,
      produce one story with `method: "title"`.
- [ ] The same two headlines more than 48h apart produce **two** stories.
- [ ] Running `dedupe` twice over identical data: the second run reports
      `items_in: 0`, creates nothing, and changes no story ID.
- [ ] Adding a third item to an existing story raises `source_count` to 3 and
      moves `last_seen`, without changing the story's `id` or `first_seen`.
- [ ] `canonical_item_id` prefers the earliest `published_at`; with equal
      dates it prefers the `domains.yml` primary domain.
- [ ] Two items from the *same* source in one story count as `source_count: 1`
      — it measures breadth of coverage, not item count.
- [ ] No code path issues `UPDATE stories SET id`, `DELETE FROM stories`, or
      resets the AUTOINCREMENT sequence. Assert this with a test that records
      all story IDs, re-runs dedupe over new data, and asserts the original
      IDs are all still present and still point at the same canonical URL.

## Tests

Seeded in-memory database, one test per acceptance line. The idempotence test
and the ID-stability test are the two that must never be skipped or
weakened — they guard the invariant.

## Out of scope

Selecting which stories to show (T014). Any semantic or embedding-based third
layer: not planned. If duplicates visibly leak, the cheapest fix is letting
the agent merge them while it skims — it is already reading all 40 titles.
