# news-ops implementation plan (v3)

## Context

Build a personal IT-news pipeline in Go: fetch RSS feeds, deduplicate,
score against current interests, and produce `digest.md`. A separate
interactive mode (`deep`) queries the archive via LLM.

Two loops — batch (deterministic, no LLM) and interactive (on-demand, LLM).
Single binary with subcommands. **Local only, no server, no cron** — the
agent triggers collection when needed.

This document is self-contained. It replaces the earlier design document,
which has been deleted; everything still needed from it is inlined here.

### Non-goals

- **No auto-publishing.** The system produces a brief. You write the post.
- **No agent in the batch loop.** Ingestion through digest is deterministic.
- **No scraping of hostile sources.** LinkedIn and similar block scrapers and
  forbid it in their terms.
- **No stored article bodies beyond what's needed.** Summaries are written
  from scratch; source paragraphs are never reproduced.

### Changes from v1

| # | Change | Reason |
|---|---|---|
| 1 | No cron. Agent-triggered via `status` + `--max-age` guard | Local setup; agent must cheaply decide whether data is stale |
| 2 | Embed title + ~1500 chars, not first paragraph | Free win; 32k token budget was being wasted. Summarize-then-embed deferred, see Assumptions |
| 3 | Dedupe demoted to 3 cheap layers; embedding clustering deferred | Source list is mostly primary sources; aggregators dedupe exactly via canonical URL |
| 4 | Dedupe now precedes embedding | Layers 1–2 need no vectors, and collapsing duplicates first means fewer items to embed |
| 5 | Embed canonical items only, one vector per story | That is exactly what interest and novelty consume |
| 6 | Interest and novelty normalized per run before weighting | Raw cosines occupy a narrow band; unnormalized, the yaml weights are fiction |

**Consequence of #3:** embeddings are still required — interest scoring and
novelty need them regardless. Only the *dedupe* use of embeddings is deferred.

---

## Approach

Steps are numbered in build order. The system is useful before it is
complete: after step 5 you have a working digest with no LLM anywhere.

### Step 1: Project scaffold, store module, config

**Goal:** Go module, directory layout, SQLite schema with migrations, CRUD
for `items`, run bookkeeping, and the config files.

- Initialize Go module (`news-ops`) at repo root.
- Directory tree: `cmd/newsops/`, `internal/feed/`, `internal/store/`,
  `internal/embed/`, `internal/dedupe/`, `internal/score/`,
  `internal/digest/`, `internal/runstate/`, `config/`, `context/`,
  `modes/`. Gitignored: `data/`, `briefs/`, `posts/`, `digest/`.
- `internal/store/schema.go` — SQLite via `modernc.org/sqlite` (pure Go,
  no cgo):

```sql
CREATE TABLE items (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    url           TEXT NOT NULL UNIQUE,        -- as published
    canonical_url TEXT NOT NULL,               -- normalized, see step 3
    title         TEXT NOT NULL,
    source        TEXT NOT NULL,
    published_at  DATETIME,
    first_seen    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    body_hash     TEXT NOT NULL,
    body_excerpt  TEXT,                        -- ~1500 chars, embedding input
    raw_path      TEXT,
    story_id      INTEGER                      -- FK stories(id), null until dedupe
);
CREATE INDEX idx_items_published     ON items(published_at);
CREATE INDEX idx_items_first_seen    ON items(first_seen);
CREATE INDEX idx_items_canonical_url ON items(canonical_url);
CREATE INDEX idx_items_story         ON items(story_id);

-- run bookkeeping: replaces cron's implicit "when did this last run"
CREATE TABLE runs (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    stage      TEXT NOT NULL,       -- fetch|dedupe|embed|score|digest
    started_at DATETIME NOT NULL,
    ended_at   DATETIME,
    ok         BOOLEAN,
    items_in   INTEGER,
    items_out  INTEGER,
    note       TEXT
);
CREATE INDEX idx_runs_stage ON runs(stage, started_at);

CREATE TABLE schema_version (
    version    INTEGER PRIMARY KEY,
    applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

**Migrations, not just creation.** The plan already schedules two schema
additions (dedupe layer 3's merge bookkeeping, and `embeddings.kind =
'summary'`). An ordered `[]migration{version, sql}` slice applied inside a
transaction on open is twenty lines now and saves a hand-edited archive later.

- `internal/store/db.go` — open/close, transaction helpers, WAL mode, and
  the process lock (see below).
- `internal/store/items.go` — `InsertItem`, `GetItemsByDateRange`,
  `CountItemsInWindow`, `GetItemsByCanonicalURL`.
- `internal/runstate/` — `Begin(stage)`, `End(stage, ok, counts)`,
  `LastSuccess(stage) (time.Time, bool)`. Every subcommand wraps itself in
  these. The bool is false when the stage has never succeeded.

**Process lock.** Lock `data/news.lock`, **not** `data/news.db` — SQLite
manages its own locking on the database file, and an advisory lock on top of
it is at best redundant and at worst interacts badly with WAL. Non-blocking:
a second process exits 2 immediately with
`{"error": "locked", "held_since": "2026-08-30T09:14:02Z"}`. Blocking would
mean an agent invocation can hang indefinitely, which is exactly what the
`status`-first protocol exists to avoid.

**`config/sources.yml`** — the full list. Tier maps to the `source_tier`
weight: **1** = primary or high signal-to-noise · **2** = solid reporting ·
**3** = volume, useful mainly for the coverage-breadth signal. Weighted
toward backend / infra / AI.

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
`sources:check` run to fail several and need a round of fixes; that is the
point of writing that subcommand early, not a sign something is broken.

- `config/domains.yml` — primary-source domains, used by the
  `primary_source` score component and by `deep`'s link-following filter.
- `config/weights.yml` — every tunable number (contents in step 5).

**Tests:** in-memory SQLite. Round-trip each CRUD function; assert the `url`
UNIQUE constraint rejects a duplicate; assert migrations are idempotent when
applied twice.

### Step 2: Feed module + agent-facing CLI surface

**Goal:** Concurrent fetch, plus the ergonomics that let an agent drive this
safely without cron.

- `internal/feed/parser.go` — wrapper around `mmcdole/gofeed`. Extract
  title, link, published date, content, **and feed categories/tags** (they
  feed the embedding, see step 4).
- `internal/feed/fetcher.go` — `errgroup` with `SetLimit(10)`, per-host
  limiter, `ETag` / `If-Modified-Since` (304 = skip). **Send a descriptive
  `User-Agent`** (e.g. `newsops/0.1 (+personal feed reader)`) — Reddit and
  several CDNs 429 the default Go agent.
- `internal/feed/cache.go` — raw body to `data/cache/<url_hash>/<date>`. You
  will reprocess many times while tuning, and refetching is slow, rude, and
  gets you blocked.
- Fall back to `go-shiori/go-readability` extraction only when a feed returns
  a truncated summary.
- `cmd/newsops/main.go` — subcommand routing:
  `status`, `sources:check`, `fetch`, `dedupe`, `embed`, `score`,
  `digest`, `run`.

**Agent-facing requirements — these are the point of this step:**

- **`--json` on every subcommand.** The agent parses stdout; human-formatted
  output is a bug generator. Human-readable is the fallback, not the
  default contract.
- **`newsops status --json`** — read-only, no network, fast. Returns per
  stage: last success, age in minutes, item counts, whether stale. This is
  the first call the agent makes, every time.

```json
{
  "stages": {
    "fetch":  {"last_success": "2026-08-30T07:12:00Z", "age_min": 143, "stale": false},
    "dedupe": {"last_success": "2026-08-30T07:13:00Z", "age_min": 142, "stale": false},
    "score":  {"last_success": "2026-08-29T07:15:00Z", "age_min": 1582, "stale": true}
  },
  "counts": {"items_total": 4102, "items_24h": 287, "stories_24h": 231},
  "digest": {"path": "digest.md", "generated_at": "2026-08-29T07:15:00Z"}
}
```

**Never-run contract.** On a virgin database `status` must still return the
same shape — `stale` is a boolean, not an error or a missing key. Never-run
is stale:

```json
{
  "stages": {
    "fetch":  {"last_success": null, "age_min": null, "stale": true},
    "dedupe": {"last_success": null, "age_min": null, "stale": true},
    "embed":  {"last_success": null, "age_min": null, "stale": true},
    "score":  {"last_success": null, "age_min": null, "stale": true},
    "digest": {"last_success": null, "age_min": null, "stale": true}
  },
  "counts": {"items_total": 0, "items_24h": 0, "stories_24h": 0},
  "digest": {"path": "digest.md", "generated_at": null}
}
```

**`--max-age` guard on every stage.** `newsops fetch --max-age 6h` no-ops if
the last successful fetch is younger, exiting 0 with
`{"skipped": true, "reason": "fresh"}`. This makes "just run it" safe — the
agent never has to reason about whether it already ran.

**But age alone is not sufficient.** Consider the `status` example above:
fetch is fresh at 143 minutes, score is stale at 1582. Under a strict
age-only rule a fetch that just landed 300 items would be followed by a
`score` that skips itself as fresh, and those items would never reach a
digest. So:

> **A stage runs if its last success is older than `--max-age`, OR it has
> pending input.**

| Stage | Pending input |
|---|---|
| `fetch` | (age only — it is the network stage; nothing upstream of it) |
| `dedupe` | any item with `story_id IS NULL` |
| `embed` | any canonical item with no `embeddings` row for the current model |
| `score` | any candidate story with no `scores` row, or whose `interest_version` differs from the current one |
| `digest` | any `scores` row written since the last successful digest |

Skipping stays cheap and safe, and a fresh fetch never strands new items.

- **`newsops run --max-age 6h`** — chains fetch → dedupe → embed → score →
  digest, each honouring the guard above.
- **Exit codes:** 0 success or skipped, 1 partial (some feeds failed, digest
  still produced), 2 hard failure. **`run` continues past a stage that exits
  1** — the whole point of code 1 is that a digest is still worth producing —
  and **aborts on 2**. `run` returns the worst code any stage returned.

- **`sources:check`** — GET each source, confirm 200 and that `gofeed`
  parses ≥1 item. Print a pass/fail table. No side effects. Run before
  first real fetch and monthly after.

**Tests:** `httptest` servers serving saved fixture bodies (one RSS, one
Atom, one truncated-summary feed). Assert the parser extracts categories;
assert a 304 response results in zero new items and no error; assert the
per-host limiter serializes two URLs on the same host.

### Step 3: Dedupe module — three layers, cheapest first

**Goal:** Collapse the same story appearing more than once. **Layers 1 and 2
only.** Layer 3 is deferred and may never be needed.

Rationale for the demotion: the source list is dominated by primary sources
— vendor engineering blogs, arXiv, personal blogs — which don't republish
each other. Genuine cross-outlet duplication is confined to roughly 12 of
the 50 sources. And for aggregators, semantic similarity is the *wrong
tool*: HN, Lobsters, Reddit, and Techmeme all link to the original, so
canonicalizing the outbound URL dedupes them **exactly**.

Terminology change from v1: `clusters` → `stories`, `cluster_size` →
`source_count`. The old name implied an algorithm that is no longer the
primary mechanism.

```sql
CREATE TABLE stories (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    canonical_item_id INTEGER NOT NULL,
    source_count      INTEGER NOT NULL DEFAULT 1,
    method            TEXT NOT NULL,   -- url|title|embedding
    first_seen        DATETIME NOT NULL,
    last_seen         DATETIME NOT NULL,
    FOREIGN KEY(canonical_item_id) REFERENCES items(id)
);
CREATE INDEX idx_stories_last_seen ON stories(last_seen);
```

**Story IDs are permanent.** `digest.md` numbers stories by ID,
`decisions.story_id` is the entire feedback loop, and `deep` writes
`briefs/<story_id>.md`. If a dedupe re-run ever renumbers, every brief and
every recorded decision silently points at a different story — and nothing
will tell you. So the invariant, stated up front:

> Dedupe is incremental and append-only over `stories`. An existing row may
> have its `source_count`, `last_seen`, and `canonical_item_id` updated. A
> story ID is never reused and never renumbered. If two existing stories must
> merge (only possible once layer 3 lands), the **older** ID survives and the
> merge is recorded so old references can be followed.

**Layer 1 — URL canonicalization (`internal/dedupe/canonical.go`).**
Free, exact, catches the largest share.

- Strip `utm_*`, `ref`, `source`, `fbclid`, fragments.
- Lowercase host, strip `www.`, strip trailing slash.
- Follow one redirect hop for known shorteners.
- **Resolve aggregator links to their target.** An aggregator item carries
  the real URL in the feed; use it as the item's `canonical_url`, and record
  the aggregator as an additional source on the same story rather than as a
  separate item. Which field holds what differs per aggregator:

| Source | Outbound (article) URL | Discussion URL |
|---|---|---|
| Hacker News (`hnrss`) | `<link>` | `<comments>` |
| Lobsters | `<link>` | `<comments>` |
| Reddit (`.rss`) | first outbound `<a>` in `content` (self-posts have none — treat as their own item) | `<link>` |
| Techmeme | `<link>` (already the source article) | — |

The discussion URL is stored on the story as an extra source, never as an
item of its own. A self-post or Ask HN with no outbound link is a normal item.

**Layer 2 — normalized title similarity (`internal/dedupe/title.go`).**
Free, catches most rewrites.

- Normalize: lowercase, strip punctuation, drop outlet suffixes
  (`" | Ars Technica"`), drop leading `"Show HN: "` etc.
- SimHash with Hamming distance ≤ 3, or token Jaccard ≥ 0.6.
- Compare only within a 48h window.

**Layer 3 — embedding cosine. DEFERRED. Do not build in this pass.**
Trigger to build it: after two weeks of real digests, count duplicate
stories that survived layers 1–2. If that's under ~5% of digest slots, this
layer is never written. The interface is designed so it can slot in as a
third pass over the survivors without touching layers 1–2.

- `internal/dedupe/assign.go` — run layers in order, assign `story_id`,
  update `stories`. Canonical item selection: earliest `published_at`,
  preferring a `domains.yml` primary-source domain when tied.

**Tests:** table tests for `canonical.go` and `title.go`. These are pure
functions over strings and are the highest-value tests in the project — every
downstream number depends on them, and their failures are silent. Cover the
tracking-param cases, the aggregator mappings above, outlet-suffix stripping,
and a pair of real headlines that should and should not match.

### Step 4: Embed module + content-hash cache

**Goal:** One vector per **story**, cached by content hash.

**Scope: canonical items only.** Dedupe runs first, so embedding every item
would pay for near-duplicates and produce vectors nothing reads — interest
and novelty both consume the story's canonical vector. Cost of this choice:
if dedupe layer 3 is ever built, it needs a back-fill pass over the
non-canonical items first. That is a real part of the price of deferring
layer 3, and it is worth paying.

**Embedding input:**

```
title + "\n" + categories/tags (if any) + "\n" + body_excerpt[:1500]
```

Not the first paragraph. Voyage 3 accepts 32k tokens; restricting to a lede
threw away context for nothing. 1500 characters covers the substance of
almost every news item and blog post opener without pulling in navigation
boilerplate.

- `internal/embed/client.go` — HTTP client for Voyage (default, 1024d) via
  `EMBEDDING_API_KEY` / `EMBEDDING_MODEL`. OpenAI `text-embedding-3-small`
  as a drop-in fallback. **Confirm the current model id, dimension, and
  pricing against the provider's docs at implementation time** — do not
  trust the values in this plan. Voyage takes an `input_type`: send
  `document` for items and `query` for the interests paragraph.
- `internal/embed/hash.go` — SHA-256 of the exact embedding input string.
  Cache key. Never re-embed identical content.
- `internal/embed/store.go`:

```sql
CREATE TABLE embeddings (
    item_id    INTEGER PRIMARY KEY,
    vec        BLOB NOT NULL,     -- little-endian float32[]
    model      TEXT NOT NULL,
    dim        INTEGER NOT NULL,
    input_hash TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY(item_id) REFERENCES items(id)
);
CREATE INDEX idx_embeddings_input_hash ON embeddings(input_hash);
```

- `internal/embed/embed.go` — `EmbedItem(ctx, item)`: check `input_hash`,
  call API on miss, serialize `[]float32` via `encoding/binary`.
- **Batch the API calls.** Voyage and OpenAI both accept arrays; one
  request per 100 items, not per item.

**No vector database.** Novelty compares against 30 days (~10k vectors). At
1024 dimensions that is ~10M multiplies — low single-digit milliseconds,
trivially parallel across goroutines. A vector store buys nothing and costs
cgo, extension loading, and a class of deployment problems.

**Tests:** a fake HTTP embedding server. Assert batching (101 items → 2
requests); assert an identical input string on a second run issues zero
requests; assert float32 round-trips through the BLOB unchanged.

### Step 5: Score + digest modules

**Goal:** Deterministic scoring, `digest.md`.

**Candidate set.** A run scores stories whose `last_seen` is within 24h of
now. Not `published_at` — arXiv, aggregators, and several vendor blogs
publish null or plainly wrong dates, and an item with a bad timestamp would
silently never appear. `published_at` is kept for display and for canonical
item selection only. A story that gains a new source re-enters the candidate
set and is rescored, which is correct: coverage breadth is a scoring feature.
On a first run against a cold archive the window still applies, so the first
digest is small — that is intended, not a bug.

- `internal/score/weights.go` — parse `config/weights.yml`:

```yaml
weights:
  interest:       0.35
  novelty:        0.25
  source_count:   0.15
  source_tier:    0.15
  primary_source: 0.10
novelty_window_days: 30
digest_size:         12
per_source_cap:      3
title_simhash_max:   3
candidate_window_h:  24
```

- `internal/score/compute.go` — per story:
  - **Interest** (0.35) — cosine between the story's canonical item vector
    and the embedding of the current dated section of
    `context/interests.md`.
  - **Novelty** (0.25) — inverse of max cosine to any story in the last 30
    days. This is the feature that stops the digest being 60% funding rounds.
  - **Source count** (0.15) — `log(1 + source_count)`, normalized. Falls
    out of dedupe layers 1–2 for free.
  - **Source tier** (0.15) — tier 1 = 1.0, tier 2 = 0.7, tier 3 = 0.4.
  - **Primary source** (0.10) — canonical item's domain in `domains.yml`.

**Normalize interest and novelty across the candidate set before weighting.**
Both are raw cosines, and for a news excerpt against a prose interests
paragraph those land in a narrow band — roughly 0.3–0.6 in practice. A
0.35-weighted term whose values span 0.1 contributes about 0.035 of spread
across the digest, while `source_tier` — a clean 1.0 / 0.7 / 0.4 — contributes
its full 0.15. Left raw, the digest is ranked by tier and coverage breadth
wearing an interest score as decoration, and the numbers in `weights.yml`
describe something the code does not do.

Min-max across the candidate stories of that run. When the candidate set is
smaller than 5 stories, min-max is noise: fall back to passing the raw value
through and note it in the components blob. Store **both** the raw and the
normalized value for interest and novelty — calibration needs the raw
distribution, and the gap between raw and normalized is itself the signal
that tells you whether normalization is still earning its place.

```sql
CREATE TABLE scores (
    story_id         INTEGER PRIMARY KEY,
    components       JSON NOT NULL,   -- {"interest_raw":0.51,"interest":0.72,...}
    total            REAL NOT NULL,
    interest_version TEXT NOT NULL,   -- "<section-date>:<hash8>"
    at               DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY(story_id) REFERENCES stories(id)
);

CREATE TABLE decisions (
    story_id INTEGER PRIMARY KEY,
    action   TEXT NOT NULL CHECK(action IN ('brief','dismiss','post')),
    at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY(story_id) REFERENCES stories(id)
);
```

Store components, not just totals — otherwise the review can't tell you
which feature actually predicted your clicks. `decisions` is the feedback
loop: dismissal is a labeled negative, a brief a positive, a post a strong
positive. It is also the table people skip.

**`interest_version` must invalidate.** `context/interests.md` is appended to
over time and its current section gets rewritten; a stale score computed
against an old paragraph is worse than no score. Define
`interest_version` as `<section-date>:<first 8 chars of SHA-256 of the section
text>` — e.g. `2026-08:a91f3c02`. The `score` pending-input predicate in step 2
then picks up the mismatch automatically, so editing a paragraph causes the
next `run` to rescore the window.

**No embeddings means no scores.** If `EMBEDDING_API_KEY` is unset, `embed`
skips with a warning and `items_out: 0`. `score` must then refuse: exit 2
with `{"error": "no embeddings"}`. It must **not** renormalize the remaining
weights and carry on — a digest ranked purely on tier and coverage breadth
looks completely normal, which makes it worse than no digest at all. Failure
here should be loud.

- `internal/digest/write.go` — top N by score, `per_source_cap` enforced
  (counted against the **canonical item's** source), numbered by story ID.
  Writes `digest.md` and an archived copy at `digest/YYYY-MM-DD.md`; the
  archive is what makes a weekly review possible.

```markdown
# Digest — 2026-08-30
_287 items → 231 stories → 12 shown_

## [4213] Cloudflare publishes postmortem on the 26 Aug outage  (0.81)
cloudflare.com · 4 sources · tier 1 · primary
https://blog.cloudflare.com/...
> interest 0.74 · novelty 0.88 · sources 0.45 · tier 1.00 · primary 1.00
```

Showing the component breakdown inline costs one line and makes miscalibration
visible immediately — you'll see *why* something ranked before you have to go
query for it.

**Formula, not holistic judgment.** The score decides what you *never see*,
at high volume. An uncalibrated holistic score silently drops things and you
never learn what. Holistic judgment is fine on the surviving top 20, where
you read everything anyway.

**Tests:** golden test over a fixed candidate set with hand-written vectors —
assert the exact output ordering, that `per_source_cap` drops the fourth item
from one source, and that a candidate set of 3 takes the raw-passthrough
path.

### Step 6: Stop. Run it for two weeks.

Not a build step, and the most important instruction in this document.

At this point the system works end to end with no LLM anywhere. Before
building anything deferred, measure:

- **Duplicate leakage** — how many digest slots went to a story that already
  appeared? Under ~5% and dedupe layer 3 is never written.
- **Interest separation** — do interest scores actually separate the stories
  you clicked from the ones you didn't, or do they cluster in a band? This
  is the trigger for summarize-then-embed.
- **Calibration** — label 50–100 stories by hand and take the Spearman
  correlation between the score and your own ordering. Without hand labels
  you're adjusting weights on vibes. Expect a couple of tier-3 outlets to
  earn promotion and two or three tier-1 blogs to post too rarely to matter.

Resist step 7 until the digest is genuinely worth reading.

### Step 7: Deep interactive mode

**Goal:** The one place an agent earns its place — querying the archive.

An agent is justified when it needs tools a chat window lacks, or when
repetition is high enough that a versioned prompt beats retyping. Here that
is one operation out of four: *"this vendor had an outage three weeks ago and
shipped a related fix last week"* needs a similarity search over 90 days. You
can't paste that context because you don't know it's there — which is why
you're asking.

- `modes/_shared.md` — non-negotiables, plus **the freshness protocol**:

```markdown
Before answering anything about current news:
1. Run `newsops status --json`.
2. If fetch is stale (>6h), run `newsops run --max-age 6h --json`.
3. Read `digest.md`.
Never re-fetch a source by hand; the pipeline owns collection.
Never reproduce source paragraphs. Never publish. Prefer primary sources.
The brief is the deliverable, not a draft.
```

- `modes/deep.md` — deep research on one story ID:
  - Query `news.db` for related stories over 90 days.
  - Link-following: outbound links from the story's items, filtered to
    `domains.yml` primaries, fetched and read.
  - Write the brief to `briefs/<story_id>.md` and record
    `decisions(story_id, 'brief')`.
  - Brief format, five fixed parts: core fact in one sentence · primary
    source link · what most coverage skipped or got wrong · connection to
    prior events in the archive · one thing you might disagree with.
- `context/interests.md` — dated sections, appended, never overwritten:

```markdown
## 2026-08 (current)
Agent memory and context engineering — specifically where retrieval
beats fine-tuning. Also watching eval tooling consolidate.

## 2026-06 (archived)
Inference cost curves, quantization.
```

Rewriting a paragraph takes thirty seconds; retuning fifteen numeric weights
takes an evening and you'll never do it. Keeping archived sections lets the
weekly review ask whether stated interests match actual clicks — they usually
don't, and the gap is the interesting part.

- `context/voice.md`, `context/beats.md` — templates. Voice is ~10 of your
  own posts as style anchors, beats is standing topics and topics to avoid.

**No `brief` or `draft` mode.** Extract a `brief` mode only after typing the
same framing three times; that repetition is the signal, and anticipating it
is architecture for its own sake. `draft` stays unbuilt: model-written
LinkedIn posts are recognizable, and the recognition costs more credibility
than the reach gains. Automate finding and digesting — 90% of the time cost —
and keep the writing.

---

## Critical files & anchors

| File | Symbol | Reason |
|------|--------|--------|
| `internal/store/schema.go` | Table definitions, migrations | Foundation |
| `internal/runstate/` | `LastSuccess` | Replaces cron; enables `--max-age` and `status` |
| `cmd/newsops/main.go` | Subcommand routing, `--json`, stage gating | The agent's entire contract with the pipeline |
| `internal/dedupe/canonical.go` | URL normalization | Does most of the dedupe work for zero cost |
| `internal/score/compute.go` | Weighted formula + per-run normalization | Deterministic ranking |

---

## Verification

1. **Build:** `go build ./cmd/newsops/` — single binary, no cgo.
2. **Unit tests:** `go test ./...` passes; `internal/dedupe` and
   `internal/score` are the two packages that must have real coverage.
3. **Sources:** `./newsops sources:check` — ≥80% pass after one round of URL
   fixes.
4. **Idempotence:** `./newsops fetch --max-age 6h` twice in a row — second
   run exits 0 with `{"skipped": true}` and no network traffic.
5. **Status contract:** `./newsops status --json | jq .stages.fetch.stale`
   returns `true` before any run has happened (not an error, not null).
6. **Stage gating:** with a fresh fetch and a stale score, `./newsops run`
   still scores — assert new items reach the digest rather than being
   skipped as "fresh".
7. **Lock:** two concurrent `./newsops run` — the second exits 2 with
   `{"error": "locked"}` immediately (does not hang), and
   `PRAGMA integrity_check` passes afterward.
8. **Dedupe layer 1:** seed a HN item and the article it links to; verify
   one story with `source_count: 2`, `method: "url"`.
9. **Dedupe layer 2:** two outlets covering one story with differing
   headlines; verify one story, `method: "title"`.
10. **ID stability:** run `dedupe` twice over the same data; verify no story
    ID changed and no new story rows were created.
11. **Digest:** `./newsops score && ./newsops digest` — 12 stories, ordered,
    `per_source_cap` enforced, component breakdown on each line, and
    `digest/YYYY-MM-DD.md` written.
12. **Degradation:** unset `EMBEDDING_API_KEY`, run `score` — exits 2 with
    `{"error": "no embeddings"}` and writes no `scores` rows.
13. **Deep mode:** load `modes/deep.md`, ask for a story ID; verify it calls
    `status` first and returns real archive connections.

---

## Assumptions & contingencies

- **Embedding provider:** Voyage 3 (1024d) by default via env var. OpenAI
  `text-embedding-3-small` is a drop-in. Missing key → `embed` skips with a
  warning; `score` then fails loudly rather than producing a plausible-looking
  digest ranked on 40% of its weights.
- **Summarize-then-embed (deferred):** not built now. It helps interest
  matching but actively *hurts* dedupe — a summary abstracts away exactly the
  entity names and version numbers that distinguish near-identical stories —
  and it puts an LLM call plus nondeterminism back into the batch loop.
  **Trigger:** if after two weeks interest scores fail to separate things you
  clicked from things you didn't, add a *second* vector
  (`embeddings.kind = 'summary'`) computed only for the ~40 stories that
  survive the prefilter — not all 300 items.
- **Dedupe layer 3 (deferred):** see step 3. Measure before building, and
  remember it requires a back-fill of non-canonical item embeddings.
- **arXiv volume:** cs.LG alone runs to hundreds of papers daily.
  `per_source_cap: 3` handles it mechanically. If it still floods, filter
  subcategories or raise its interest threshold specifically.
- **Techmeme:** already an editorially clustered feed with human editors.
  Treat divergence from our dedupe as a tuning signal, not a bug.
- **Interest blending from `decisions`:** deferred until two weeks of real
  usage. Initial scoring uses stated interests only. Cap implicit signal at
  30% when added — pure implicit feedback collapses into showing you more of
  yesterday, the opposite of tracking a shifting interest.
- **No cron anywhere.** If this later moves to a server, `newsops run
  --max-age 6h` is already the right cron line — the guard means cron and
  agent invocation can coexist safely.

---

## Decisions log

| Decision | Rationale |
|---|---|
| Two loops, human pick between them | The human pick is the value; automating it defeats the purpose |
| No LLM in the batch loop | Deterministic, free, reproducible |
| Dedupe before embedding | Layers 1–2 need no vectors and cut the volume to embed |
| Canonical items only get vectors | One vector per story is exactly what scoring reads |
| URL canonicalization over embedding clustering | Aggregators link to the original; canonicalizing dedupes them exactly, for free |
| Formula scoring, not holistic | The score decides what you never see; uncalibrated judgment drops things silently |
| Normalize interest and novelty per run | Raw cosines occupy a narrow band; unnormalized, the weights are fiction |
| Brute-force cosine, no vector DB | 10k vectors is milliseconds; a vector store adds only deployment risk |
| Hosted embedding API | Local models are a Python ecosystem; cents/month at this volume |
| Interests as prose, not weights | A paragraph gets rewritten; weights never get retuned |
| Story IDs permanent | Briefs and decisions reference them; silent renumbering is unrecoverable |
| Fail loudly with no embeddings | A digest missing 60% of its weights looks normal, which is worse |
| Brief, not draft | Model-written posts are recognizable; the credibility cost exceeds the reach gain |
| One mode file, not four | An agent earns its place only where it needs tools chat lacks — here, the archive query |
