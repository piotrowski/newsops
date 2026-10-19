---
name: newsops-sources
description: >-
  Diagnose failing or stale feeds, add or fix a source in
  config/sources.yml, and clear a stuck pipeline. Use when sources:check or
  fetch reports failures, status shows a stage stale, a command returns
  `locked`, or the user wants to add, remove or re-tier a feed.
user_invocable: true
argument-hint: "[check | add <name> <url> | triage <name> | unstick]"
---

# newsops sources: feed health and configuration

Commands and their JSON are described in the newsops-cli skill and
`AGENTS.md`. This file is the triage procedure and the config rules.

## Triage a failing feed

1. `newsops sources:check --json` (all feeds, network, writes nothing) or
   `newsops sources:check --source "<name>" --json` for one.
2. Classify each `results[].status`:

   | status | meaning | usual fix |
   |---|---|---|
   | `ok` | 2xx, parsed, ≥1 item | nothing |
   | `http_error` | non-2xx (`http_status` says which) | 404/410: URL moved, find the new feed URL; 403/429: the site blocks the user agent or rate; 5xx: wait a day |
   | `network_error` | DNS, TLS, timeout (15s) | transient unless it repeats across days; slow hosts are not worth a longer timeout |
   | `parse_error` | body is not RSS/Atom/RDF | the URL is an HTML page, not the feed; look for `<link rel="alternate" type="application/rss+xml">` |
   | `empty` | parsed, zero items | feed is alive but dormant; keep unless it stays empty for weeks |

3. Confirm with the real path: `newsops fetch --source "<name>" --json` and
   read `failures[].error` and `items.new`. `sources.not_modified: 1` means
   the feed answered 304 and nothing changed, which is healthy.
4. One failing feed is exit 1, not a reason to stop the pipeline. Report it
   and move on. Remove a feed only after it has failed the same way for more
   than a week.

## Edit `config/sources.yml`

```yaml
sources:
  - name: hacker-news-digest   # unique, free-form; used in output and --source
    tier: 1                    # 1..3; lower sorts first in candidates
    url: https://hnrss.org/frontpage   # http or https, must parse
```

Rules enforced at load (any violation is `config_error`, exit 2, and every
command stops): names non-empty and unique, tier in 1..3, URL http(s) with a
host. Validate with `newsops sources:check --source "<name>" --json` before
committing.

- **Tier** is a ranking input, not a trust score: tier 1 candidates come first,
  then more sources, then recency. Primary vendors and official blogs are
  tier 1, aggregators tier 2, commentary tier 3.
- **Aggregators** (HN, Lobsters, Reddit, Techmeme) are detected by feed host
  (`internal/dedupe/canonical.go`, `aggregatorHosts`), with a name fallback
  (`hacker`, `lobste`, `reddit`, `techmeme`). Keep the real host in the URL
  so discussion links get attached to stories.
- **`config/domains.yml`** `primary_domains`: hosts whose articles win the
  canonical slot in a story and are flagged `primary: true` in candidates.
  Add the vendor domains behind your tier-1 feeds.
- **`config/prefilter.yml`**: `candidate_limit` (default 40), `per_source_cap`
  (3), `title_simhash_max` (3; raise to 4 if near-duplicate titles leak,
  lower if unrelated stories merge), `max_backfill_days` (30; first-run window),
  `exclude_terms` (substring match on canonical titles, case-insensitive).
  Changes apply on the next command; nothing is cached.

## Stale or stuck pipeline

- `newsops status --json`: `stages.fetch.stale: true` after 6h or when no
  fetch has ever succeeded. `collect --max-age 6h` fixes it. `--max-age 0s`
  forces every stage.
- **`{"error":"locked","pid":N,"held_since":...}`**: another newsops owns
  `data/news.lock`. Check whether `pid` is alive. If it is, wait; a full
  collect takes under a minute. If it is not, the lock is free already
  (flock releases on process exit) and the retry will succeed. Never delete
  `data/news.lock` by hand.
- **Fetch window caveat.** The window starts at the last *successful* fetch,
  so a single feed that was down during a successful run loses the items it
  published then (they arrive later as `items.dropped_old`). Backfill with
  `newsops fetch --source "<name>" --since <RFC3339> --json`, then
  `newsops dedupe --json`. A run where *every* feed failed is not recorded as
  a success and does not move the window.
- **Raw feed bodies** are cached under `data/<sha1(url)>/<date>.xml` on each
  fetch and never pruned. Safe to delete; nothing reads them back.

## Make targets

- `make sources` runs `sources:check` against the live feed list. It is
  deliberately not part of `make check`: feeds rot independently of the code.
- `make check` (build, unit, e2e, symlink checks) is offline.
