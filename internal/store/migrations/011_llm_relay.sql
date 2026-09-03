-- Migration 011: Mac LLM relay (`llm.relay.v1`) durable work.
-- Additive except for the `job` table rebuild, which widens only the
-- `job_type` CHECK. `job_dependency` references `job` with cascading
-- foreign keys and migrations run with foreign keys enabled, so the
-- dependency rows are backed up, the child table is dropped first, and
-- every row is restored after the rebuild.

-- Dependency backup ----------------------------------------------------------
CREATE TABLE _job_dependency_backup_011 (
    job_id            TEXT NOT NULL,
    dependency_job_id TEXT NOT NULL,
    PRIMARY KEY (job_id, dependency_job_id)
);

INSERT INTO _job_dependency_backup_011 (job_id, dependency_job_id)
    SELECT job_id, dependency_job_id FROM job_dependency;

DROP TABLE job_dependency;

-- Job rebuild: byte-identical to migration 005 except the job_type CHECK, ---
-- which additionally admits `llm.relay.v1`. -------------------------------
CREATE TABLE job_new (
    id                 TEXT PRIMARY KEY,
    job_type           TEXT NOT NULL CHECK (job_type IN ('reader.analysis.v2', 'tts.avspeech.v1', 'tts.chatterbox.v3', 'llm.relay.v1')),
    execution_target   TEXT NOT NULL CHECK (execution_target IN ('server', 'macos')),
    owner_type         TEXT NOT NULL CHECK (length(owner_type) > 0),
    owner_id           TEXT NOT NULL CHECK (length(owner_id) > 0),
    idempotency_key    TEXT NOT NULL UNIQUE,
    input_hash         TEXT NOT NULL CHECK (length(input_hash) > 0),
    payload_json       TEXT NOT NULL CHECK (length(payload_json) > 0),
    state              TEXT NOT NULL CHECK (state IN ('queued', 'leased', 'running', 'succeeded', 'failed', 'canceled')),
    priority           INTEGER NOT NULL DEFAULT 0,
    attempt_count     INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0 AND attempt_count <= 3),
    max_attempts      INTEGER NOT NULL DEFAULT 3 CHECK (max_attempts BETWEEN 1 AND 3),
    available_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    lease_owner        TEXT NOT NULL DEFAULT '',
    lease_token_hash   TEXT NOT NULL DEFAULT '',
    lease_expires_at   TEXT NOT NULL DEFAULT '',
    progress_percent   INTEGER NOT NULL DEFAULT 0 CHECK (progress_percent BETWEEN 0 AND 100),
    error_code         TEXT NOT NULL DEFAULT '',
    created_at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    started_at         TEXT NOT NULL DEFAULT '',
    completed_at       TEXT NOT NULL DEFAULT ''
);

INSERT INTO job_new (id, job_type, execution_target, owner_type, owner_id,
    idempotency_key, input_hash, payload_json, state, priority, attempt_count,
    max_attempts, available_at, lease_owner, lease_token_hash,
    lease_expires_at, progress_percent, error_code, created_at, updated_at,
    started_at, completed_at)
    SELECT id, job_type, execution_target, owner_type, owner_id,
    idempotency_key, input_hash, payload_json, state, priority, attempt_count,
    max_attempts, available_at, lease_owner, lease_token_hash,
    lease_expires_at, progress_percent, error_code, created_at, updated_at,
    started_at, completed_at FROM job;

DROP TABLE job;

ALTER TABLE job_new RENAME TO job;

CREATE INDEX idx_job_claim ON job(execution_target, state, available_at, priority DESC, created_at, id);
CREATE INDEX idx_job_owner ON job(owner_type, owner_id, job_type);
CREATE INDEX idx_job_lease_expiry ON job(state, lease_expires_at);

CREATE TABLE job_dependency (
    job_id            TEXT NOT NULL REFERENCES job(id) ON DELETE CASCADE,
    dependency_job_id TEXT NOT NULL REFERENCES job(id) ON DELETE CASCADE,
    PRIMARY KEY (job_id, dependency_job_id),
    CHECK (job_id <> dependency_job_id)
);

INSERT INTO job_dependency (job_id, dependency_job_id)
    SELECT job_id, dependency_job_id FROM _job_dependency_backup_011;

CREATE INDEX idx_job_dependency_dependency ON job_dependency(dependency_job_id, job_id);

DROP TABLE _job_dependency_backup_011;

-- Relay worker presence ------------------------------------------------------
ALTER TABLE speech_worker ADD COLUMN llm_relay_capabilities_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE speech_worker ADD COLUMN relay_last_seen_at TEXT NOT NULL DEFAULT '';

-- Relay results: immutable once accepted. A second completion carrying ---
-- different bytes for the same job is rejected; the first row stays. ------
CREATE TABLE llm_relay_result (
    job_id      TEXT PRIMARY KEY REFERENCES job(id) ON DELETE CASCADE,
    input_hash  TEXT NOT NULL,
    operation   TEXT NOT NULL CHECK (operation IN ('chat_completion', 'list_models')),
    result_json TEXT NOT NULL,
    result_hash TEXT NOT NULL,
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX idx_llm_relay_result_input_hash ON llm_relay_result(input_hash);
