# T005 — Config files + loaders

**Depends on:** T001
**Size:** M
**Touches:** `config/sources.yml`, `config/domains.yml`, `config/prefilter.yml`, `internal/config/`

## Why

Three files hold every input the deterministic half needs. They are also the
files you will edit most often — feed URLs drift, and the prefilter knobs are
the first thing you reach for when the candidate list feels wrong. A loader
that fails fast with a clear message is worth more here than anywhere else,
because a bad `sources.yml` otherwise surfaces as a silently short digest.

Note what is **not** here: no interest weights, no scoring numbers. Interest
lives in `context/interests.md` as prose the agent reads. A paragraph gets
rewritten in thirty seconds; a weight never gets retuned.

## Deliverables

### `config/sources.yml`

Tier is a coarse quality hint: **1** = primary or high signal-to-noise ·
**2** = solid reporting · **3** = volume.

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

**These URLs are unverified and feed paths drift.** Expect several to fail the
first `sources:check` (T009). That is the expected cost of a source list, not
a bug in this ticket.

### `config/domains.yml`

Primary-source domains. Used for canonical-item selection when two items tie
on date (T012), and as the allowlist for the agent's link-following during
briefs (T020).

```yaml
primary_domains:
  - arxiv.org
  - blog.cloudflare.com
  - aws.amazon.com
  - github.blog
  - openai.com
  - anthropic.com
  - deepmind.google
  - research.google
  - huggingface.co
  - postgresql.org
  - kernel.org
  - lwn.net
  - golang.org
  - go.dev
  - rust-lang.org
  - python.org
  - kubernetes.io
  - engineering.fb.com
  - netflixtechblog.com
  - stripe.com
  - discord.com
  - shopify.engineering
  - googleprojectzero.blogspot.com
  - queue.acm.org
```

### `config/prefilter.yml`

```yaml
candidate_limit:      40   # how many the agent skims per run
per_source_cap:        3   # stops arXiv and TechCrunch owning the list
title_simhash_max:     3   # Hamming distance for title-based dedupe
max_backfill_days:    30   # bounds a first run or a return from holiday
exclude_terms: []          # hard title drops, case-insensitive substrings
```

## Interface

```go
type Source struct {
    Name string `yaml:"name"`
    Tier int    `yaml:"tier"`
    URL  string `yaml:"url"`
}

type Prefilter struct {
    CandidateLimit  int      `yaml:"candidate_limit"`
    PerSourceCap    int      `yaml:"per_source_cap"`
    TitleSimhashMax int      `yaml:"title_simhash_max"`
    MaxBackfillDays int      `yaml:"max_backfill_days"`
    ExcludeTerms    []string `yaml:"exclude_terms"`
}

type Config struct {
    Sources        []Source
    PrimaryDomains map[string]bool // set, for O(1) lookup
    Prefilter      Prefilter
}

func Load(dir string) (*Config, error)

// IsPrimary reports whether a URL's host (minus "www.") is a primary domain.
func (c *Config) IsPrimary(rawURL string) bool
```

Dependency: `gopkg.in/yaml.v3`.

Validation, all fatal with a message naming the offending entry:
- source names unique and non-empty
- `tier` in 1..3
- `url` parses and is http/https
- `candidate_limit`, `per_source_cap`, `max_backfill_days` all > 0
- `title_simhash_max` in 0..64

Defaults, applied when a key is absent from `prefilter.yml` (a missing file is
also fine and yields all defaults): the values shown above.

## Acceptance criteria

- [ ] `Load("config/")` on the shipped files returns 51 sources and no error.
- [ ] Duplicate source name → error naming the duplicate.
- [ ] `tier: 0` or `tier: 4` → error naming the source.
- [ ] `url: "not a url"` or an `ftp://` URL → error naming the source.
- [ ] Missing `prefilter.yml` → all defaults, no error.
- [ ] Partial `prefilter.yml` (only `candidate_limit`) → that value plus
      defaults for the rest.
- [ ] `IsPrimary("https://www.arxiv.org/abs/1234")` is true;
      `IsPrimary("https://techcrunch.com/...")` is false.

## Tests

Table tests over small fixture directories in `internal/config/testdata/`, one
per validation failure. Plus one test that loads the real `config/` directory
and asserts the source count, so an accidental YAML break is caught.

## Out of scope

Fetching anything. Verifying that the URLs work — that is `sources:check`
(T009).
