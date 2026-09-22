# Brief Mode — Research One Story

## Procedure

1. Read `modes/_shared.md`.
2. `newsops show <id> --json` — the story, its items, its sources, its history.
3. Fetch and read the canonical URL. Actually read it; do not brief from the
   excerpt.
4. Follow outbound links **filtered to `config/domains.yml` primaries** — the
   changelog, the paper, the filing, the postmortem. Cap at 5 fetches.
5. `newsops query --story <id> --related --days 90 --json` for archive
   connections. Run at most 2 further `--text` queries to chase a specific
   hunch.
6. Write `briefs/<story_id>-<slug>.md`.
7. `newsops decide <id> briefed`.
8. **Stop.** The brief is the deliverable. Do not draft.

## Slug Rule

Lowercase the title, keep `[a-z0-9]`, collapse runs to single hyphens, trim
to 60 characters at a word boundary. `4213-cloudflare-postmortem-on-the-26-aug-outage`.

## Brief Format

```markdown
# [4213] Cloudflare publishes postmortem on the 26 Aug outage

**Core fact.** One sentence. What actually happened.

**Primary source.** https://blog.cloudflare.com/... (+ the changelog, the
paper, whatever the real artifact is)

**What coverage skipped.** The thing every outlet got wrong, glossed, or
didn't chase. If coverage was accurate and complete, say that — it is a
legitimate finding, not a failure to find an angle.

**Prior events.** [3907] July edge outage, same subsystem. [3712] The
mitigation they shipped in June. With story IDs, so they can be looked up.
Write "no prior coverage found" when there is none — that is also information.

**One thing to disagree with.** A claim in the coverage or the source that is
arguable, and why.

---
_Sources read: 4 · Archive queries: 2_
```

## Rules

- Never reproduce source paragraphs. Every sentence is written fresh.
- Quote at most one short phrase, and only when the exact wording is the point.
- Prefer the primary artifact over anyone's description of it.
- Cite story IDs for archive connections so they can be verified.
- If the primary source is paywalled or unreachable, say so in the brief
  rather than briefing from coverage as if it were the source.
- Length: aim for 200–400 words. A brief that runs long has become the article.
