# Draft Mode — LinkedIn and Blog

## Procedure

1. Read `modes/_shared.md`, `context/voice-guide.md`, and every file in
   `context/voice/` matching the target platform.
2. Read the brief. If it is missing or thin, stop and say so — do not
   re-research here; that is `modes/brief.md`.
3. Write to `posts/<YYYY-MM-DD>-<slug>-<target>.md`, reusing the brief's slug.
4. `newsops decide <story_id> drafted`.
5. Report the draft in the conversation and say plainly that it is a starting
   point.

### --linkedin

- 150–250 words. One idea. If there are two, pick one.
- Opens with a concrete detail or a position — **not** a question, not "I've
  been thinking about…", not a statistic without a source.
- Contains at least one specific fact from the primary source: a number, a
  version, a named subsystem.
- Ends with a position, not a solicitation. No "thoughts?", no "what's your
  experience?".
- Plain text. No markdown — LinkedIn strips it and the asterisks survive as
  asterisks. Line breaks only.
- At most 3 hashtags, and only if the voice corpus shows you use them at all.
- **Link goes in a separate block** at the end of the file, marked
  `--- first comment ---`, because LinkedIn is widely reported to suppress
  reach on posts with external links. Test that against your own numbers;
  platform behaviour moves.

### --blog

- 800–1200 words, subheads, links inline.
- Structure: what happened → why it matters → what most people are missing →
  what you'd watch next. Follow the voice guide's structural moves where they
  conflict with this default.
- Technical detail is welcome; this is the format where it belongs.

## Rules for Both

- Follow `voice-guide.md` section 6 ("never does") strictly — that section
  exists to catch exactly this output.
- No em-dash-heavy cadence, no tricolons, no "it's not just X, it's Y", no
  opening with a rhetorical question. These read as machine-written regardless
  of what the voice guide says.
- Never state a fact absent from the brief. If the draft needs something more,
  say what is missing rather than inventing it.
- Never reproduce source sentences.
- If `voice-guide.md` does not exist, stop and point at `modes/voice.md`.
  Drafting without it produces generic output that is harder to fix than a
  blank page.
