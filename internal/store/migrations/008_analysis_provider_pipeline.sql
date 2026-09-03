-- Migration 008: configurable analysis provider pipeline. Additive and
-- deterministic; keeps every legacy settings/run/turn/cache column and table
-- for history and downgrade diagnosis.

-- Pipeline profiles and bindings ---------------------------------------------
CREATE TABLE analysis_pipeline_profile (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL COLLATE NOCASE UNIQUE,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE analysis_pipeline_binding (
    profile_id   TEXT NOT NULL REFERENCES analysis_pipeline_profile(id) ON DELETE CASCADE,
    stage_id     TEXT NOT NULL,
    provider_id  TEXT NOT NULL,
    model_id     TEXT NOT NULL,
    options_json TEXT NOT NULL,
    options_hash TEXT NOT NULL,
    PRIMARY KEY (profile_id, stage_id)
);

CREATE TABLE analysis_pipeline_settings (
    id                INTEGER PRIMARY KEY CHECK (id = 1),
    active_profile_id TEXT NOT NULL REFERENCES analysis_pipeline_profile(id) ON DELETE RESTRICT,
    updated_at        TEXT NOT NULL
);

-- Stage caches ----------------------------------------------------------------
CREATE TABLE analysis_stage_cache (
    id                     TEXT PRIMARY KEY,
    stage_id               TEXT NOT NULL,
    input_hash             TEXT NOT NULL,
    upstream_artifact_hash TEXT NOT NULL DEFAULT '',
    contract_version       TEXT NOT NULL,
    prompt_version         TEXT NOT NULL,
    provider_id            TEXT NOT NULL,
    provider_type          TEXT NOT NULL DEFAULT '',
    provider_config_fingerprint TEXT NOT NULL DEFAULT '',
    model_id               TEXT NOT NULL,
    options_hash           TEXT NOT NULL,
    validated_artifact_json TEXT NOT NULL,
    artifact_hash          TEXT NOT NULL,
    source_run_id          TEXT NOT NULL DEFAULT '',
    created_at             TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE UNIQUE INDEX idx_stage_cache_identity
    ON analysis_stage_cache(stage_id, input_hash, upstream_artifact_hash,
        contract_version, prompt_version, provider_id, model_id, options_hash);

-- Stage attempts and turns ----------------------------------------------------
CREATE TABLE analysis_stage_attempt (
    id                        TEXT PRIMARY KEY,
    run_id                    TEXT NOT NULL REFERENCES analysis_run(id) ON DELETE CASCADE,
    block_index               INTEGER NOT NULL CHECK (block_index >= 0),
    stage_id                  TEXT NOT NULL,
    status                    TEXT NOT NULL CHECK (status IN ('running', 'succeeded', 'failed')),
    provider_id               TEXT NOT NULL,
    provider_type             TEXT NOT NULL DEFAULT '',
    provider_config_fingerprint TEXT NOT NULL DEFAULT '',
    model_id                  TEXT NOT NULL,
    options_json              TEXT NOT NULL DEFAULT '{}',
    options_hash              TEXT NOT NULL DEFAULT '',
    contract_version          TEXT NOT NULL,
    prompt_version            TEXT NOT NULL,
    input_hash                TEXT NOT NULL DEFAULT '',
    upstream_artifact_hash    TEXT NOT NULL DEFAULT '',
    cache_disposition         TEXT NOT NULL DEFAULT 'miss' CHECK (cache_disposition IN ('hit', 'miss', 'bypassed')),
    source_cache_id           TEXT NOT NULL DEFAULT '',
    requested_model           TEXT NOT NULL DEFAULT '',
    reported_model            TEXT NOT NULL DEFAULT '',
    request_id                TEXT NOT NULL DEFAULT '',
    finish_reason             TEXT NOT NULL DEFAULT '',
    usage_json                TEXT NOT NULL DEFAULT '',
    timing_json               TEXT NOT NULL DEFAULT '',
    metadata_json             TEXT NOT NULL DEFAULT '',
    provider_stderr_excerpt   TEXT NOT NULL DEFAULT '',
    error_code                TEXT NOT NULL DEFAULT '',
    error_detail              TEXT NOT NULL DEFAULT '',
    started_at                TEXT NOT NULL,
    completed_at              TEXT NOT NULL DEFAULT '',
    duration_ms               INTEGER NOT NULL DEFAULT 0 CHECK (duration_ms >= 0),
    UNIQUE (run_id, block_index, stage_id)
);

CREATE INDEX idx_stage_attempt_run ON analysis_stage_attempt(run_id, block_index, stage_id);

CREATE TABLE analysis_stage_turn (
    id                 TEXT PRIMARY KEY,
    stage_attempt_id   TEXT NOT NULL REFERENCES analysis_stage_attempt(id) ON DELETE CASCADE,
    turn_index         INTEGER NOT NULL CHECK (turn_index >= 0),
    turn_kind          TEXT NOT NULL CHECK (turn_kind IN ('initial', 'corrective')),
    prompt             TEXT NOT NULL,
    output_schema      TEXT NOT NULL,
    completed_response TEXT NOT NULL DEFAULT '',
    response_hash      TEXT NOT NULL DEFAULT '',
    validation_error   TEXT NOT NULL DEFAULT '',
    provider_error     TEXT NOT NULL DEFAULT '',
    completion_metadata_json TEXT NOT NULL DEFAULT '{}',
    provider_stderr_excerpt TEXT NOT NULL DEFAULT '',
    started_at         TEXT NOT NULL,
    completed_at       TEXT NOT NULL DEFAULT '',
    duration_ms        INTEGER NOT NULL DEFAULT 0 CHECK (duration_ms >= 0),
    status             TEXT NOT NULL CHECK (status IN ('running', 'completed', 'failed')),
    validation_truncated        INTEGER NOT NULL DEFAULT 0 CHECK (validation_truncated IN (0, 1)),
    provider_error_truncated    INTEGER NOT NULL DEFAULT 0 CHECK (provider_error_truncated IN (0, 1)),
    metadata_truncated          INTEGER NOT NULL DEFAULT 0 CHECK (metadata_truncated IN (0, 1)),
    stderr_truncated            INTEGER NOT NULL DEFAULT 0 CHECK (stderr_truncated IN (0, 1)),
    UNIQUE (stage_attempt_id, turn_index)
);

CREATE INDEX idx_stage_turn_attempt ON analysis_stage_turn(stage_attempt_id, turn_index);

-- Provenance columns ----------------------------------------------------------
ALTER TABLE analysis_run ADD COLUMN pipeline_version TEXT NOT NULL DEFAULT '';
ALTER TABLE analysis_run ADD COLUMN profile_id TEXT NOT NULL DEFAULT '';
ALTER TABLE analysis_run ADD COLUMN profile_name TEXT NOT NULL DEFAULT '';
ALTER TABLE analysis_run ADD COLUMN profile_snapshot_json TEXT NOT NULL DEFAULT '';
ALTER TABLE analysis_run ADD COLUMN profile_snapshot_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE analysis_run ADD COLUMN failed_stage_id TEXT NOT NULL DEFAULT '';
ALTER TABLE analysis_run ADD COLUMN failed_provider_id TEXT NOT NULL DEFAULT '';

ALTER TABLE article ADD COLUMN analysis_profile_id TEXT NOT NULL DEFAULT '';
ALTER TABLE article ADD COLUMN analysis_profile_name TEXT NOT NULL DEFAULT '';
ALTER TABLE article ADD COLUMN analysis_pipeline_snapshot_json TEXT NOT NULL DEFAULT '';
ALTER TABLE article ADD COLUMN analysis_pipeline_snapshot_hash TEXT NOT NULL DEFAULT '';

ALTER TABLE article_block ADD COLUMN published_analysis_profile_id TEXT NOT NULL DEFAULT '';
ALTER TABLE article_block ADD COLUMN published_analysis_profile_name TEXT NOT NULL DEFAULT '';
ALTER TABLE article_block ADD COLUMN published_analysis_snapshot_hash TEXT NOT NULL DEFAULT '';

ALTER TABLE semantic_sense ADD COLUMN translation_provider_id TEXT NOT NULL DEFAULT '';
ALTER TABLE semantic_sense ADD COLUMN translation_provider_model TEXT NOT NULL DEFAULT '';

-- Old-format analysis jobs are incompatible with the pipeline payload.
-- Cancel them deterministically (no provider call) and move their affected
-- articles and first unresolved blocks to the recoverable failed state,
-- exactly as migration 007 did for the v3 cutover. Accepted paragraphs stay
-- published. No replacement work is enqueued.
UPDATE job SET
    state = 'canceled',
    error_code = 'v1.analysis_pipeline_upgraded',
    lease_owner = '',
    lease_token_hash = '',
    lease_expires_at = '',
    completed_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE job_type = 'reader.analysis.v2'
  AND state IN ('queued', 'leased', 'running');

UPDATE article SET
    analysis_status = 'failed',
    analysis_error_code = 'v1.analysis_pipeline_upgraded',
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE analysis_status IN ('queued', 'processing');

UPDATE article_block AS block SET
    analysis_status = 'failed',
    analysis_error_code = 'v1.analysis_pipeline_upgraded'
WHERE block.analysis_status = 'pending'
  AND EXISTS (
    SELECT 1 FROM article AS article_row
    WHERE article_row.id = block.article_id
      AND article_row.analysis_status = 'failed'
      AND article_row.analysis_error_code = 'v1.analysis_pipeline_upgraded'
  )
  AND block.block_index = (
    SELECT MIN(pending.block_index)
    FROM article_block AS pending
    WHERE pending.article_id = block.article_id
      AND pending.analysis_status = 'pending'
  );
