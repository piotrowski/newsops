# T002 — SQLite schema + migrations

**Depends on:** T001
**Size:** L
**Touches:** `internal/store/schema.go`, `internal/store/migrate.go`

## Why

Everything else reads or writes this. Two properties matter more than the
column list: **story IDs must be permanent** (briefs and your recorded
decisions reference them, and a silent renumber is unrecoverable), and the
schema must be **migratable** — the FTS index and `feed_state` will both want
changes later, and hand-editing a real archive is miserable.

## Deliverables

Full schema, applied through an ordered migration list.

```sql
CREATE TABLE items (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    url           TEXT NOT NULL UNIQUE,        -- as published
    canonical_url TEXT NOT NULL,               -- normalized (T010)
    title         TEXT NOT NULL,
    source        TEXT NOT NULL,               -- name from sources.yml
    tier          INTEGER NOT NULL,
    published_at  DATETIME,                    -- display only; often null/wrong
    first_seen    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    body_hash     TEXT NOT NULL,
    body_excerpt  TEXT,                        -- ~1500 chars, what the agent skims
    raw_path      TEXT,
    story_id      INTEGER REFERENCES stories(id)
);
CREATE INDEX idx_items_first_seen    ON items(first_seen);
CREATE INDEX idx_items_canonical_url ON items(canonical_url);
CREATE INDEX idx_items_story         ON items(story_id);

CREATE TABLE stories (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    canonical_item_id INTEGER NOT NULL REFERENCES items(id),
    source_count      INTEGER NOT NULL DEFAULT 1,
    method            TEXT NOT NULL,           -- url|title
    first_seen        DATETIME NOT NULL,
    last_seen         DATETIME NOT NULL,
    shown_at          DATETIME                 -- set once it appears in a digest
);
CREATE INDEX idx_stories_last_seen ON stories(last_seen);
CREATE INDEX idx_stories_shown     ON stories(shown_at);

CREATE TABLE story_sources (
    story_id INTEGER NOT NULL REFERENCES stories(id),
    source   TEXT NOT NULL,
    url      TEXT NOT NULL,
    kind     TEXT NOT NULL CHECK(kind IN ('article','discussion')),
    PRIMARY KEY(story_id, url)
);

CREATE TABLE decisions (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    story_id INTEGER NOT NULL REFERENCES stories(id),
    action   TEXT NOT NULL CHECK(action IN ('shown','briefed','drafted','posted','dismissed')),
    note     TEXT,
    at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_decisions_story ON decisions(story_id, at);

CREATE TABLE runs (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    stage      TEXT NOT NULL CHECK(stage IN ('fetch','dedupe','candidates')),
    started_at DATETIME NOT NULL,
    ended_at   DATETIME,
    ok         BOOLEAN,
    items_in   INTEGER,
    items_out  INTEGER,
    note       TEXT
);
CREATE INDEX idx_runs_stage ON runs(stage, started_at);

-- HTTP conditional-request state, one row per source. Consumed by T007.
CREATE TABLE feed_state (
    source        TEXT PRIMARY KEY,            -- name from sources.yml
    url           TEXT NOT NULL,
    etag          TEXT,
    last_modified TEXT,
    last_fetched  DATETIME,
    last_status   INTEGER,
    last_error    TEXT
);

CREATE TABLE schema_version (
    version    INTEGER PRIMARY KEY,
    applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE VIRTUAL TABLE items_fts USING fts5(
    title, body_excerpt, content='items', content_rowid='id'
);
```

`items_fts` is an external-content table, so it does **not** stay in sync on
its own. Add the three triggers:

```sql
CREATE TRIGGER items_ai AFTER INSERT ON items BEGIN
    INSERT INTO items_fts(rowid, title, body_excerpt)
    VALUES (new.id, new.title, new.body_excerpt);
END;
CREATE TRIGGER items_ad AFTER DELETE ON items BEGIN
    INSERT INTO items_fts(items_fts, rowid, title, body_excerpt)
    VALUES ('delete', old.id, old.title, old.body_excerpt);
END;
CREATE TRIGGER items_au AFTER UPDATE ON items BEGIN
    INSERT INTO items_fts(items_fts, rowid, title, body_excerpt)
    VALUES ('delete', old.id, old.title, old.body_excerpt);
    INSERT INTO items_fts(rowid, title, body_excerpt)
    VALUES (new.id, new.title, new.body_excerpt);
END;
```

`decisions` is an append-only log, not one row per story: a story is shown on
Monday, briefed on Tuesday, drafted on Wednesday, and that sequence is the
only honest record of whether this system is working.

## Interface

```go
type migration struct {
    Version int
    SQL     string
}

var migrations = []migration{{1, schemaV1}}

// Migrate applies every migration above the recorded version, each inside its
// own transaction, and records it in schema_version.
func Migrate(ctx context.Context, db *sql.DB) error
```

Dependency: `modernc.org/sqlite` (pure Go, no cgo). Confirm FTS5 is available
in the build — it ships enabled, but assert it in a test rather than at
runtime in front of a user.

## Acceptance criteria

- [ ] `Migrate` on an empty database creates every table, index, trigger and
      virtual table, and records `schema_version = 1`.
- [ ] `Migrate` run a second time is a no-op and returns nil.
- [ ] A failed migration rolls back completely; `schema_version` is unchanged.
- [ ] Inserting an item makes it findable via
      `SELECT rowid FROM items_fts WHERE items_fts MATCH ?`.
- [ ] Updating an item's title updates what FTS returns; deleting removes it.
- [ ] `PRAGMA foreign_keys = ON` is set and a bad `story_id` is rejected.
- [ ] Inserting a duplicate `url` fails; inserting an invalid `decisions.action`
      or `story_sources.kind` fails.

## Tests

In-memory SQLite (`:memory:`) throughout:

- migration idempotence, and version recorded
- FTS sync on insert / update / delete
- each CHECK constraint rejects a bad value
- UNIQUE on `items.url`

## Out of scope

Opening the database, WAL, locking (T003). Any queries beyond what the tests
need.
