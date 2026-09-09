-- Migration 015: sentence translation storage (`reader.sentence_translation.v1`).
-- Adds the `sentence_translation` table (one saved result per source
-- sentence, keyed to the exact source anchor) and widens only the `job`
-- `job_type` CHECK to admit the new job type. The job rebuild repeats
-- migration 013's child-table preservation pattern for BOTH referencing
-- child tables (`job_dependency` and `llm_relay_result`, both with cascading
-- foreign keys), keeping every runtime row, index, and the migration-012
-- `ailocals_result_sha256` column. Migrations run inside a transaction with
-- foreign keys enabled. Existing articles start with no saved translations;
-- no queued job payload is rewritten.

-- Dependency backup ----------------------------------------------------------
CREATE TABLE _job_dependency_backup_015 (
    job_id            TEXT NOT NULL,
    dependency_job_id TEXT NOT NULL,
    PRIMARY KEY (job_id, dependency_job_id)
);

INSERT INTO _job_dependency_backup_015 (job_id, dependency_job_id)
    SELECT job_id, dependency_job_id FROM job_dependency;

-- Relay-result backup (job_id is its primary key). ----------------------------
CREATE TABLE _llm_relay_result_backup_015 (
    job_id      TEXT PRIMARY KEY,
    input_hash  TEXT NOT NULL,
    operation   TEXT NOT NULL,
    result_json TEXT NOT NULL,
    result_hash TEXT NOT NULL,
    created_at  TEXT NOT NULL
);

INSERT INTO _llm_relay_result_backup_015 (job_id, input_hash, operation, result_json, result_hash, created_at)
    SELECT job_id, input_hash, operation, result_json, result_hash, created_at FROM llm_relay_result;

DROP TABLE llm_relay_result;
DROP TABLE job_dependency;

-- Dictionary pointer backup --------------------------------------------------
-- Dropping the job table below fires the dictionary_entry last_job_id
-- ON DELETE SET NULL action. The pending-job pointers are backed up and
-- restored around the rebuild; job ids are preserved byte-for-field, so the
-- restored pointers reference the same jobs and no entry loses its linkage.
CREATE TABLE _dictionary_last_job_backup_015 (
    id          TEXT PRIMARY KEY,
    last_job_id TEXT
);

INSERT INTO _dictionary_last_job_backup_015 (id, last_job_id)
    SELECT id, last_job_id FROM dictionary_entry;

-- Job rebuild: byte-identical to the post-013 schema except the -------------
-- job_type CHECK, which additionally admits `reader.sentence_translation.v1`.
CREATE TABLE job_new (
    id                 TEXT PRIMARY KEY,
    job_type           TEXT NOT NULL CHECK (job_type IN ('reader.analysis.v2', 'tts.avspeech.v1', 'tts.chatterbox.v3', 'llm.relay.v1', 'reader.dictionary.v1', 'reader.sentence_translation.v1')),
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
    completed_at       TEXT NOT NULL DEFAULT '',
    ailocals_result_sha256 TEXT NOT NULL DEFAULT ''
);

INSERT INTO job_new (id, job_type, execution_target, owner_type, owner_id,
    idempotency_key, input_hash, payload_json, state, priority, attempt_count,
    max_attempts, available_at, lease_owner, lease_token_hash,
    lease_expires_at, progress_percent, error_code, created_at, updated_at,
    started_at, completed_at, ailocals_result_sha256)
    SELECT id, job_type, execution_target, owner_type, owner_id,
    idempotency_key, input_hash, payload_json, state, priority, attempt_count,
    max_attempts, available_at, lease_owner, lease_token_hash,
    lease_expires_at, progress_percent, error_code, created_at, updated_at,
    started_at, completed_at, ailocals_result_sha256 FROM job;

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
    SELECT job_id, dependency_job_id FROM _job_dependency_backup_015;

CREATE INDEX idx_job_dependency_dependency ON job_dependency(dependency_job_id, job_id);

DROP TABLE _job_dependency_backup_015;

CREATE TABLE llm_relay_result (
    job_id      TEXT PRIMARY KEY REFERENCES job(id) ON DELETE CASCADE,
    input_hash  TEXT NOT NULL,
    operation   TEXT NOT NULL CHECK (operation IN ('chat_completion', 'list_models')),
    result_json TEXT NOT NULL,
    result_hash TEXT NOT NULL,
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX idx_llm_relay_result_input_hash ON llm_relay_result(input_hash);

INSERT INTO llm_relay_result (job_id, input_hash, operation, result_json, result_hash, created_at)
    SELECT job_id, input_hash, operation, result_json, result_hash, created_at FROM _llm_relay_result_backup_015;

DROP TABLE _llm_relay_result_backup_015;

UPDATE dictionary_entry SET last_job_id = (
    SELECT last_job_id FROM _dictionary_last_job_backup_015
    WHERE _dictionary_last_job_backup_015.id = dictionary_entry.id
);

DROP TABLE _dictionary_last_job_backup_015;

-- Sentence translations ------------------------------------------------------
-- One saved translation per source sentence in the article's target language.
-- The row is keyed to the exact source anchor: sentence_id references
-- article_sentence with cascading delete, and writers must verify the stored
-- source_hash still matches before saving, so a result is never attached to
-- changed or recreated anchors. translation_text is null until the first
-- successful generation; result metadata uses empty defaults when absent,
-- and last_job_id stays nullable.
CREATE TABLE sentence_translation (
    sentence_id      TEXT PRIMARY KEY REFERENCES article_sentence(id) ON DELETE CASCADE,
    source_hash      TEXT NOT NULL CHECK (length(source_hash) > 0),
    target_language  TEXT NOT NULL CHECK (length(target_language) > 0),
    translation_text TEXT,
    result_hash      TEXT NOT NULL DEFAULT '',
    provenance_json  TEXT NOT NULL DEFAULT '',
    last_job_id      TEXT REFERENCES job(id) ON DELETE SET NULL,
    last_run_id      TEXT REFERENCES analysis_run(id) ON DELETE SET NULL,
    updated_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX idx_sentence_translation_last_job ON sentence_translation(last_job_id);
CREATE INDEX idx_sentence_translation_last_run ON sentence_translation(last_run_id);
