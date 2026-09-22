# Voice Corpus

This directory holds samples of your own posts — the tone transfer from
examples beats any instruction about tone.

## Format

Each file is one post, exactly as published:

```markdown
---
platform: linkedin   # or: blog
date: 2026-05-14
note: did well, ~40 comments   # optional
---

The post text, exactly as published.
```

## Guidelines

- **8–12 samples** total. One per file.
- Pick your most **characteristic** posts, not your most popular.
- Include both `linkedin` and `blog` platforms when you have them.
- The real test: a draft produced with this guide should need less rewriting
  than one produced without it.

## Regenerating the Guide

Run `modes/voice.md` to regenerate `context/voice-guide.md`. Human edits
survive regeneration only if copied out first, so nobody loses their
corrections.
