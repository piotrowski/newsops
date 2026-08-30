# news-ops implementation plan

## Context

Build a personal IT-news pipeline in Go: fetch RSS feeds, cluster items by cosine similarity on embeddings, score against current interests, and produce `digest.md`. A separate interactive mode (`deep`) queries the archive via LLM. Two loops — batch (deterministic, cron) and interactive (on-demand, LLM). Single binary with subcommands.

## Approach

### Step 1: Project scaffold + store module

**Goal:** Go module, directory layout, SQLite schema, CRUD for `items` table.

- Initialize Go module (`news-ops`) at repo root.
- Create directory tree: `cmd/newsops/`, `internal/feed/`, `internal/store/`, `internal/embed/`, `internal/cluster/`, `internal/score/`, `internal/digest/`, `config/`.
- Write `internal/store/schema.go`: single SQLite table via `modernc.org/sqlite` (pure Go, no cgo):

```sql
CREATE TABLE items (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    url TEXT NOT NULL UNIQUE,
    title TEXT NOT NULL,
    source TEXT NOT NULL,
    published_at DATETIME,
    first_seen DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    body_hash TEXT NOT NULL,
    raw_path TEXT
);
CREATE INDEX idx_items_published ON items(published_at);
CREATE INDEX idx_items_first_seen ON items(first_seen);
```

- Write `internal/store/db.go`: open/close, transaction helpers.
- Write `internal/store/items.go`: `InsertItem`, `GetItemsByDateRange`, `CountItemsInWindow` — the three queries the pipeline needs.
- Create `config/sources.yml` with all sources from design doc §8 (tier 1–3).

### Step 2: Feed module + `sources:check` subcommand

**Goal:** Concurrently fetch feeds, parse items, cache raw bodies to disk.

- Write `internal/feed/parser.go`: wrapper around `mmcdole/gofeed`. Parse RSS/Atom, extract title, link, published date, description/content.
- Write `internal/feed/fetcher.go`: concurrent fetch with `errgroup` + `SetLimit(10)` + per-host limiter via `golang.org/x/sync/semaphore`. Send `ETag` / `If-Modified-Since` headers for unchanged feeds (304 = skip).
- Write `internal/feed/cache.go`: store raw response body to `data/cache/<url_hash>/<date>` keyed by URL + date. On fetch miss, read from cache if available and within 48h.
- Write `cmd/newsops/main.go` with subcommand routing: `sources:check`, `fetch`, `cluster`, `score`, `digest`, `deep`.
- Implement `sources:check`: GET each source URL, confirm HTTP 200, parse with gofeed and verify at least one item returned. Print failures. No side effects.

### Step 3: Embed module + content-hash cache

**Goal:** Generate embeddings for titles+first paragraphs, skip re-embedding identical content.

- Write `internal/embed/client.go`: HTTP client wrapping Voyage/OpenAI/Cohere embedding API. Configurable via env var (`EMBEDDING_API_KEY`, `EMBEDDING_MODEL`). Default to a reasonable provider (Voyage 3 at 1024d, or OpenAI text-embedding-3-small at 1536d — design says 384d; use Voyage 3 which outputs 1024d and note the dimension in code).
- Write `internal/embed/hash.go`: SHA-256 of (title + first 500 chars of body) as content hash. Store in `items.body_hash`.
- Write `internal/embed/store.go`: `embeddings` table:

```sql
CREATE TABLE embeddings (
    item_id INTEGER PRIMARY KEY,
    vec BLOB NOT NULL,          -- little-endian float32[]
    model TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY(item_id) REFERENCES items(id)
);
```

- Write `internal/embed/embed.go`: `EmbedItem(ctx, item)` — check content hash cache first; if new, call API, store vec as BLOB (serialize []float32 to bytes via `encoding/binary`). Return vec.

### Step 4: Cluster module

**Goal:** Single-pass online clustering against a rolling 48-hour window using cosine similarity.

- Write `internal/cluster/cosine.go`: `CosineSimilarity(a, b []float32) float64`.
- Write `internal/cluster/window.go`: load all embeddings from last 48 hours from DB. For each new item, compare against existing cluster centroids (mean of member vectors). If max similarity >= threshold (~0.82), assign to that cluster; else create new cluster.
- Write `internal/cluster/model.go`:

```sql
CREATE TABLE clusters (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    canonical_item_id INTEGER NOT NULL,
    size INTEGER NOT NULL DEFAULT 1,
    window_start DATETIME NOT NULL,
    window_end DATETIME NOT NULL,
    FOREIGN KEY(canonical_item_id) REFERENCES items(id)
);
CREATE INDEX idx_clusters_window ON clusters(window_start, window_end);
```

- Write `internal/cluster/assign.go`: `AssignClusters(ctx, newItems []Item)` — load window embeddings, run single-pass clustering, update clusters table. Pick canonical item per cluster: earliest `published_at`, or the one linking to a primary source (domain from `config/domains.yml`).
- Create `config/domains.yml`: list of primary-source domains for link-following priority in canonical selection.

### Step 5: Score + digest modules

**Goal:** Deterministic scoring from weights, produce `digest.md`.

- Write `internal/score/weights.go`: parse `config/weights.yml` (interest, novelty, cluster_size, source_tier, primary_source weights; novelty_window_days=30, cluster_threshold=0.82, digest_size=12, per_source_cap=3).
- Write `internal/score/compute.go`: for each cluster:
  - **Interest** (0.35): cosine similarity between cluster representative embedding and current month's `context/interests.md` embedding.
  - **Novelty** (0.25): inverse of max cosine similarity to any item in last 30 days. Higher distance = higher novelty score.
  - **Cluster size** (0.15): normalized cluster size (log scale to prevent domination).
  - **Source tier** (0.15): mapped from source tier (tier 1=1.0, tier 2=0.7, tier 3=0.4).
  - **Primary source** (0.10): boolean — does the canonical item link to a domain in `domains.yml`?
- Write `internal/score/model.go`:

```sql
CREATE TABLE scores (
    cluster_id INTEGER PRIMARY KEY,
    components JSON NOT NULL,   -- {"interest": 0.72, "novelty": 0.45, ...}
    total REAL NOT NULL,
    interest_version TEXT NOT NULL,  -- month from interests.md
    at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY(cluster_id) REFERENCES clusters(id)
);

CREATE TABLE decisions (
    cluster_id INTEGER PRIMARY KEY,
    action TEXT NOT NULL CHECK(action IN ('brief', 'dismiss', 'post')),
    at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY(cluster_id) REFERENCES clusters(id)
);
```

- Write `internal/digest/write.go`: produce `digest.md` — top N clusters (default 12), numbered by cluster ID, grouped loosely by topic (sort by score descending; cap per source at 3). Format:

```markdown
# Digest — YYYY-MM-DD

## 1. [Cluster title] (score: X.XX)
Source: Hacker News | Cluster size: 5
URL: https://...

## 2. ...
```

- Wire all subcommands in `cmd/newsops/main.go`: `fetch`, `cluster`, `score`, `digest` — each runs its stage and exits.

### Step 6: Deep interactive mode

**Goal:** LLM-powered archive query via Claude Code agent with `modes/deep.md`.

- Write `modes/_shared.md`: non-negotiable rules (no auto-publish, no republishing source text, prefer primary sources).
- Write `modes/deep.md`: agent instructions for deep research mode. Includes:
  - Query the SQLite database via SQL tool calls to find related clusters over 90 days.
  - Link-following loop: extract outbound links from cluster items, filter to `domains.yml` primaries, fetch and summarize changelogs/papers.
  - Brief format (5 parts): core fact, primary source link, what coverage missed, connection to prior events, one thing to disagree with.
- Create `context/interests.md`: dated sections template.
- Create `context/voice.md`: empty template for future voice anchors.
- Create `context/beats.md`: standing topics and topics to avoid.

## Critical files & anchors

| File | Symbol | Reason |
|------|--------|--------|
| `internal/store/schema.go` | Table definitions | Foundation — everything depends on this |
| `cmd/newsops/main.go` | Subcommand routing | Single binary entry point |
| `internal/feed/fetcher.go` | Concurrent fetch + ETag/304 | First real data pipeline step |
| `internal/cluster/window.go` | Single-pass online clustering | Core algorithm — threshold tuning happens here |
| `internal/score/compute.go` | Weighted scoring formula | Deterministic ranking logic |

## Verification

1. **Build:** `go build ./cmd/newsops/` — compiles to single binary with no cgo dependencies (modernc.org/sqlite is pure Go).
2. **sources:check:** `./newsops sources:check` — prints pass/fail for each source URL; at least 80% should pass initially.
3. **fetch + cluster cycle:** Run `./newsops fetch && ./newsops cluster` on a day's data. Verify `digest.md` is produced with numbered clusters, no duplicates of the same story.
4. **Score + digest:** `./newsops score && ./newsops digest` — verify top 12 clusters appear in order, per-source cap enforced (max 3 from any single source).
5. **Deep mode:** Run Claude Code with `modes/deep.md` loaded; query the database for a past cluster and verify it returns relevant archive connections.

## Assumptions & contingencies

- **Embedding provider choice:** Default to Voyage 3 (1024d vectors) via env var. If the API key isn't set, pipeline stages that need embeddings skip gracefully with a warning. Fallback: OpenAI text-embedding-3-small — same interface, just different model name.
- **arXiv volume:** cs.LG alone can produce hundreds of items daily. The `per_source_cap=3` in weights.yml handles this mechanically; no special-case code needed. If arXiv still floods the digest, increase cap or add a subcategory filter later.
- **Techmeme divergence:** If Techmeme clusters diverge from ours significantly, it's a threshold tuning signal, not a bug. The design doc explicitly calls this out as validation, not ground truth.
- **Interest blending from decisions:** The design mentions blending implicit feedback capped at 30%. This is deferred to post-calibration — the initial score uses only stated interests from `context/interests.md`. Add decision-blending after two weeks of real usage data.
