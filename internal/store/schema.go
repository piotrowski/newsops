// Package store provides the SQLite-backed persistence layer for news-ops.
package store

// schemaV1 is the complete migration-1 DDL: tables, indexes, triggers, and
// virtual table.  Migrate() runs this verbatim on an empty database.
const schemaV1 = `
-- Articles as published.
CREATE TABLE items (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    url           TEXT NOT NULL UNIQUE,
    canonical_url TEXT NOT NULL,
    title         TEXT NOT NULL,
    source        TEXT NOT NULL,
    tier          INTEGER NOT NULL,
    published_at  DATETIME,
    first_seen    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    body_hash     TEXT NOT NULL,
    body_excerpt  TEXT,
    raw_path      TEXT,
    comments_url  TEXT,
    story_id      INTEGER REFERENCES stories(id)
);

CREATE INDEX idx_items_first_seen    ON items(first_seen);
CREATE INDEX idx_items_canonical_url ON items(canonical_url);
CREATE INDEX idx_items_story         ON items(story_id);

-- Story clusters (one canonical item per story).
CREATE TABLE stories (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    canonical_item_id INTEGER NOT NULL REFERENCES items(id),
    source_count      INTEGER NOT NULL DEFAULT 1,
    method            TEXT NOT NULL,
    first_seen        DATETIME NOT NULL,
    last_seen         DATETIME NOT NULL,
    shown_at          DATETIME
);

CREATE INDEX idx_stories_last_seen ON stories(last_seen);
CREATE INDEX idx_stories_shown     ON stories(shown_at);

-- All sources that contributed to a story.
CREATE TABLE story_sources (
    story_id INTEGER NOT NULL REFERENCES stories(id),
    source   TEXT NOT NULL,
    url      TEXT NOT NULL,
    kind     TEXT NOT NULL CHECK(kind IN ('article','discussion')),
    PRIMARY KEY(story_id, source, url)
);

-- Append-only decision log.
CREATE TABLE decisions (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    story_id INTEGER NOT NULL REFERENCES stories(id),
    action   TEXT NOT NULL CHECK(action IN ('shown','briefed','drafted','posted','dismissed')),
    note     TEXT,
    at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_decisions_story ON decisions(story_id, at);

-- Run bookkeeping.
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

-- HTTP conditional-request state per source.
CREATE TABLE feed_state (
    source        TEXT PRIMARY KEY,
    url           TEXT NOT NULL,
    etag          TEXT,
    last_modified TEXT,
    last_fetched  DATETIME,
    last_status   INTEGER,
    last_error    TEXT
);

-- Migration tracker.
CREATE TABLE schema_version (
    version    INTEGER PRIMARY KEY,
    applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Full-text search via external-content FTS5 table.
CREATE VIRTUAL TABLE items_fts USING fts5(
    title, body_excerpt, content='items', content_rowid='id'
);

-- Triggers to keep the FTS index in sync with items.
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
`

// schemaV2 normalizes timestamps written by earlier builds in RFC3339 form
// ("2026-09-22T17:36:27Z") to TimeLayout ("2026-09-22 17:36:27"), so that
// comparisons against CURRENT_TIMESTAMP-written columns are chronological.
const schemaV2 = `
UPDATE items   SET published_at = strftime('%Y-%m-%d %H:%M:%S', published_at) WHERE published_at LIKE '%T%';
UPDATE items   SET first_seen   = strftime('%Y-%m-%d %H:%M:%S', first_seen)   WHERE first_seen   LIKE '%T%';
UPDATE stories SET first_seen   = strftime('%Y-%m-%d %H:%M:%S', first_seen)   WHERE first_seen   LIKE '%T%';
UPDATE stories SET last_seen    = strftime('%Y-%m-%d %H:%M:%S', last_seen)    WHERE last_seen    LIKE '%T%';
UPDATE stories SET shown_at     = strftime('%Y-%m-%d %H:%M:%S', shown_at)     WHERE shown_at     LIKE '%T%';
UPDATE runs    SET started_at   = strftime('%Y-%m-%d %H:%M:%S', started_at)   WHERE started_at   LIKE '%T%';
UPDATE runs    SET ended_at     = strftime('%Y-%m-%d %H:%M:%S', ended_at)     WHERE ended_at     LIKE '%T%';
UPDATE decisions SET at         = strftime('%Y-%m-%d %H:%M:%S', at)           WHERE at           LIKE '%T%';
UPDATE feed_state SET last_fetched = strftime('%Y-%m-%d %H:%M:%S', last_fetched) WHERE last_fetched LIKE '%T%';
`
