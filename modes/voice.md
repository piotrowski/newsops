# Voice Mode — Corpus + Guide Generation

## Procedure

1. Read every file in `context/voice/` (README.md, EXAMPLE.md, and all post samples).
2. Analyze the samples for patterns in: shape/length, openings, rhythm,
   vocabulary/tics, structural moves, and constructions absent from every sample.
3. Write `context/voice-guide.md` with these fixed sections:

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

## Hard Rules

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
