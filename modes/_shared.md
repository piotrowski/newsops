# Shared rules

Before answering anything about current news:
1. Run `newsops status --json`.
2. If fetch is stale (>6h), run `newsops collect --max-age 6h --json`.

Never re-fetch a feed by hand; the CLI owns collection.
Never reproduce source paragraphs. Everything you write is written fresh.
Never publish anywhere. Drafts go to disk; the human posts them.
Prefer the primary source over coverage of it.
Record what happens with `newsops decide`.
Stop at each human gate. Do not brief unasked, and do not draft unasked.
When querying the database for story or item data, always use `newsops show <id>` via CLI.
Never run raw SQL (sqlite3) or access `data/news.db` directly — the CLI is the only correct interface.
