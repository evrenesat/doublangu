-- Migration 004: article reader annotations and per-learner state.
-- Source spans use browser-compatible UTF-16 code-unit offsets.

CREATE TABLE article (
    id                    TEXT PRIMARY KEY,
    title                 TEXT NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
    source_language       TEXT NOT NULL,
    target_language       TEXT NOT NULL,
    enrichment_status     TEXT NOT NULL CHECK (enrichment_status IN ('draft', 'processing', 'ready', 'failed')),
    enrichment_error_code TEXT NOT NULL DEFAULT '',
    created_at            TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at            TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX idx_article_created ON article(created_at DESC, id DESC);

CREATE TABLE article_block (
    id           TEXT PRIMARY KEY,
    article_id   TEXT NOT NULL REFERENCES article(id) ON DELETE CASCADE,
    block_index  INTEGER NOT NULL CHECK (block_index >= 0),
    kind         TEXT NOT NULL CHECK (kind IN ('paragraph')),
    source_text  TEXT NOT NULL CHECK (length(source_text) > 0),
    UNIQUE (article_id, block_index)
);

CREATE INDEX idx_article_block_article ON article_block(article_id, block_index);

CREATE TABLE article_annotation (
    id                  TEXT PRIMARY KEY,
    article_block_id    TEXT NOT NULL REFERENCES article_block(id) ON DELETE CASCADE,
    start_utf16         INTEGER NOT NULL CHECK (start_utf16 >= 0),
    end_utf16           INTEGER NOT NULL CHECK (end_utf16 > start_utf16),
    source_text         TEXT NOT NULL CHECK (length(source_text) > 0),
    kind                TEXT NOT NULL CHECK (kind IN ('word', 'phrase', 'idiom', 'expression', 'proverb')),
    learning_key        TEXT NOT NULL CHECK (length(learning_key) > 0),
    primary_translation TEXT NOT NULL CHECK (length(primary_translation) > 0),
    alternatives_json   TEXT NOT NULL DEFAULT '[]',
    literal_translation TEXT NOT NULL DEFAULT '',
    meaning_note        TEXT NOT NULL DEFAULT '',
    usage_note          TEXT NOT NULL DEFAULT '',
    parts_note          TEXT NOT NULL DEFAULT '',
    suggest_shadow      INTEGER NOT NULL CHECK (suggest_shadow IN (0, 1)),
    UNIQUE (article_block_id, start_utf16, end_utf16)
);

CREATE INDEX idx_article_annotation_block_start ON article_annotation(article_block_id, start_utf16);
CREATE INDEX idx_article_annotation_learning ON article_annotation(learning_key, kind);

CREATE TABLE learning_state (
    source_language TEXT NOT NULL,
    kind            TEXT NOT NULL CHECK (kind IN ('word', 'phrase', 'idiom', 'expression', 'proverb')),
    learning_key    TEXT NOT NULL CHECK (length(learning_key) > 0),
    status          TEXT NOT NULL CHECK (status IN ('learned', 'unlearned')),
    updated_at      TEXT NOT NULL,
    PRIMARY KEY (source_language, kind, learning_key)
);

CREATE INDEX idx_learning_state_source_key ON learning_state(source_language, learning_key);
