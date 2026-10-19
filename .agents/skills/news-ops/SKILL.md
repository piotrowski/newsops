---
name: news-ops
description: >-
  Personal news pipeline -- collect and skim a daily digest, research a story
  into a brief, draft a LinkedIn or blog post. Use when the user asks what is
  in the news, asks to brief or research a story ID, asks for a draft, or runs
  any news-ops mode.
arguments: mode
user_invocable: true
argument-hint: "[collect | brief <id> | draft <id> --linkedin|--blog | voice | status | posted <id> [url]]"
---

## Project root resolution

Start at this file's directory and walk upward to the nearest directory containing both `AGENTS.md` and `modes/`. That is `PROJECT_ROOT`. Resolve all paths (`modes/`, `config/`, `context/`, `data/`, `briefs/`, `posts/`, `digest.md`) against it, never against the process working directory. If the two sentinels are not found, stop and say so.

## Binary resolution

Prefer `newsops` on `PATH`; fall back to `$PROJECT_ROOT/newsops`. If neither exists, stop and point at `make install`. Never `go run` it -- a compile on every call is a silent 30-second tax.

## Mode routing table

| Input | Mode |
|---|---|
| (empty) | discovery: list the rows of this table with one clause each, run `newsops status --json`, report it, stop. Touch nothing else: no fetch, no `digest.md`, no `decide`. |
| `collect`, `digest`, `news` | `modes/collect.md` |
| `brief <id>` | `modes/brief.md` |
| `draft <id> [--linkedin|--blog]` | `modes/draft.md` |
| `voice` | `modes/voice.md` |
| `status` | run `newsops status --json`, report, stop |
| `posted <id> [url]` | run `newsops decide <id> posted --note "<url or empty>" --json`, confirm, stop. The human publishes by hand; this only records that they did. |

## Related skills

- Running or reading any `newsops` command directly: `newsops-cli`.
- A feed failing, a stage stale, a `locked` error, or editing `config/`: `newsops-sources`.
- Changing the Go code: `newsops-dev`.

## Procedure

1. Always read `modes/_shared.md` first, then the routed mode file, then follow it exactly.
2. The gates. Restate both human gates verbatim. A router that forgets them routes straight past them.

### Gate 1 (after digest)

```
── HUMAN GATE: which are worth researching? ──
```

Do not brief anything unasked. Stop and ask the user.

### Gate 2 (after brief)

```
── HUMAN GATE: which is worth writing about? ──
```

Do not draft unasked. Stop and ask the user.

## Database access

When querying the database for story or item data, always use `newsops show <id>` via CLI.
Never run raw SQL (sqlite3) or access `data/news.db` directly — the CLI is the only correct interface.
