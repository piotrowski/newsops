# T022 — Voice corpus + `modes/voice.md`

**Depends on:** T018
**Size:** M
**Touches:** `context/voice/`, `context/voice-guide.md`, `modes/voice.md`

## Why

Drafting only works if the output sounds like you. Two mechanisms, both
needed:

- **Samples** — tone transfer from examples beats any instruction about tone.
- **A derived guide** — because when a draft comes out sounding wrong, an
  explicit rule is something you can *edit*. A sample set is not.

Samples alone leave you with no correction surface. A hand-written guide alone
fails because people are famously bad at describing their own writing. This
ticket builds both, with the guide derived from the samples so it stays honest.

## Deliverables

### `context/voice/README.md`

Instructions for the corpus: 8–12 of your own posts, one per file, the ones
you would be happy to have written again. Not your most popular — your most
characteristic.

Each file:

```markdown
---
platform: linkedin   # or: blog
date: 2026-05-14
note: did well, ~40 comments   # optional
---

The post text, exactly as published.
```

Ship the README plus one clearly-marked `EXAMPLE.md` so the format is
unambiguous. The real posts are the user's to add, and until they exist this
ticket's output is a guide that correctly refuses to generate.

### `modes/voice.md`

Procedure: read every file in `context/voice/`, then write
`context/voice-guide.md` with these fixed sections:

1. **Shape and length** — typical word count per platform; paragraph length;
   whether lists appear.
2. **Openings** — how the first sentence works across the samples. Quote three
   real openings.
3. **Rhythm** — sentence length variance, fragments, where emphasis lands.
4. **Vocabulary and tics** — words and constructions that recur; hedges;
   whether jargon is defined or assumed.
5. **Structural moves** — the recurring shapes (observation → complication →
   position; anecdote → generalization; etc.).
6. **Never does** — constructions absent from every sample. This section does
   the most work at draft time.
7. **Worked example** — one sample, annotated line by line with which rule it
   demonstrates.

**Hard rules for this mode:**

- **Every rule must cite at least two sample filenames.** A rule derived from
  one post is a coincidence.
- **Never invent a rule not evidenced in the samples.** Not "uses a
  conversational tone" unless you can point at what makes it so.
- **Refuse below 5 samples.** Say plainly that the corpus is too small and
  what to add. A guide extrapolated from three posts is worse than none — it
  will confidently encode accidents.
- The guide is regenerable and hand-editable. State at the top of the
  generated file that human edits survive regeneration only if copied out
  first, so nobody loses their corrections.

## Acceptance criteria

- [ ] `context/voice/README.md` documents the front-matter format, with a
      complete `EXAMPLE.md`.
- [ ] With fewer than 5 samples, the mode refuses and names the shortfall.
      It does not produce a partial guide.
- [ ] With 8+ samples, `context/voice-guide.md` is produced with all seven
      sections present and non-empty.
- [ ] Every rule cites ≥2 sample filenames. Check every rule in section 6.
- [ ] Section 2 quotes three real openings verbatim from the corpus.
- [ ] Regenerating after adding two samples changes the guide (it is derived,
      not cached) and does not crash on the added front-matter.
- [ ] The guide contains no rule you cannot trace to a sample — read it
      against the corpus and check.
- [ ] Nothing in `internal/` parses `context/voice/`; it is read by the agent
      only.

## Tests

Manual. The real test is the next ticket: a draft produced with this guide
should need less rewriting than one produced without it. If it doesn't, the
guide's rules are too vague to bite — sharpen section 6 first.

## Out of scope

Using the guide to write (T023). Any automated style scoring.
