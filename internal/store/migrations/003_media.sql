-- Migration 003: Immutable content-addressed media store.
-- Blob digests are lowercase SHA-256 hex strings.
-- Blob references use explicit source-asset foreign keys with ON DELETE CASCADE
-- so removing a source_asset cleans up its blob reference, while ON DELETE
-- RESTRICT on blob prevents deleting blobs that still have references.

CREATE TABLE blob (
    digest     TEXT PRIMARY KEY,
    size_bytes INTEGER NOT NULL CHECK (size_bytes >= 0),
    mime_type  TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE blob_reference (
    id              TEXT PRIMARY KEY,
    source_asset_id TEXT NOT NULL REFERENCES source_asset(id) ON DELETE CASCADE,
    blob_digest     TEXT NOT NULL REFERENCES blob(digest) ON DELETE RESTRICT,
    created_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE UNIQUE INDEX idx_blob_reference_source_asset ON blob_reference(source_asset_id);
CREATE INDEX idx_blob_reference_digest ON blob_reference(blob_digest);
