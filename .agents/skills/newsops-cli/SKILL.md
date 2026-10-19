---
name: newsops-cli
description: >-
  How and when to run each `newsops` subcommand and read its JSON. Use
  whenever you are about to call `newsops` directly, need to interpret its
  output or exit code, or a mode file tells you to run a command you have
  not used before. Not for editorial judgment (ranking, briefing, drafting):
  that lives in the news-ops skill and modes/.
user_invocable: true
argument-hint: "[status | collect | fetch | dedupe | candidates | query | show | decide | sources:check | version]"
---

# newsops command cookbook

Exact flags and full JSON shapes are in `AGENTS.md` → "CLI Reference". This
file says *when* to use each command and *which fields to read*. Where the two
disagree, `AGENTS.md` wins and this file is stale.

## Before any command

- **Project root.** Walk upward from this file to the nearest directory holding
  both `AGENTS.md` and `modes/`. Run every command from there, or pass
  `--db <root>/data --config <root>/config` explicitly. The defaults are
  cwd-relative.
- **Binary.** Prefer `newsops` on `PATH`; fall back to `<root>/newsops`. If
  neither exists, stop and point at `make install`. Never `go run` it.
- **Always `--json`.** Human output is indented JSON with extra stderr lines;
  `--json` is the stable contract.
- **Never raw SQL.** `data/news.db` is reached only through these commands.

## Contracts that apply to every command

| Exit | Meaning | Do |
|---|---|---|
| 0 | success, or skipped as fresh | continue |
| 1 | partial: some feeds failed, output is usable | **continue**; mention the failures |
| 2 | hard failure or bad arguments | stop, read `message` |

- **Error envelope** on stdout with `--json`: `{"error":"<slug>","message":"..."}`
  plus extra fields. Slugs you will meet: `partial_fetch` (continue),
  `check_failures` (continue), `locked` (another newsops holds the write lock;
  `pid` and `held_since` say who; wait and retry), `unknown_story`,
  `invalid_argument`, `config_error`, `unknown_command`, `internal`.
- **Write lock.** `collect`, `fetch`, `dedupe`, `candidates` and `decide` take
  `data/news.lock` for their duration. `status`, `query`, `show` and `version`
  never block and never create the database.
- **Timestamps** in output are RFC3339 UTC. `--since` flags take RFC3339.
- **`shown` is irreversible.** `decide ... shown` sets `stories.shown_at` once,
  and shown stories leave the candidate pool for good. Only collect mode
  records it, after the human-facing digest is written.

## Commands

### `status`
- **Use when:** first thing in any session; before deciding whether to
  collect; after a command returned `locked` or exit 2; to confirm a stage ran.
- **Don't use when:** you need the candidate list (`status` has counts only).
- **Run:** `newsops status --json`
- **Read:** `stages.fetch.stale` (true → data is older than 6h or never
  fetched), `stages.*.age_min`, `counts.unshown_stories` (what a collect would
  surface), `digest.generated_at` (null until a digest exists).
- **Then:** stale → `collect --max-age 6h`; fresh → `candidates` or carry on.

### `collect`
- **Use when:** the daily loop. Runs fetch → dedupe → candidates, each only if
  stale or holding pending input, under one lock. This is the only command
  collect mode needs.
- **Don't use when:** you only want to re-read the current candidate pool
  (`candidates`), or you want to force one stage (`fetch`, `dedupe`).
- **Run:** `newsops collect --max-age 6h --json` (`--max-age 0s` forces every
  stage; `--limit N` caps candidates; `--source NAME` restricts the fetch).
- **Read:** `exit` (mirrors the process exit code), `stages.<name>.skipped`
  with `reason` (`fresh`, `fetch_failed`, `dedupe_failed`),
  `stages.fetch.result.failures[]`, and `candidates[]` (same shape as the
  `candidates` command).
- **Then:** exit 1 → still skim `candidates[]`, note the failed feeds. Exit 2
  → read `stages.fetch.result.message`, see the newsops-sources skill.

### `fetch`
- **Use when:** you need to pull feeds without deduping or ranking: verifying a
  fixed source (`--source NAME`), backfilling a gap (`--since <RFC3339>`), or
  diagnosing a feed.
- **Don't use when:** you want candidates afterwards; use `collect` so dedupe
  and candidates run in the same lock.
- **Run:** `newsops fetch --json`, `newsops fetch --source "hacker-news-digest" --json`,
  `newsops fetch --since 2026-10-01T00:00:00Z --json`
- **Read:** `sources.ok/not_modified/failed`, `items.new`, `items.dropped_old`
  (published before the window start), `failures[].error`, `skipped: true,
  reason: fresh` when `--max-age` is set and the last run is recent.
- **Then:** `dedupe` if you want the new items assigned to stories now. A run
  where every source failed does not advance the fetch window.

### `dedupe`
- **Use when:** `status` shows items waiting (or `fetch` just ran outside
  `collect`); to replay story assignment with a different title window.
- **Don't use when:** nothing is unassigned; it is a no-op that still writes a
  run row.
- **Run:** `newsops dedupe --json` (`--window 24h` narrows the title match,
  `--limit N` processes a batch).
- **Read:** `items_in`, `stories_created`, `stories_updated`,
  `methods.url` / `methods.title` (how items joined existing stories),
  `discussions_linked` (HN/Lobsters/Reddit comment threads attached).
- **Then:** `candidates`.

### `candidates`
- **Use when:** you want the current unshown pool without fetching: re-reading
  after a collect, testing a `--limit`, or auditing what the prefilter dropped
  (`--include-shown`).
- **Don't use when:** data is stale (check `status` first). It takes the write
  lock briefly to record its run.
- **Run:** `newsops candidates --json`, `newsops candidates --include-shown --limit 100 --json`,
  `newsops candidates --since 2026-10-01T00:00:00Z --json`
- **Read:** `total_in_window`, `returned`, `dropped.shown/excluded/source_cap`,
  and per candidate `story_id`, `title`, `url`, `source_count`, `sources[]`,
  `primary` (URL is on a configured primary domain), `excerpt` (what you skim).
- **Then:** judgment happens in collect mode; `decide` records it.

### `query`
Archive search over titles and excerpts. Exactly one of three forms.
- **Use when:** "did we cover this before?", "what else from this domain?",
  "what is related to story N?" during collect or brief mode. Keep to the
  per-mode query budget (collect: 3, brief: 1 related + 2 text).
- **Don't use when:** you already have the story ID and want its contents
  (`show`).
- **Run:** `newsops query --text "cloudflare outage" --days 90 --json`,
  `newsops query --domain postgresql.org --days 180 --json`,
  `newsops query --story 4213 --related --json` (`--limit N`, default 20).
- **Read:** `total` (matches, independent of `--limit`), `results[]` with
  `story_id`, `title`, `url`, `source_count`, `snippet` (`<b>` marks the hit).
  Text terms are OR-ed; operators are stripped, so quote nothing.
- **Then:** `show <story_id>` for anything you intend to cite.

### `show`
- **Use when:** you need everything about one story: canonical item, every
  member item, article and discussion links, and the decision history.
  Brief mode starts here.
- **Don't use when:** you only need a title (the candidate or query row has it).
- **Run:** `newsops show 4213 --json`
- **Read:** `canonical.url` (the thing to fetch and read), `items[]` (other
  outlets' versions), `sources[]` with `kind: discussion` (HN thread etc.),
  `decisions[]` (has it been shown, briefed, drafted, posted, dismissed?).
- **Then:** `decide`, or research from `canonical.url`.

### `decide`
Append-only log of what happened to a story.
- **Use when:** a mode says so: `shown` (collect, batched, `--note "rank=N"`),
  `briefed` (brief), `drafted` (draft), `dismissed` (collect, one-clause reason),
  `posted` (the human, after publishing by hand, `--note "<url>"`).
- **Don't use when:** you are unsure of the ID; the command refuses the whole
  batch if any ID is unknown, so nothing half-applies.
- **Run:** `newsops decide 4213,4198 shown --note "rank=1" --json`,
  `newsops decide 4213 briefed --json`, `newsops decide 4213 posted --note "https://..." --json`.
  IDs may be space-separated or one comma-separated list. One `--note` per call.
- **Read:** `applied[]`, `already_shown[]` (only for `shown`; those were
  skipped), `unknown[]` (non-empty means exit 2 and no writes).
- **Then:** nothing; the log is the output.

### `sources:check`
- **Use when:** a fetch reported failures, before editing `config/sources.yml`,
  or monthly feed-rot maintenance. Hits every feed over the network; not part
  of `make check`.
- **Don't use when:** you want items stored (`fetch`). This writes nothing.
- **Run:** `newsops sources:check --json`, `newsops sources:check --source "FeedName" --json`
- **Read:** `passed`, `failed`, `results[].status` ∈ `ok`, `http_error`,
  `network_error`, `parse_error`, `empty`, plus `http_status` and `items`.
- **Then:** see the newsops-sources skill for what each status means.

### `version`
- **Use when:** reporting a bug, or checking the installed binary is current
  after `make install`. `dev`/`none` means it was built without `make`.
- **Run:** `newsops version --json` → `version`, `commit`.

## Common sequences

- **Is the data fresh?** `status` → if `stages.fetch.stale` → `collect --max-age 6h`.
- **Did we cover X?** `query --text "x" --days 90` → `show <id>` on hits →
  cite the story ID in the digest reason line or the brief.
- **Inspect before recording.** `show <id>` → read `decisions[]` → `decide`.
- **A feed looks dead.** `sources:check --source NAME` → newsops-sources skill.
- **Something is locked.** `status` (never blocks) → read `pid`/`held_since`
  from the error → wait for that process; do not delete `data/news.lock`.
- **The code is wrong.** newsops-dev skill.
