# news-ops implementation plan (v4)

## Context

news-ops is an **agentic workflow**. An AI agent running in a harness
(Claude Code, Pi, or similar) sits on this repo, calls a small Go CLI to
collect and narrow the news, judges what is interesting, researches the ones
you pick, and drafts a LinkedIn or blog post in your voice.

The CLI does the things a language model is bad at: HTTP concurrency, feed
parsing, deduplication, and remembering everything you've ever seen. The
agent does the thing it is good at: judgment, research, and writing. Neither
half tries to do the other's job.

**Collection happens only when the agent is invoked.** No server, no cron, no
background process. You open a session, the agent collects if the data is
stale, and everything downstream happens in that conversation.

### The loop

```
newsops collect          →  ~40 candidates, deterministic, no LLM
    ↓  agent skims titles + excerpts
digest.md                →  one line per candidate, ranked, with a reason
    ↓  YOU pick which are worth researching
briefs/<id>-<slug>.md    →  agent reads the article, queries the archive
    ↓  YOU pick at most one worth writing about
posts/<date>-<slug>.md   →  draft in your voice, LinkedIn or blog
    ↓  YOU edit and publish by hand
```

Two human gates, deliberately. The first stops the agent burning tokens
researching things you don't care about. The second is the one that matters:
it keeps you deciding what you put your name on.

### What changed from v3

v3 was a deterministic pipeline with an agent bolted on the end. v4 inverts
that. The consequences are large and mostly subtractive:

| Change | Reason |
|---|---|
| **Embeddings removed entirely** | Interest scoring was ~3 of 7 build steps, an API key, a vector cache, and a two-week calibration period — to approximate a judgment the agent makes for free while reading the list anyway |
| **Weighted score formula removed** | Same reason. `weights.yml` shrinks to a handful of prefilter knobs |
| **Digest is written by the agent, not the CLI** | The CLI emits `candidates --json`; the agent ranks, annotates, and writes `digest.md` |
| **Novelty via full-text archive query, not cosine** | `newsops query` over FTS5 answers "have we covered this vendor before" well enough, with no vectors |
| **Drafting is now in scope** | v1–v3 said brief-not-draft. Reversed by decision: the agent drafts, gated behind an explicit human pick, in a voice derived from your own posts |
| **Voice capture added** | `context/voice/` samples plus a derived style guide |
| **Window is "since last fetch", not 24h** | Collection is invocation-triggered; a fixed window silently loses a week if you skip a week |

Kept from v3 unchanged: the store and run-bookkeeping design, the lock file,
`--json` everywhere, the `status` contract, the two-layer dedupe, story ID
permanence, and the source list.

### Non-goals

- **No auto-publishing.** Drafts land on disk. You copy, edit, and post. No
  LinkedIn credentials in this repo.
- **No server, no cron, no daemon.**
- **No scraping of hostile sources.** LinkedIn, Indeed and similar block
  scrapers and forbid it in their terms.
- **No stored article bodies beyond an excerpt.** Briefs and posts are
  written from scratch; source paragraphs are never reproduced.
- **No agent in the collection loop.** `fetch` → `dedupe` → `candidates` is
  deterministic and reproducible. The agent starts where judgment starts.

---

## Architecture

### Division of labour

| Concern | Owner | Why |
|---|---|---|
| Fetch, parse, cache feeds | CLI | Concurrency, ETag handling, format zoo |
| Deduplicate | CLI | Pure string functions, must be deterministic |
| Narrow ~300 items → ~40 candidates | CLI | Volume control, no judgment required |
| Rank and annotate candidates | Agent | Judgment |
| Research a story | Agent | Reading, link-following, archive queries |
| Write the draft | Agent | Writing |
| Remember everything | CLI + SQLite | The archive query is the capability a chat window lacks |
| Decide what gets briefed, what gets written, what gets published | **You** | The whole point |

### Repository layout

```
news-ops/
├── AGENTS.md                # canonical agent instructions (harness-neutral)
├── CLAUDE.md                # two lines: @AGENTS.md
├── modes/
│   ├── _shared.md           # loaded by every mode; non-negotiables
│   ├── collect.md           # collect → skim → write digest.md
│   ├── brief.md             # research one story → briefs/
│   ├── draft.md             # brief → post draft in your voice
│   └── voice.md             # regenerate the style guide from samples
├── config/
│   ├── sources.yml          # feeds
│   ├── domains.yml          # primary-source domains
│   └── prefilter.yml        # the few numbers the CLI needs
├── context/
│   ├── interests.md         # dated sections; what you care about now
│   ├── beats.md             # standing topics, and topics to avoid
│   ├── voice/               # ~10 of your own posts, one file each
│   └── voice-guide.md       # derived style guide (generated, editable)
├── cmd/newsops/main.go
├── internal/{feed,store,dedupe,candidates,runstate}/
├── data/                    # news.db, news.lock, cache/   (gitignored)
├── digest.md                # current digest (agent-written)
├── briefs/                  # (gitignored)
└── posts/                   # (gitignored)
```

`.claude/commands/` may hold thin slash-command shortcuts, but they contain
no behavior — each is a one-liner pointing at a mode file. Everything real
lives in harness-neutral markdown so moving to Pi is a no-op.

---

## Approach

Numbered in build order. After step 4 the CLI is complete and you could drive
it by hand; after step 5 the daily loop works; step 6 adds drafting.

### Step 1: Scaffold, store, config

**Goal:** Go module, SQLite schema with migrations, run bookkeeping, config
loading.

- Go module `news-ops` at repo root. Directory tree as above.
- `internal/store/schema.go` — SQLite via `modernc.org/sqlite` (pure Go, no
  cgo):

```sql
CREATE TABLE items (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    url           TEXT NOT NULL UNIQUE,        -- as published
    canonical_url TEXT NOT NULL,               -- normalized, see step 3
    title         TEXT NOT NULL,
    source        TEXT NOT NULL,               -- name from sources.yml
    tier          INTEGER NOT NULL,
    published_at  DATETIME,
    first_seen    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    body_hash     TEXT NOT NULL,
    body_excerpt  TEXT,                        -- ~1500 chars, what the agent skims
    raw_path      TEXT,
    story_id      INTEGER                      -- FK stories(id), null until dedupe
);
CREATE INDEX idx_items_first_seen    ON items(first_seen);
CREATE INDEX idx_items_canonical_url ON items(canonical_url);
CREATE INDEX idx_items_story         ON items(story_id);

CREATE TABLE stories (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    canonical_item_id INTEGER NOT NULL,
    source_count      INTEGER NOT NULL DEFAULT 1,
    method            TEXT NOT NULL,           -- url|title
    first_seen        DATETIME NOT NULL,
    last_seen         DATETIME NOT NULL,
    shown_at          DATETIME,                -- appeared in a digest; prevents repeats
    FOREIGN KEY(canonical_item_id) REFERENCES items(id)
);
CREATE INDEX idx_stories_last_seen ON stories(last_seen);
CREATE INDEX idx_stories_shown     ON stories(shown_at);

-- extra sources on a story (the HN thread, the second outlet)
CREATE TABLE story_sources (
    story_id  INTEGER NOT NULL,
    source    TEXT NOT NULL,
    url       TEXT NOT NULL,
    kind      TEXT NOT NULL,                   -- article|discussion
    PRIMARY KEY(story_id, url),
    FOREIGN KEY(story_id) REFERENCES stories(id)
);

-- your decisions: the record of what you actually cared about
CREATE TABLE decisions (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    story_id INTEGER NOT NULL,
    action   TEXT NOT NULL CHECK(action IN ('shown','briefed','drafted','posted','dismissed')),
    note     TEXT,
    at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY(story_id) REFERENCES stories(id)
);
CREATE INDEX idx_decisions_story ON decisions(story_id, at);

CREATE TABLE runs (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    stage      TEXT NOT NULL,                  -- fetch|dedupe|candidates
    started_at DATETIME NOT NULL,
    ended_at   DATETIME,
    ok         BOOLEAN,
    items_in   INTEGER,
    items_out  INTEGER,
    note       TEXT
);
CREATE INDEX idx_runs_stage ON runs(stage, started_at);

-- HTTP conditional-request state, one row per source: what makes a
-- re-fetch of an unchanged feed cost one round trip and no parsing.
CREATE TABLE feed_state (
    source        TEXT PRIMARY KEY,           -- name from sources.yml
    url           TEXT NOT NULL,
    etag          TEXT,
    last_modified TEXT,
    last_fetched  DATETIME,
    last_status   INTEGER,
    last_error    TEXT
);

CREATE TABLE schema_version (
    version    INTEGER PRIMARY KEY,
    applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- the archive query that justifies the agent
CREATE VIRTUAL TABLE items_fts USING fts5(
    title, body_excerpt, content='items', content_rowid='id'
);
```

`decisions` is a log, not a single row per story — a story can be shown on
Monday, briefed on Tuesday, and drafted on Wednesday, and the sequence is the
interesting part. It is also the table people skip; it costs nothing now and
is the only record of whether the agent's picks match yours.

**Migrations, not just creation.** An ordered `[]migration{version, sql}`
slice applied in a transaction on open. Twenty lines now, and the alternative
is hand-editing a real archive later.

- `internal/store/db.go` — open/close, WAL, transaction helpers, FTS triggers,
  and the process lock.
- `internal/runstate/` — `Begin(stage)`, `End(stage, ok, counts)`,
  `LastSuccess(stage) (time.Time, bool)`. The bool is false when a stage has
  never succeeded.

**Process lock.** Lock `data/news.lock`, **not** `data/news.db` — SQLite
manages its own locking on the database file, and an advisory lock on top of
it is redundant at best and interacts badly with WAL at worst. Non-blocking:
a second process exits 2 immediately with
`{"error": "locked", "held_since": "..."}`. Blocking would let an agent
invocation hang, which is the one failure mode a conversational tool must not
have.

**`config/sources.yml`** — the full list. Tier is a coarse quality hint the
prefilter uses for ordering and the agent sees in the candidate list.
**1** = primary or high signal-to-noise · **2** = solid reporting ·
**3** = volume.

```yaml
sources:
  # Aggregators & community
  - {name: Hacker News,        tier: 1, url: "https://hnrss.org/frontpage?points=100"}
  - {name: Techmeme,           tier: 1, url: "https://www.techmeme.com/feed.xml"}
  - {name: Lobsters,           tier: 1, url: "https://lobste.rs/rss"}
  - {name: r/programming,      tier: 2, url: "https://www.reddit.com/r/programming/top/.rss?t=day"}
  - {name: r/MachineLearning,  tier: 2, url: "https://www.reddit.com/r/MachineLearning/top/.rss?t=day"}
  - {name: dev.to,             tier: 3, url: "https://dev.to/feed"}

  # Deep / analyst
  - {name: Simon Willison,     tier: 1, url: "https://simonwillison.net/atom/everything/"}
  - {name: Stratechery,        tier: 1, url: "https://stratechery.com/feed/"}
  - {name: SemiAnalysis,       tier: 1, url: "https://semianalysis.com/feed/"}
  - {name: Pragmatic Engineer, tier: 1, url: "https://newsletter.pragmaticengineer.com/feed"}
  - {name: LWN,                tier: 1, url: "https://lwn.net/headlines/newrss"}
  - {name: ACM Queue,          tier: 1, url: "https://queue.acm.org/rss/feeds/queuecontent.xml"}
  - {name: Martin Fowler,      tier: 1, url: "https://martinfowler.com/feed.atom"}
  - {name: Brendan Gregg,      tier: 1, url: "https://www.brendangregg.com/blog/rss.xml"}

  # Research
  - {name: arXiv cs.AI,        tier: 1, url: "https://rss.arxiv.org/rss/cs.AI"}
  - {name: arXiv cs.LG,        tier: 1, url: "https://rss.arxiv.org/rss/cs.LG"}
  - {name: arXiv cs.DC,        tier: 1, url: "https://rss.arxiv.org/rss/cs.DC"}
  - {name: Google Research,    tier: 1, url: "https://research.google/blog/rss/"}
  - {name: DeepMind,           tier: 1, url: "https://deepmind.google/blog/rss.xml"}

  # AI labs & platforms
  - {name: Anthropic,          tier: 1, url: "https://www.anthropic.com/rss.xml"}
  - {name: OpenAI,             tier: 1, url: "https://openai.com/blog/rss.xml"}
  - {name: Hugging Face,       tier: 1, url: "https://huggingface.co/blog/feed.xml"}
  - {name: LangChain,          tier: 2, url: "https://blog.langchain.dev/rss/"}
  - {name: Databricks,         tier: 2, url: "https://www.databricks.com/feed"}

  # Infra & engineering
  - {name: Cloudflare,          tier: 1, url: "https://blog.cloudflare.com/rss/"}
  - {name: AWS News,            tier: 1, url: "https://aws.amazon.com/blogs/aws/feed/"}
  - {name: Google Cloud,        tier: 2, url: "https://cloudblog.withgoogle.com/rss/"}
  - {name: GitHub,              tier: 1, url: "https://github.blog/feed/"}
  - {name: Netflix Tech,        tier: 1, url: "https://netflixtechblog.com/feed"}
  - {name: Meta Engineering,    tier: 1, url: "https://engineering.fb.com/feed/"}
  - {name: Uber Engineering,    tier: 1, url: "https://www.uber.com/blog/engineering/rss/"}
  - {name: Stripe Blog,         tier: 1, url: "https://stripe.com/blog/feed.rss"}
  - {name: Discord Engineering, tier: 2, url: "https://discord.com/blog/rss.xml"}
  - {name: Shopify Engineering, tier: 2, url: "https://shopify.engineering/blog.atom"}
  - {name: Grafana,             tier: 2, url: "https://grafana.com/blog/index.xml"}
  - {name: Confluent,           tier: 2, url: "https://www.confluent.io/blog/feed/"}
  - {name: Temporal,            tier: 2, url: "https://temporal.io/blog/rss.xml"}

  # Security
  - {name: Krebs on Security,  tier: 1, url: "https://krebsonsecurity.com/feed/"}
  - {name: Schneier,           tier: 1, url: "https://www.schneier.com/feed/atom/"}
  - {name: BleepingComputer,   tier: 2, url: "https://www.bleepingcomputer.com/feed/"}
  - {name: The Hacker News,    tier: 2, url: "https://feeds.feedburner.com/TheHackersNews"}
  - {name: Project Zero,       tier: 1, url: "https://googleprojectzero.blogspot.com/feeds/posts/default"}

  # Outlets
  - {name: Ars Technica,   tier: 2, url: "https://feeds.arstechnica.com/arstechnica/index"}
  - {name: The Register,   tier: 2, url: "https://www.theregister.com/headlines.atom"}
  - {name: InfoQ,          tier: 2, url: "https://feed.infoq.com/"}
  - {name: The New Stack,  tier: 2, url: "https://thenewstack.io/feed/"}
  - {name: Phoronix,       tier: 2, url: "https://www.phoronix.com/rss.php"}
  - {name: IEEE Spectrum,  tier: 2, url: "https://spectrum.ieee.org/feeds/feed.rss"}
  - {name: TechCrunch,     tier: 3, url: "https://techcrunch.com/feed/"}
  - {name: The Verge,      tier: 3, url: "https://www.theverge.com/rss/index.xml"}
  - {name: VentureBeat,    tier: 3, url: "https://venturebeat.com/feed/"}
```

**These URLs are unverified — feed paths drift.** Expect the first
`sources:check` to fail several. That is why it is the first thing built after
fetch, not a sign anything is wrong.

- `config/domains.yml` — primary-source domains. Used by canonical-item
  selection and as the allowlist for the agent's link-following during briefs.
- `config/prefilter.yml` — the only tunable numbers left:

```yaml
candidate_limit:      40   # how many the agent skims
per_source_cap:        3   # stops arXiv and TechCrunch owning the list
title_simhash_max:     3
max_backfill_days:    30   # first run, or after a long gap
exclude_terms: []          # hard drops, e.g. ["horoscope"]
```

No interest weights here. Interest lives in `context/interests.md` as prose,
which the agent reads. A paragraph gets rewritten in thirty seconds; a
weight never gets retuned.

**Tests:** in-memory SQLite. CRUD round-trips, the `url` UNIQUE constraint,
migrations idempotent when applied twice, FTS triggers keeping `items_fts` in
sync on insert and update.

### Step 2: Feed module + fetch

**Goal:** Concurrent, polite, cacheable collection.

- `internal/feed/parser.go` — wrapper around `mmcdole/gofeed`. Extract title,
  link, published date, content, and feed categories/tags. Build
  `body_excerpt` as `title + "\n" + tags + "\n" + content[:1500]`, stripped of
  markup — this is what the agent skims, so it must be readable prose, not
  raw HTML.
- `internal/feed/fetcher.go` — `errgroup` with `SetLimit(10)`, per-host
  limiter, `ETag` / `If-Modified-Since` (304 = skip). **Send a descriptive
  `User-Agent`** (`newsops/0.1 (+personal feed reader)`) — Reddit and several
  CDNs 429 the default Go agent.
- `internal/feed/cache.go` — raw bodies to `data/cache/<url_hash>/<date>`.
  You will reprocess many times while tuning, and refetching is slow, rude,
  and gets you blocked.
- `go-shiori/go-readability` as a fallback only, when a feed returns a
  truncated summary and the excerpt would otherwise be useless.

**Window.** `fetch` collects everything new since the last successful fetch.
On a first run, or after a gap longer than `max_backfill_days`, it reaches
back that far and no further. This is the difference between a tool that
works when you use it twice a week and one that only works if you use it
daily.

**`newsops sources:check`** — GET each source, confirm 200 and that `gofeed`
parses ≥1 item. Pass/fail table, no side effects. Run before the first real
fetch, and monthly.

**Tests:** `httptest` servers over saved fixture bodies — one RSS, one Atom,
one truncated-summary feed. Assert category extraction, that a 304 yields
zero items and no error, and that the per-host limiter serializes two URLs on
one host.

### Step 3: Dedupe — two layers, cheapest first

**Goal:** One story per actual story, with the aggregators folded in.

The source list is dominated by primary sources — vendor engineering blogs,
arXiv, personal blogs — which don't republish each other. Genuine cross-outlet
duplication is confined to maybe 12 of the 50 sources. And for aggregators,
semantic similarity is the wrong tool entirely: HN, Lobsters, Reddit and
Techmeme all link to the original, so canonicalizing the outbound URL dedupes
them *exactly*.

**Story IDs are permanent.** `digest.md` numbers stories by ID, briefs are
`briefs/<story_id>-<slug>.md`, and `decisions.story_id` is the only record of
what you cared about. If a dedupe re-run renumbered, every one of those would
silently point somewhere else, and nothing would tell you. So:

> Dedupe is incremental and append-only over `stories`. An existing row may
> have `source_count`, `last_seen` and `canonical_item_id` updated. A story ID
> is never reused and never renumbered.

**Layer 1 — URL canonicalization (`internal/dedupe/canonical.go`).**

- Strip `utm_*`, `ref`, `source`, `fbclid`, and fragments.
- Lowercase host, strip `www.`, strip trailing slash.
- Follow one redirect hop for known shorteners.
- **Resolve aggregator links to their target.** The aggregator item carries
  the real URL; use it as `canonical_url` and record the aggregator in
  `story_sources` as an extra source, not a separate item:

| Source | Article URL | Discussion URL |
|---|---|---|
| Hacker News (`hnrss`) | `<link>` | `<comments>` |
| Lobsters | `<link>` | `<comments>` |
| Reddit (`.rss`) | first outbound `<a>` in content | `<link>` |
| Techmeme | `<link>` (already the source) | — |

A self-post or Ask HN with no outbound link is a normal item of its own.

**Layer 2 — normalized title similarity (`internal/dedupe/title.go`).**

- Normalize: lowercase, strip punctuation, drop outlet suffixes
  (`" | Ars Technica"`), drop leading `"Show HN: "`.
- SimHash, Hamming distance ≤ `title_simhash_max`.
- Compare only within a 48h window.

Canonical item selection: earliest `published_at`, preferring a
`domains.yml` primary-source domain when tied.

**A third semantic layer is not planned.** If, after a few weeks, duplicates
are visibly leaking into digests, the cheapest fix is to let the agent merge
them while skimming — it is already reading all 40 titles — and record the
merge. That costs nothing and needs no embedding infrastructure.

**Tests:** table tests for both files. These are pure string functions, every
downstream number depends on them, and their failures are silent — they are
the highest-value tests in the project. Cover tracking params, each aggregator
mapping above, outlet-suffix stripping, and a real headline pair that should
match plus one that should not.

### Step 4: The agent-facing CLI surface

**Goal:** Everything the agent needs, as JSON, with no judgment in it.

`cmd/newsops/main.go` routes: `status`, `sources:check`, `fetch`, `dedupe`,
`candidates`, `collect`, `query`, `decide`, `show`.

**`--json` on every subcommand.** The agent parses stdout; human-formatted
output is a bug generator. Human-readable is the fallback, not the contract.

**`newsops status --json`** — read-only, no network, fast. First call of every
session.

```json
{
  "stages": {
    "fetch":      {"last_success": "2026-08-30T07:12:00Z", "age_min": 143, "stale": false},
    "dedupe":     {"last_success": "2026-08-30T07:13:00Z", "age_min": 142, "stale": false},
    "candidates": {"last_success": "2026-08-30T07:13:00Z", "age_min": 142, "stale": false}
  },
  "counts": {"items_total": 4102, "new_since_fetch": 0, "unshown_stories": 12},
  "digest": {"path": "digest.md", "generated_at": "2026-08-30T07:20:00Z"}
}
```

On a virgin database the shape is identical, with `last_success: null`,
`age_min: null`, `stale: true`. **Never-run is stale**, and `stale` is always
a boolean — never a missing key, never an error. An agent that has to
special-case a first run will get it wrong.

**`newsops collect --max-age 6h --json`** — the agent's one command. Chains
fetch → dedupe → candidates. `--max-age` no-ops the fetch if the last
successful one is younger, exiting 0 with
`{"skipped": true, "reason": "fresh"}`, but dedupe and candidates still run if
they have pending input (items with `story_id IS NULL`, or unshown stories).
A fresh fetch must never strand new items.

**`newsops candidates --json`** — the prefiltered set. Deterministic, no LLM,
no vectors:

1. Stories with `last_seen` since the last fetch (`first_seen`-based, because
   arXiv and several vendor blogs publish null or plainly wrong `published_at`
   — that field is for display only).
2. Drop anything with `shown_at` set. This, not similarity scoring, is what
   stops the same story reappearing every day.
3. Drop titles matching `exclude_terms`.
4. Apply `per_source_cap` per source.
5. Order by tier, then `source_count`, then recency. This is presentation
   order, not a quality claim — the agent re-ranks.
6. Cap at `candidate_limit`.

```json
{
  "generated_at": "2026-08-30T07:13:00Z",
  "window": {"since": "2026-08-29T06:55:00Z", "days": 1},
  "total_new": 287, "returned": 40,
  "candidates": [
    {
      "story_id": 4213,
      "title": "Cloudflare publishes postmortem on the 26 Aug outage",
      "url": "https://blog.cloudflare.com/...",
      "source": "Cloudflare", "tier": 1, "source_count": 4,
      "published_at": "2026-08-30T05:40:00Z",
      "primary": true,
      "excerpt": "On 26 August at 14:02 UTC our edge began returning 522s for..."
    }
  ]
}
```

**`newsops query --json`** — the archive. FTS5 over title and excerpt, with
filters:

```
newsops query --text "cloudflare outage" --days 90 --json
newsops query --domain cloudflare.com --days 180 --json
newsops query --story 4213 --related --json    # shares terms/domain, excludes itself
```

This is the capability a chat window does not have and the reason an agent
earns its place here. *"This vendor had an outage three weeks ago and shipped
a related fix last week"* is not something you can paste into a prompt,
because you don't know it's there — that's why you're asking. It is also what
makes a post worth reading rather than another summary of the same press
release.

**`newsops decide <story_ids> <action> [--note ...]`** — records `shown`,
`briefed`, `drafted`, `posted`, `dismissed`. Accepts a comma-separated id
list, so marking forty candidates shown is one call. The agent calls this as
the workflow proceeds; `shown` also sets `stories.shown_at`, first write
winning. `collect` mode records the rank it assigned as `--note "rank=<n>"` —
that note is what lets the review ask whether the agent's ordering predicted
your picks.

**`newsops show <story_id> --json`** — one story with all its items, sources
and decision history. What `brief` mode opens with.

**Exit codes:** 0 success or skipped, 1 partial (some feeds failed, candidates
still produced), 2 hard failure. `collect` continues past a stage that exits
1 — a partial fetch is still worth reading — aborts on 2, and returns the
worst code any stage returned.

**Tests:** golden test on `candidates` over a fixed seeded database — assert
the exact ordering, that `per_source_cap` drops the fourth item from one
source, and that a story with `shown_at` set never reappears.

### Step 5: The agent layer — collect, digest, brief

**Goal:** The daily loop, as harness-neutral markdown.

`AGENTS.md` is canonical. `CLAUDE.md` contains `@AGENTS.md` and nothing else.
Any Claude Code slash commands are one-liners pointing at mode files. Nothing
that matters is harness-specific, so this runs in Pi with no rewrite.

**`modes/_shared.md`** — loaded by every mode:

```markdown
Before answering anything about current news:
1. Run `newsops status --json`.
2. If fetch is stale (>6h), run `newsops collect --max-age 6h --json`.

Never re-fetch a feed by hand; the CLI owns collection.
Never reproduce source paragraphs — everything you write is written fresh.
Never publish anything anywhere. Drafts go to disk; the human posts them.
Prefer the primary source over coverage of it.
Record what happens with `newsops decide`.
```

**`modes/collect.md`** — collect and produce the digest:

1. `newsops collect --max-age 6h --json`.
2. Read `context/interests.md` (current section) and `context/beats.md`.
3. Skim all ~40 candidates. Rank them by how interesting they are *to this
   user given those files* — not by general newsworthiness.
4. Write `digest.md`: one line per candidate, most interesting first, each
   with a story ID, title, source, and **a short reason it might matter to
   you**. Flag anything the archive suggests is a follow-up to something
   already covered.
5. `newsops decide <ids> shown --note "rank=<n>"` for every candidate listed.
6. Stop. Ask which ones are worth briefing. Do not brief anything unasked.

```markdown
# Digest — 2026-08-30
_287 new items → 40 candidates_

1. **[4213]** Cloudflare postmortem on the 26 Aug outage · cloudflare.com · 4 sources
   Primary source, and you covered their July incident — this is the follow-up.
2. **[4198]** Postgres 18 ships async I/O · postgresql.org · 2 sources
   Directly in your infra beat; the benchmark numbers are the story, not the release.
...
```

The reason line is the whole value of the digest. A bare list of headlines is
something an RSS reader already gives you.

**`modes/brief.md`** — research one story, on request:

1. `newsops show <id> --json`.
2. Fetch and read the actual article. Follow outbound links filtered to
   `domains.yml` primaries — the changelog, the paper, the filing. This is
   the five-to-ten tool calls you'd skip on a Tuesday, and skipping them is
   how people end up writing from coverage instead of sources.
3. `newsops query --story <id> --related --days 90` for archive connections.
4. Write `briefs/<story_id>-<slug>.md` in a fixed five-part shape:
   - **Core fact** — one sentence.
   - **Primary source** — the link.
   - **What coverage skipped or got wrong.**
   - **Connection to prior events** in the archive, with story IDs.
   - **One thing you might disagree with.**
5. `newsops decide <id> briefed`.
6. Stop. The brief is the deliverable of this mode.

Fixed shape because a brief you can skim in fifteen seconds is one you'll
actually read; free-form summaries drift into being the article again.

**Tests:** none — this is prose. Verification is running it (below).

### Step 6: Voice + draft

**Goal:** A draft that sounds like you, from a brief you chose.

**`context/voice/`** — roughly ten of your own posts, one file each, best
ones you'd be happy to have written again. This is the input; without it,
nothing in this step works.

**`context/voice-guide.md`** — derived, then hand-edited. `modes/voice.md`
reads the samples and distills them into explicit rules: typical length,
how you open, whether you use questions, lists, first person; what you never
do; recurring structural moves. Regenerate when you add samples.

Samples plus a derived guide, rather than either alone: tone transfer from
examples generally beats instructions about tone, but when a draft comes out
sounding wrong, an explicit rule is something you can *edit*. A sample set is
not. You need both — the examples to imitate, the rules to correct.

**`modes/draft.md`** — takes a brief and a target:

- `--linkedin` — 150–250 words. One idea. A hook that isn't a question, a
  concrete detail from the primary source, a position. No hashtag spray, no
  "thoughts?", no em-dash-heavy LLM cadence. Link goes in the first comment,
  emitted as a separate block, because LinkedIn is widely reported to suppress
  reach on posts with external links — test that against your own numbers,
  platform behaviour moves.
- `--blog` — 800–1200 words. Context, the thing itself, why it matters,
  what you'd watch next. Subheads. Links inline.

Both read `context/voice-guide.md` and few-shot from `context/voice/`. Both
write to `posts/<date>-<slug>-<target>.md` and call
`newsops decide <id> drafted`.

**The draft is a starting point, not an artifact.** It exists to kill the
blank page. Expect to rewrite most of it — and note that the reason v1–v3 of
this plan refused to build this step is real: model-written posts are
recognizable, and the credibility cost of publishing one unedited exceeds
whatever reach it earns. The guard against that is the human gate before this
mode ever runs, and the expectation that you edit. Nothing in the code can
enforce it.

### Step 7: Use it, then measure

Not a build step. Run the loop for two weeks before changing anything, then
ask:

- **Digest quality** — of the stories you asked to brief, how many came from
  the agent's top 5? (The `rank=` note on each `shown` decision is what makes
  this answerable.) If the ranking is uncorrelated with your picks,
  `interests.md` is too vague, not the prompt.
- **Duplicate leakage** — how many digest lines were the same story twice?
  Under ~5% and the two dedupe layers are sufficient forever.
- **Prefilter loss** — spot-check what the `candidate_limit: 40` cut dropped.
  If good stories are dying there, raise the cap before touching anything
  else.
- **Voice drift** — do drafts need heavy rewriting in the same way every
  time? That's a missing rule in `voice-guide.md`, and it's editable.
- **`decisions`** — the log of shown vs briefed vs drafted is the only
  honest record of whether this is working.

Deliberately not planned until this measurement says otherwise: semantic
dedupe, embedding-based interest scoring, a `publish` command, any web UI.

---

## Critical files & anchors

| File | Symbol | Reason |
|---|---|---|
| `AGENTS.md` + `modes/` | — | Where all behavior lives; harness-neutral by construction |
| `cmd/newsops/main.go` | Subcommand routing, `--json` | The agent's entire contract with the pipeline |
| `internal/candidates/` | Prefilter + `shown_at` exclusion | Volume control, and the thing that stops repeats |
| `internal/dedupe/canonical.go` | URL normalization | Does most of the dedupe work for zero cost |
| `internal/store/schema.go` | Tables, FTS, migrations | Foundation; `decisions` is the feedback record |
| `context/voice-guide.md` | — | The editable half of voice; the correction surface |

---

## Verification

1. **Build:** `go build ./cmd/newsops/` — one static binary, no cgo.
2. **Tests:** `go test ./...`. `internal/dedupe` and `internal/candidates`
   must have real coverage.
3. **Sources:** `./newsops sources:check` — ≥80% pass after one round of URL
   fixes.
4. **Status on a virgin DB:** `./newsops status --json | jq .stages.fetch.stale`
   returns `true`, not an error and not null.
5. **Idempotence:** `./newsops collect --max-age 6h` twice — the second
   reports the fetch skipped, with no network traffic.
6. **Stage gating:** with a fresh fetch but unprocessed items, `collect` still
   dedupes and produces candidates.
7. **Lock:** two concurrent `collect` — the second exits 2 with
   `{"error": "locked"}` immediately, and `PRAGMA integrity_check` passes.
8. **Dedupe L1:** seed a HN item and the article it links to → one story,
   `source_count: 2`, `method: "url"`, HN in `story_sources` as `discussion`.
9. **Dedupe L2:** two outlets, differing headlines, one story, `method: "title"`.
10. **ID stability:** run `dedupe` twice over the same data — no ID changes,
    no new rows.
11. **No repeats:** run `candidates` twice with `decide … shown` in between —
    the second returns none of the same stories.
12. **Archive query:** `newsops query --text "outage" --days 90 --json`
    returns hits with story IDs, over a seeded archive.
13. **Collect mode end-to-end:** in a session, load `modes/collect.md` — it
    calls `status` first, produces `digest.md` with a reason line per entry,
    and stops without briefing anything.
14. **Brief mode:** ask for two story IDs — two files in `briefs/`, five-part
    shape, at least one real archive connection with a story ID, and
    `decisions` rows written.
15. **Draft mode:** with ~10 samples in `context/voice/`, generate both
    targets from one brief. The honest test is whether you'd post it after
    editing, not before.

---

## Assumptions & contingencies

- **No embedding API key needed anywhere.** If interest matching by prose
  turns out to be too coarse, the fallback is a better `interests.md`, not a
  vector database.
- **The agent's skim is the quality bottleneck.** ~40 candidates at ~1500
  chars each is a large but manageable read. If it degrades, lower
  `candidate_limit` before adding cleverness.
- **arXiv floods.** `per_source_cap: 3` handles it mechanically. If it still
  dominates, cut subcategories in `sources.yml`.
- **Techmeme is already an editorially clustered feed.** Treat divergence
  from our dedupe as a signal about our thresholds, not a bug.
- **Feed URLs drift.** `sources:check` monthly is the maintenance cost of
  this system, and roughly all of it.
- **Long gaps are normal.** `max_backfill_days: 30` bounds the first run and
  any return from holiday.
- **`interests.md` is the actual product.** Everything else is scaffolding.
  Dated sections, appended, never overwritten — keeping the archived ones is
  what lets you notice that what you said you cared about and what you
  actually briefed are different things.

---

## Decisions log

| Decision | Rationale |
|---|---|
| Agent judges, CLI narrows | Judgment is what a model is good at; concurrency, dedupe and memory are what it is bad at |
| No embeddings anywhere | Three build steps and a calibration period to approximate a judgment made for free while reading the list |
| Full-text archive query, not vector similarity | FTS5 answers "have we covered this" well enough, with no key, no cache, no dimension mismatch |
| Two human gates | The first saves tokens; the second keeps you deciding what carries your name |
| Digest carries a reason per line | A bare headline list is what an RSS reader already does |
| URL canonicalization over semantic dedupe | Aggregators link to the original; canonicalizing dedupes them exactly, for free |
| `shown_at`, not similarity, prevents repeats | Exact, trivial, and correct — no threshold to tune |
| Story IDs permanent | Briefs and decisions reference them; silent renumbering is unrecoverable |
| Drafting in scope, gated behind a pick | Reverses v1–v3. The blank page is real; the credibility risk is managed by the gate and by editing |
| Voice = samples + derived rules | Examples transfer tone; rules are what you can edit when it drifts |
| Drafts to disk, no publishing | No credentials in the repo, no irreversible action one command away |
| Harness-neutral markdown | Claude Code today, Pi tomorrow, no rewrite |
| Go single binary | No venv or node_modules to drift; a dependency failure mid-session is the worst failure mode |
