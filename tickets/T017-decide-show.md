# T017 — `decide` + `show` subcommands

**Depends on:** T012
**Size:** S
**Touches:** `cmd/newsops/decide.go`, `cmd/newsops/show.go`

## Why

`decisions` is the feedback record — the only honest account of whether this
system is working. A story shown on Monday, briefed on Tuesday, drafted on
Wednesday: that sequence is what the two-week review (T024) reads, and it is
the table people skip because nothing breaks immediately when they do.

`shown` doubles as the repeat-suppression mechanism: it sets
`stories.shown_at`, which is what keeps yesterday's digest out of today's
(T014).

## `decide`

```
newsops decide <story_ids> <action> [--json] [--note "..."]
```

`<story_ids>` is one id or a comma-separated list — the agent marks all ~40
candidates shown in one call, and forty subprocess invocations in a
conversation is a bad time for everyone.

`<action>` is one of `shown` · `briefed` · `drafted` · `posted` · `dismissed`.

Always appends a `decisions` row (the log is append-only; a story briefed
twice is two rows, and that is meaningful). Additionally, `shown` sets
`stories.shown_at` **only if currently null** — first time wins, so re-running
a digest never moves the mark and never resurfaces the story.

`--note` is free text. The digest mode (T019) uses it to record the rank it
gave each story, as `rank=3`, which is what lets T024 ask whether the agent's
ordering predicted your picks.

```json
{
  "action": "shown",
  "applied": [4213, 4198, 4155],
  "already_shown": [4155],
  "unknown": []
}
```

Exit 0 when every id applied. Exit 2 when any id is unknown or the action is
invalid — with the valid actions listed in the message — and in that case
**apply nothing**. A partial write here would leave the agent unsure what
state it is in.

## `show`

```
newsops show <story_id> [--json]
```

Everything about one story: the story row, all member items, all
`story_sources`, and the full decision history in time order. This is what
`modes/brief.md` opens with.

```json
{
  "story": {
    "id": 4213, "source_count": 4, "method": "url", "primary": true,
    "first_seen": "...", "last_seen": "...", "shown_at": "..."
  },
  "canonical": {
    "item_id": 88213, "title": "...", "url": "...", "source": "Cloudflare",
    "tier": 1, "published_at": "...", "excerpt": "..."
  },
  "items": [
    {"item_id": 88213, "title": "...", "url": "...", "source": "Cloudflare",
     "tier": 1, "published_at": "...", "excerpt": "..."}
  ],
  "sources": [
    {"source": "Hacker News", "url": "https://news.ycombinator.com/item?id=...",
     "kind": "discussion"}
  ],
  "decisions": [
    {"action": "shown", "note": "rank=1", "at": "2026-08-30T07:20:00Z"}
  ]
}
```

`decide` opens `ReadWrite`; `show` opens `ReadOnly` with no lock.

## Acceptance criteria

- [ ] `decide 1,2,3 shown` writes three rows and sets `shown_at` on all three.
- [ ] Repeating it writes three more rows but leaves `shown_at` unchanged,
      reporting the ids in `already_shown`.
- [ ] `decide 1 nonsense` exits 2, lists the five valid actions, writes nothing.
- [ ] `decide 1,99999 shown` with 99999 unknown exits 2 and writes **nothing**
      — including for story 1.
- [ ] `--note "rank=3"` round-trips through `show`.
- [ ] `show` on a story with 4 items, 2 extra sources, and 3 decisions returns
      all of them, decisions in ascending time order.
- [ ] `show 99999` exits 2 with `{"error": "unknown_story"}`.
- [ ] `show` works while a write lock is held.

## Tests

Seeded in-memory database, one test per acceptance line. The all-or-nothing
behaviour on a partially-unknown id list needs its own test — it is the
non-obvious requirement here.

## Out of scope

When to call these — the mode files decide (T019, T020, T023).
