# T011 — Title normalization + SimHash

**Depends on:** T005
**Size:** M
**Touches:** `internal/dedupe/title.go`

## Why

Layer 1 (T010) catches everything that shares a URL. What it misses is two
outlets writing their own headline about the same event. Comparing normalized
titles is free, needs no vectors, and catches most of the remainder.

The risk here is the opposite of missing a duplicate: **merging two stories
that are not the same**. "Postgres 18 released" and "Postgres 17 released"
differ by one token and must not collapse. A false merge is invisible — you
never see the story that got swallowed — so the tests below matter more than
the implementation.

## Interface

```go
// NormalizeTitle strips the decoration outlets add, leaving comparable text.
func NormalizeTitle(title string, sourceNames []string) string

// SimHash returns a 64-bit fingerprint of the normalized title.
func SimHash(normalized string) uint64

// Hamming is the bit distance between two fingerprints.
func Hamming(a, b uint64) int

// SimilarTitles reports whether two raw titles are the same story, at the
// configured maximum Hamming distance.
func SimilarTitles(a, b string, sourceNames []string, maxDist int) bool
```

### Normalization

1. Decode HTML entities; NFKC-normalize Unicode.
2. Strip an outlet suffix: a trailing ` | X`, ` - X`, ` — X`, ` · X` where `X`
   either matches a name from `sources.yml` (case-insensitive) or is at most
   four words. Only strip **one** suffix.
3. Strip known prefixes: `Show HN: `, `Ask HN: `, `Tell HN: `, `Launch HN: `,
   and Reddit tags `[P]`, `[R]`, `[D]`, `[N]`.
4. Lowercase.
5. Replace punctuation with spaces; collapse whitespace; trim.

Numbers and version strings are **kept** — they are exactly what distinguishes
"Postgres 18" from "Postgres 17". Do not strip digits, and do not stem.

### SimHash

Word tokens, not character shingles: character shingles make near-identical
version numbers collide, which is the failure mode this ticket most needs to
avoid. Weight each token by its length in characters (a cheap stand-in for
informativeness — "cloudflare" should outweigh "the"). Hash tokens with
FNV-1a into 64 bits, accumulate signed weights per bit, and take the sign bits.

Titles shorter than 4 tokens after normalization return a fingerprint but
`SimilarTitles` must return false for them regardless of distance — short
titles collide too easily to trust.

## Acceptance criteria

Must match (distance ≤ 3):

- [x] `"Cloudflare publishes outage postmortem | Ars Technica"` and
      `"Cloudflare publishes postmortem on outage"`
- [x] `"Show HN: A tool for X"` and `"A tool for X"`
- [x] `"OpenAI Ships New Model"` and `"OpenAI ships new model"`

Must **not** match:

- [x] `"Postgres 18 released"` and `"Postgres 17 released"`
- [x] `"AWS launches new region in Spain"` and
      `"AWS launches new region in Japan"`
- [x] `"Rust 1.90 announced"` and `"Go 1.90 announced"`
- [x] Any pair where either title normalizes to fewer than 4 tokens

Plus:

- [x] `NormalizeTitle` strips exactly one outlet suffix, not two
- [x] A title that is *only* an outlet suffix does not normalize to empty
      string chaos — it returns the original lowercased
- [x] `Hamming(x, x) == 0`; `SimHash` is deterministic across runs

## Tests

Two table tests with 34+ cases total, each labelled match / no-match,
including every acceptance line above. Takes "must not match" cases from
real headline patterns — version bumps, region names, product names — since
those are where this fails in production.

## Out of scope

The 48h window and story assignment (T012). Which distance to use — that is
`title_simhash_max` from `prefilter.yml` (T005), passed in.
