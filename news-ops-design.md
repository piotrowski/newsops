# news-ops — design document

A personal IT-news pipeline: collect, deduplicate, score against current
interests, research the ones worth researching, and write a LinkedIn post
from a brief.

---

## 1. Scope and non-goals

**In scope:** feed ingestion, cross-outlet deduplication, interest-based
ranking, a daily digest, and an on-demand research step that can query the
archive.

**Non-goals:**

- **No auto-publishing.** The system produces a brief. You write the post.
- **No agent in the batch loop.** Ingestion through digest is deterministic.
- **No scraping of hostile sources.** LinkedIn, Indeed, and similar block
  scrapers and forbid it in their terms. Not worth the trouble.
- **No stored article bodies beyond what's needed.** Republishing source
  text is infringement; summaries must be written from scratch.

---

## 2. Architecture: two loops

The core structural decision. These are separate programs sharing one
database.

**Loop 1 — batch, scheduled, no LLM.**
`fetch → cluster → score → digest`. Runs on cron. Same input produces the
same output. Writes `digest.md` with numbered cluster IDs.

**Loop 2 — interactive, on demand, LLM.**
You read the digest, pick an item, run `deep` on its ID. Fires maybe twice
a week.

The human pick sits between them deliberately. That judgment is the entire
value of the system; automating it means posting about things you don't
care about.

---

## 3. Repository layout

```
news-ops/
├── AGENTS.md                # canonical agent instructions
├── CLAUDE.md                # thin wrapper importing AGENTS.md
├── config/
│   ├── sources.yml          # feeds (see §8)
│   ├── domains.yml          # primary-source domains for link-following
│   └── weights.yml          # every tunable number
├── context/
│   ├── interests.md         # dated; the actual product
│   ├── voice.md             # ~10 of your own posts, style anchors
│   └── beats.md             # standing topics, and topics to avoid
├── modes/
│   ├── _shared.md           # loaded by every mode
│   └── deep.md              # the only mode built up front
├── cmd/newsops/
│   └── main.go              # one binary, subcommands
├── internal/
│   ├── feed/                # gofeed + concurrent fetch
│   ├── store/               # sqlite, schema, migrations
│   ├── embed/               # API client + content-hash cache
│   ├── cluster/             # cosine, online clustering
│   ├── score/               # weights.yml → ranking
│   └── digest/              # → digest.md
├── data/                    # news.db, cache/   (gitignored)
├── briefs/                  # (gitignored)
└── posts/                   # (gitignored)
```

Everything above `cmd/` is language-agnostic. The LLM half never learns what
produced `digest.md`, so the pipeline could be rewritten in another language
without touching a prompt.

---

## 4. Stack

**Go**, for a single static binary cron can call with no dependency drift,
and real concurrency on the fetch stage.

| Concern | Choice | Why |
|---|---|---|
| Feeds | `mmcdole/gofeed` | Handles the RSS/Atom format zoo |
| Storage | `modernc.org/sqlite` | Pure Go, no cgo, easy cross-compile |
| Embeddings | Hosted API (Voyage / OpenAI / Cohere) | Local sentence-transformers are a Python ecosystem; ONNX-from-Go is unpleasant. Cents per month at this volume |
| HTML extraction | `go-shiori/go-readability` | Fallback only, for truncated feeds |
| Vectors | `BLOB` of little-endian float32 | See below |

**No vector database.** Clustering compares against a 48-hour window (~600
vectors); novelty against 30 days (~10k). At 384 dimensions that is a few
million multiplications — sub-millisecond, trivially parallel across
goroutines. A vector store buys nothing and costs cgo, extension loading,
and a class of deployment problems.

**Concurrency:** `errgroup` with `SetLimit(10)` plus a per-host limiter.
Send `ETag` / `If-Modified-Since` so unchanged feeds cost nothing.

---

## 5. Data model

```
items       (id, url, title, source, published_at, first_seen,
             body_hash, raw_path)
clusters    (id, canonical_item_id, size, window_start, window_end)
embeddings  (item_id, vec BLOB, model, created_at)
scores      (cluster_id, components JSON, total, interest_version, at)
decisions   (cluster_id, action, at)
```

Two notes that matter more than they look:

**Store score *components*, not just the total.** Otherwise the weekly
review can't tell you which feature actually predicted your clicks.

**`decisions` is the feedback loop.** Dismissal is a labeled negative, a
brief is a positive, a post is a strong positive. This is the table that
makes the system improve; it's also the one people skip.

---

## 6. Pipeline

### fetch
Pull `sources.yml` concurrently. Cache raw bodies to disk keyed by URL and
date — you will reprocess many times while tuning, and refetching is slow,
rude, and gets you blocked. Fall back to readability extraction only when a
feed returns a truncated summary.

### cluster
The central problem. One story appears as a press release, then twenty
rewrites within six hours, then a HN thread, then the takes.

Embed title + first paragraph. Single-pass online clustering against a
rolling 48-hour window, cosine similarity, tuned threshold (start ~0.82).
Pick a canonical item per cluster: earliest timestamp, or the one linking
to the primary source.

**Cluster before any LLM call.** You evaluate the representative, not all
twenty rewrites. Roughly a 10× cost cut on its own.

Cluster size then becomes a free feature — breadth of coverage is a decent
proxy for importance.

### score
Deterministic, from `weights.yml`:

```yaml
weights:
  interest:      0.35   # cosine to current interests.md section
  novelty:       0.25   # distance from everything in last 30 days
  cluster_size:  0.15   # coverage breadth
  source_tier:   0.15   # hand-assigned, see §8
  primary_source: 0.10  # links to paper/changelog/filing vs another outlet
novelty_window_days: 30
cluster_threshold:   0.82
digest_size:         12
per_source_cap:      3
```

**Novelty is the feature that saves the project.** Without it the digest is
60% "AI startup raises round" forever.

**Formula, not holistic judgment.** career-ops uses a holistic 1–5 score and
is right to: low volume, human reads every output, score is a sorting aid.
Here the score decides what you *never see*, at high volume. An uncalibrated
holistic score silently drops things and you never learn what. Holistic
judgment is fine on the surviving top 20, where you read everything anyway.

### digest
Top N to `digest.md`, numbered cluster IDs, grouped loosely by topic. A
numbered list and a reply is the whole interface. Resist building a web UI
until you've used it for a month.

---

## 7. The interactive half

### `context/interests.md` — the product

Everything else is scaffolding. Dated sections, appended, never overwritten:

```markdown
## 2026-08 (current)
Agent memory and context engineering — specifically where retrieval
beats fine-tuning. Also watching eval tooling consolidate.

## 2026-06 (archived)
Inference cost curves, quantization.
```

`score` embeds the current section and ranks by cosine similarity to it.
Rewriting a paragraph takes thirty seconds; retuning fifteen numeric weights
takes an evening and you'll never do it.

Blend in implicit signal from `decisions`, but cap it around 30%. Pure
implicit feedback collapses into showing you more of yesterday — the
opposite of tracking a shifting interest.

Keeping archived sections lets the weekly review ask whether stated
interests match actual clicks. They usually don't, and the gap is the
interesting part.

### `modes/deep.md` — the only mode worth building

An agent earns its place when it needs tools a chat window lacks, or when
repetition is high enough that a versioned prompt beats retyping. For this
system that is **one operation out of four**.

What a chat window can't do: query `news.db`. *"This vendor had an outage
three weeks ago and shipped a related fix last week"* needs a similarity
search over 90 days of clusters. You can't paste that context because you
don't know it's there — that's why you're asking. That connection is also
what makes a post worth reading rather than another summary of the same
press release.

Secondary: the link-following loop. Pull outbound links from the cluster,
filter to `domains.yml` primaries, fetch the changelog or paper. Five to ten
tool calls you'd otherwise skip on a Tuesday, ending up writing from
coverage instead of sources.

**Brief format** — fixed shape, five parts:

1. Core fact, in one sentence
2. Primary source link
3. What most coverage skipped or got wrong
4. Connection to prior events in the archive
5. One thing you might disagree with

### `modes/_shared.md` — the non-negotiables

Loaded by every mode. Never reproduce source paragraphs. Never auto-publish.
Prefer primary source over coverage. The brief is the deliverable, not a
draft.

### On drafting the post

**Produce a brief, not a draft.** Model-written LinkedIn posts are
recognizable, and the recognition costs more credibility than the reach
gains. The asset being built is that people associate your name with good
judgment on a topic — outsourcing the sentences outsources the thing being
judged. Automate finding and digesting (90% of the time cost), keep the
writing.

If you do want something to react against later, few-shot from
`context/voice.md` — ten of your own posts as anchors — rather than any
instruction about tone.

Widely reported that LinkedIn suppresses reach on posts with external links,
with the workaround being link-in-first-comment. Test against your own
numbers; platform behaviour moves.

---

## 8. Sources

Tier maps to the `source_tier` weight. **1** = primary or high
signal-to-noise · **2** = solid reporting · **3** = volume, useful mainly
for the coverage-breadth signal.

Weighted toward backend / infra / AI.

```yaml
sources:
  # Aggregators & community
  - {name: Hacker News,        tier: 1, url: "https://hnrss.org/frontpage?points=100"}
  - {name: Techmeme,           tier: 1, url: "https://www.techmeme.com/feed.xml"}
  - {name: Lobsters,           tier: 1, url: "https://lobste.rs/rss"}
  - {name: r/programming,      tier: 2, url: "https://www.reddit.com/r/programming/top/.json?t=day"}
  - {name: r/MachineLearning,  tier: 2, url: "https://www.reddit.com/r/MachineLearning/top/.json?t=day"}
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

**These URLs are unverified — feed paths drift.** Write
`newsops sources:check` as the first subcommand: GET each, confirm 200 and
that `gofeed` parses at least one item, print failures. Twenty lines, and it
saves debugging a silently-empty pipeline later. Run monthly.

**Techmeme is doing something different** — it's already a clustering system
with human editors. Treat it as validation for your own clustering; if your
clusters and its groupings diverge badly, the threshold is off.

**arXiv will flood you.** cs.LG alone runs to hundreds of papers daily.
Filter subcategories or gate it behind a much higher interest threshold.
`per_source_cap` stops any one feed occupying the digest.

---

## 9. Build order

1. **`store` + schema.** Everything depends on it.
2. **`feed` + `sources:check`.** Get real data landing on disk before
   anything clever.
3. **`embed` with content-hash cache.** Never re-embed an item.
4. **`cluster`.** Tune the threshold by eye against a day of real data —
   this is where the manual labelling pays off.
5. **`score` + `digest`.** Now you have a working system with no LLM at all.
   **Run it for two weeks here.** Resist step 6.
6. **`modes/deep.md`.** Only after the digest is genuinely worth reading.

Extract a `brief` mode only when you notice yourself typing the same framing
three times. That repetition is the signal; anticipating it is architecture
for its own sake.

---

## 10. Calibration

Label 50–100 clusters by hand before tuning anything. Then:

- **Clustering:** did same-story items land together? Tune threshold.
- **Ranking:** Spearman correlation between score and your own ordering.
- **Ongoing:** click-through by `source_tier`, and which score component
  best predicts a click.

Without hand labels you're adjusting weights on vibes. Expect a couple of
tier-3 outlets to earn promotion and two or three tier-1 blogs to post too
rarely to matter.

---

## 11. Decisions log

| Decision | Rationale |
|---|---|
| Two separate programs, not one agent | The human pick is the value; automating it defeats the purpose |
| No LLM in the batch loop | Deterministic, free, reproducible |
| Cluster before scoring | ~10× cost cut; cluster size is a free importance feature |
| Formula scoring, not holistic | Score decides what you never see; uncalibrated judgment drops things silently |
| Brute-force cosine, no vector DB | 10k vectors is sub-millisecond; a vector store adds only deployment risk |
| Hosted embedding API | Local models are a Python ecosystem; cents/month at this volume |
| Interests as prose, not weights | A paragraph gets rewritten; weights never get retuned |
| Brief, not draft | Model-written posts are recognizable; the credibility cost exceeds the reach gain |
| One mode file, not four | An agent earns its place only where it needs tools chat lacks — here, the archive query |
