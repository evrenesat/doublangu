-- Migration 006: reliable paragraph analysis, provenance, diagnostics, and
-- configuration-aware validated caches.

ALTER TABLE article ADD COLUMN analysis_model TEXT NOT NULL DEFAULT '';
ALTER TABLE article ADD COLUMN analysis_effort TEXT NOT NULL DEFAULT '';

CREATE TABLE analysis_settings (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    model      TEXT NOT NULL DEFAULT '',
    effort     TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL
);

CREATE TABLE analysis_run (
    id                   TEXT PRIMARY KEY,
    article_id           TEXT NOT NULL REFERENCES article(id) ON DELETE CASCADE,
    job_id               TEXT NOT NULL,
    attempt_count        INTEGER NOT NULL CHECK (attempt_count > 0),
    content_hash         TEXT NOT NULL,
    contract_version     TEXT NOT NULL,
    prompt_version       TEXT NOT NULL,
    requested_model      TEXT NOT NULL,
    requested_effort     TEXT NOT NULL,
    provider_id          TEXT NOT NULL,
    codex_cli_version    TEXT NOT NULL DEFAULT '',
    reported_model      TEXT NOT NULL DEFAULT '',
    started_at           TEXT NOT NULL,
    completed_at         TEXT NOT NULL DEFAULT '',
    duration_ms          INTEGER NOT NULL DEFAULT 0 CHECK (duration_ms >= 0),
    status               TEXT NOT NULL CHECK (status IN ('running', 'succeeded', 'failed')),
    total_paragraphs     INTEGER NOT NULL DEFAULT 0 CHECK (total_paragraphs >= 0),
    completed_paragraphs INTEGER NOT NULL DEFAULT 0 CHECK (completed_paragraphs >= 0),
    failed_block_index   INTEGER NOT NULL DEFAULT -1 CHECK (failed_block_index >= -1),
    error_code           TEXT NOT NULL DEFAULT '',
    error_detail         TEXT NOT NULL DEFAULT '',
    stderr_excerpt       TEXT NOT NULL DEFAULT ''
);

CREATE INDEX idx_analysis_run_article_started
    ON analysis_run(article_id, started_at DESC, id DESC);

CREATE TABLE analysis_turn (
    id                         TEXT PRIMARY KEY,
    run_id                     TEXT NOT NULL REFERENCES analysis_run(id) ON DELETE CASCADE,
    block_index                INTEGER NOT NULL CHECK (block_index >= 0),
    turn_index                 INTEGER NOT NULL CHECK (turn_index >= 0),
    turn_kind                  TEXT NOT NULL CHECK (turn_kind IN ('initial', 'corrective')),
    prompt                     TEXT NOT NULL,
    output_schema              TEXT NOT NULL,
    completed_response         TEXT NOT NULL DEFAULT '',
    response_hash              TEXT NOT NULL DEFAULT '',
    validation_error           TEXT NOT NULL DEFAULT '',
    provider_error             TEXT NOT NULL DEFAULT '',
    completion_metadata_json   TEXT NOT NULL DEFAULT '{}',
    provider_stderr_excerpt    TEXT NOT NULL DEFAULT '',
    started_at                 TEXT NOT NULL,
    completed_at               TEXT NOT NULL DEFAULT '',
    duration_ms                INTEGER NOT NULL DEFAULT 0 CHECK (duration_ms >= 0),
    status                     TEXT NOT NULL CHECK (status IN ('running', 'completed', 'failed')),
    UNIQUE (run_id, block_index, turn_index)
);

CREATE INDEX idx_analysis_turn_run_order
    ON analysis_turn(run_id, block_index, turn_index);

CREATE TABLE analysis_chunk_cache (
    id                       TEXT PRIMARY KEY,
    source_language          TEXT NOT NULL,
    target_language          TEXT NOT NULL,
    content_hash             TEXT NOT NULL,
    block_index              INTEGER NOT NULL CHECK (block_index >= 0),
    block_hash               TEXT NOT NULL,
    carry_hash               TEXT NOT NULL,
    chunk_input_hash         TEXT NOT NULL,
    contract_version         TEXT NOT NULL,
    prompt_version           TEXT NOT NULL,
    provider_id              TEXT NOT NULL,
    provider_model           TEXT NOT NULL,
    provider_effort          TEXT NOT NULL,
    validated_response_json  TEXT NOT NULL,
    response_hash             TEXT NOT NULL,
    source_run_id            TEXT NOT NULL DEFAULT '',
    created_at               TEXT NOT NULL
);

CREATE UNIQUE INDEX idx_analysis_chunk_cache_identity
    ON analysis_chunk_cache(chunk_input_hash, contract_version, prompt_version, provider_model, provider_effort);

-- analysis_cache predates prepared-input, effort, and configuration-aware
-- identities. Rebuild it transactionally so legacy rows remain inspectable but
-- cannot satisfy a new exact cache lookup.
ALTER TABLE analysis_cache RENAME TO analysis_cache_legacy;

CREATE TABLE analysis_cache (
    id                      TEXT PRIMARY KEY,
    content_hash            TEXT NOT NULL,
    source_language         TEXT NOT NULL,
    target_language         TEXT NOT NULL,
    contract_version        TEXT NOT NULL,
    provider_id             TEXT NOT NULL,
    provider_model          TEXT NOT NULL DEFAULT '',
    provider_effort         TEXT NOT NULL DEFAULT '',
    prompt_version          TEXT NOT NULL,
    prepared_input_hash     TEXT NOT NULL DEFAULT '',
    validated_response_json TEXT NOT NULL,
    response_hash           TEXT NOT NULL,
    created_at              TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- Legacy rows intentionally retain an empty prepared hash. Excluding those
-- rows from the new identity index preserves every old content/language row
-- even when several used the same provider model; all new writes have a
-- non-empty prepared hash and therefore use the exact identity below.
CREATE UNIQUE INDEX idx_analysis_cache_prepared_identity
    ON analysis_cache(prepared_input_hash, contract_version, prompt_version, provider_model, provider_effort)
    WHERE prepared_input_hash <> '';

INSERT INTO analysis_cache (
    id, content_hash, source_language, target_language, contract_version,
    provider_id, provider_model, provider_effort, prompt_version,
    prepared_input_hash, validated_response_json, response_hash, created_at
)
SELECT id, content_hash, source_language, target_language, contract_version,
       provider_id, provider_model, '', prompt_version, '',
       validated_response_json, response_hash, created_at
FROM analysis_cache_legacy;

DROP TABLE analysis_cache_legacy;
