# T023 — `modes/draft.md` — LinkedIn and blog

**Depends on:** T022, T020
**Size:** M
**Touches:** `modes/draft.md`

## Why

The blank page is the real cost of posting regularly, and this removes it.

It is also the step every earlier version of this plan refused to build, for a
reason that has not gone away: **model-written posts are recognizable, and
publishing one unedited costs more credibility than the reach it earns.** The
guards are structural — this mode runs only on a story the human explicitly
picked, from a brief the human read — and the expectation, stated in the mode
file itself, is that the draft gets rewritten. No code can enforce that, so
the file says it out loud.

## Deliverable

`modes/draft.md`, taking a brief path and a target.

**Procedure**

1. Read `modes/_shared.md`, `context/voice-guide.md`, and every file in
   `context/voice/` matching the target platform.
2. Read the brief. If it is missing or thin, stop and say so — do not
   re-research here; that is `modes/brief.md`.
3. Write to `posts/<YYYY-MM-DD>-<slug>-<target>.md`, reusing the brief's slug.
4. `newsops decide <story_id> drafted`.
5. Report the draft in the conversation and say plainly that it is a starting
   point.

### `--linkedin`

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

### `--blog`

- 800–1200 words, subheads, links inline.
- Structure: what happened → why it matters → what most people are missing →
  what you'd watch next. Follow the voice guide's structural moves where they
  conflict with this default.
- Technical detail is welcome; this is the format where it belongs.

**Rules for both**

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

## Acceptance criteria

- [ ] `--linkedin` produces 150–250 words, plain text, no markdown syntax.
- [ ] The link appears only in the `--- first comment ---` block.
- [ ] The opening line is not a question.
- [ ] At least one concrete fact traceable to the brief's primary source.
- [ ] `--blog` produces 800–1200 words with subheads and inline links.
- [ ] Both targets can be generated from one brief, producing two files.
- [ ] A `decisions` row with action `drafted` is written.
- [ ] With no `context/voice-guide.md`, the mode refuses and points at T022.
- [ ] With a thin or missing brief, it refuses rather than researching.
- [ ] No sentence appears verbatim in both the draft and any source.
- [ ] The mode's own output states that the draft is a starting point.

## Tests

Manual, and the only honest test is this: **would you post it after editing?**
Not before. Run it on three briefs and count how much you rewrite. If you
rewrite everything, the voice guide's rules are too vague — fix T022's
section 6, not this file.

## Out of scope

Publishing anywhere. There is no `publish` command and no API credentials in
this repo; the pipeline stops at a file on disk.
