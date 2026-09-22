# News-ops — Personal News Pipeline

A personal news pipeline: the agent judges, researches, and drafts; the CLI collects and remembers; the human decides. Three sentences.

## Getting started

```
make install          # builds and copies to ~/.local/bin/newsops
export PATH=$HOME/.local/bin:$PATH   # or wherever PREFIX points
newsops version       # verify: real version and commit, not dev/none
```

The skill router at `.agents/skills/news-ops/SKILL.md` is the entrypoint for agentic invocation. The mode table below is the contract it routes to.

## The Loop

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

## CLI Reference

### Global Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--json` | false | Machine-readable JSON output (stdout) |
| `--db <dir>` | `data/` | Database directory |
| `--config <dir>` | `config/` | Configuration directory |
| `-v, --verbose` | false | Verbose output to stderr |

### Exit Codes

| Code | Meaning | Action |
|------|---------|--------|
| 0 | Success or skipped | Continue |
| 1 | Partial — some feeds failed but output is usable | **Continue** (exit 1 is not a reason to stop) |
| 2 | Hard failure | Stop |

### `status`

```
newsops status [--json]
```

Shows pipeline stage freshness, item counts, and digest file info.

```json
{
  "stages": {
    "fetch": {"last_success": "...", "age_min": 45, "stale": false},
    "dedupe": {"last_success": "...", "age_min": 30, "stale": false},
    "candidates": {"last_success": null, "age_min": null, "stale": true}
  },
  "counts": {"items_total": 12847, "new_since_fetch": 342, "unshown_stories": 40},
  "digest": {"path": "digest.md", "generated_at": "..."}
}
```

### `sources:check`

```
newsops sources:check [--json] [--source NAME]
```

Verifies all configured feeds are reachable. Returns per-source HTTP status and content type.

### `fetch`

```
newsops fetch [--json] [--source NAME] [--max-age DURATION] [--since RFC3339] [--no-cache]
```

Collects articles from configured RSS/Atom feeds. Gated by `--max-age` (default 6h). Records ETag/Last-Modified for conditional requests.

```json
{
  "window": {"since": "...", "backfilled": false},
  "sources": {"total": 51, "ok": 48, "not_modified": 30, "failed": 3},
  "items": {"seen": 1200, "new": 342, "dropped_old": 15},
  "failures": [{"source": "Example", "error": "timeout"}]
}
```

### `dedupe`

```
newsops dedupe [--json] [--window DURATION] [--limit N]
```

Assigns unassigned items to stories (URL match first, then title similarity). Creates/joins stories, inserts story_sources rows.

```json
{
  "items_in": 342,
  "stories_created": 28,
  "stories_updated": 15,
  "methods": {"url": 20, "title": 8},
  "discussions_linked": 12
}
```

### `candidates`

```
newsops candidates [--json] [--since RFC3339] [--include-shown] [--limit N]
```

Prefilters stories: window → drop shown → drop excluded terms → order by tier/source_count/last_seen/id → per-source cap → limit. Returns presentation-order candidate list.

```json
{
  "generated_at": "...",
  "window": {"since": "...", "days": 1},
  "total_in_window": 231,
  "returned": 40,
  "dropped": {"shown": 158, "excluded": 4, "source_cap": 29},
  "candidates": [
    {
      "story_id": 4213,
      "title": "...",
      "url": "...",
      "source": "Cloudflare",
      "tier": 1,
      "source_count": 4,
      "sources": ["Cloudflare", "Hacker News"],
      "published_at": "...",
      "primary": true,
      "excerpt": "..."
    }
  ]
}
```

### `collect`

```
newsops collect [--json] [--max-age DURATION] [--limit N]
```

Chained fetch → dedupe → candidates. Each stage runs if its last success is older than `--max-age` OR it has pending input (unassigned items for dedupe, unshown stories for candidates). Exit-code propagation: continue past 1, abort on 2. Takes write lock once.

```json
{
  "exit": 0,
  "stages": {
    "fetch": {"ran": true, "skipped": false, "result": {...}},
    "dedupe": {"ran": true, "skipped": false, "result": {...}},
    "candidates": {"ran": true, "skipped": false, "result": {...}}
  },
  "candidates": [...]
}
```

### `query`

```
newsops query [--json] [--text "..."] [--domain example.com] [--story N --related] [--days 90] [--limit 20]
```

FTS5 archive search. At least one of `--text`, `--domain`, or `--story --related` required. Results are stories ranked by FTS5 rank with `<b>`-delimited snippets.

```json
{
  "query": {"text": "cloudflare outage", "days": 90, "limit": 20},
  "total": 7,
  "results": [
    {
      "story_id": 3907,
      "title": "...",
      "url": "...",
      "source": "Cloudflare",
      "published_at": "...",
      "first_seen": "...",
      "source_count": 6,
      "snippet": "...<b>outage</b>..."
    }
  ]
}
```

### `decide`

```
newsops decide <story_ids> <action> [--note "..."]
```

Records actions on stories (shown/briefed/drafted/posted/dismissed). Appends decisions rows. `shown` sets `stories.shown_at` only if currently null. Validates all IDs before writing — exits 2 with no changes if any ID is unknown.

```json
{
  "action": "shown",
  "applied": [4213, 4198],
  "already_shown": [4155],
  "unknown": []
}
```

### `show`

```
newsops show <story_id> [--json]
```

Displays a single story: story row, all member items, all story_sources, and full decision history.

```json
{
  "story": {"id": 4213, "source_count": 4, "method": "url", "first_seen": "...", "last_seen": "...", "shown_at": "..."},
  "canonical": {"item_id": 88213, "title": "...", "url": "...", "source": "Cloudflare", "tier": 1, "published_at": "...", "excerpt": "..."},
  "items": [...],
  "sources": [{"source": "Hacker News", "url": "...", "kind": "discussion"}],
  "decisions": [{"action": "shown", "note": "rank=1", "at": "..."}]
}
```

### `version`

```
newsops version [--json]
```

Shows build version and commit hash.

## Where Things Live

| Path | Purpose |
|------|---------|
| `config/` | sources.yml, domains.yml, prefilter.yml |
| `context/` | interests.md, beats.md, voice/ corpus |
| `modes/` | collect.md, brief.md, draft.md, voice.md, _shared.md |
| `briefs/` | Research briefs: `<id>-<slug>.md` |
| `posts/` | Draft posts: `<date>-<slug>-<target>.md` |
| `data/` | Runtime: news.db, news.lock (gitignored) |
| `digest.md` | Daily digest output |

## Which Mode to Load

| Task | Mode file |
|------|-----------|
| Collect/digest | `modes/collect.md` |
| Research/brief | `modes/brief.md` |
| Draft post | `modes/draft.md` |
| Voice corpus | `modes/voice.md` |

Always read `modes/_shared.md` first.
