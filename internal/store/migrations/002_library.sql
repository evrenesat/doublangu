-- Migration 002: Library metadata schema.
-- All record identities are canonical uppercase 26-character ULIDs.
-- Chapter and source-asset timing uses integer milliseconds.
-- Foreign keys cascade: deleting a library removes all descendant records.

CREATE TABLE library (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    source_language TEXT NOT NULL,
    target_language TEXT NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    created_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE work (
    id         TEXT PRIMARY KEY,
    library_id TEXT NOT NULL REFERENCES library(id) ON DELETE CASCADE,
    title      TEXT NOT NULL,
    author     TEXT NOT NULL DEFAULT '',
    kind       TEXT NOT NULL DEFAULT '',
    source_url TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX idx_work_library ON work(library_id);

CREATE TABLE edition (
    id         TEXT PRIMARY KEY,
    work_id    TEXT NOT NULL REFERENCES work(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    language   TEXT NOT NULL,
    format     TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX idx_edition_work ON edition(work_id);

CREATE TABLE chapter (
    id           TEXT PRIMARY KEY,
    edition_id   TEXT NOT NULL REFERENCES edition(id) ON DELETE CASCADE,
    title        TEXT NOT NULL DEFAULT '',
    chapter_num  INTEGER NOT NULL DEFAULT 0,
    start_ms     INTEGER NOT NULL CHECK (start_ms >= 0),
    end_ms       INTEGER NOT NULL CHECK (end_ms >= 0),
    duration_ms  INTEGER NOT NULL CHECK (duration_ms >= 0),
    created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    CHECK (end_ms >= start_ms)
);

CREATE INDEX idx_chapter_edition ON chapter(edition_id);

CREATE TABLE source_asset (
    id          TEXT PRIMARY KEY,
    chapter_id  TEXT NOT NULL REFERENCES chapter(id) ON DELETE CASCADE,
    url         TEXT NOT NULL,
    mime_type   TEXT NOT NULL DEFAULT '',
    size_bytes  INTEGER NOT NULL DEFAULT 0,
    sha256_hash TEXT NOT NULL DEFAULT '',
    start_ms    INTEGER NOT NULL CHECK (start_ms >= 0),
    end_ms      INTEGER NOT NULL CHECK (end_ms >= 0),
    duration_ms INTEGER NOT NULL CHECK (duration_ms >= 0),
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    CHECK (end_ms >= start_ms)
);

CREATE INDEX idx_source_asset_chapter ON source_asset(chapter_id);
