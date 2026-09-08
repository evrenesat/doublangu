-- Migration 014: immutable prompt version library, per-profile prompt
-- selections, the independent Explore binding, and run operation/subject/phase
-- columns. Additive only: no existing column, row, or hash input changes, and
-- no queued job payload is rewritten.

-- Prompt version library -------------------------------------------------------
-- One immutable version stream per fixed prompt type. Application code only
-- ever INSERTs rows here (allocated under a transaction so concurrent saves
-- cannot take the same version); UPDATE and DELETE have no code path.
CREATE TABLE prompt_version (
    id               TEXT PRIMARY KEY,
    prompt_type      TEXT NOT NULL CHECK (prompt_type IN
        ('linguistic_analysis', 'article_translation', 'explore',
         'sentence_translation', 'correction')),
    version          INTEGER NOT NULL CHECK (version > 0),
    label            TEXT NOT NULL DEFAULT '',
    instruction_text TEXT NOT NULL CHECK (length(instruction_text) > 0),
    content_hash     TEXT NOT NULL,
    created_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (prompt_type, version)
);

CREATE INDEX idx_prompt_version_type ON prompt_version(prompt_type, version DESC);

-- Parent key for the composite matching-type foreign key below.
CREATE UNIQUE INDEX idx_prompt_version_type_id ON prompt_version(prompt_type, id);

-- Per-profile prompt selections ------------------------------------------------
-- Exactly one pinned version per prompt type per profile. The composite
-- foreign key makes a cross-type selection impossible at the schema level:
-- (prompt_type, prompt_version_id) must pair a stored version of that type.
CREATE TABLE profile_prompt_selection (
    profile_id        TEXT NOT NULL REFERENCES analysis_pipeline_profile(id) ON DELETE CASCADE,
    prompt_type       TEXT NOT NULL CHECK (prompt_type IN
        ('linguistic_analysis', 'article_translation', 'explore',
         'sentence_translation', 'correction')),
    prompt_version_id TEXT NOT NULL,
    PRIMARY KEY (profile_id, prompt_type),
    FOREIGN KEY (prompt_version_id) REFERENCES prompt_version(id) ON DELETE RESTRICT,
    FOREIGN KEY (prompt_type, prompt_version_id) REFERENCES prompt_version(prompt_type, id) ON DELETE RESTRICT
);

-- Independent Explore binding ---------------------------------------------------
-- One row per profile; seeded by copying the translation binding exactly once
-- so existing profiles keep working unchanged while Explore becomes editable.
CREATE TABLE analysis_profile_explore_binding (
    profile_id   TEXT PRIMARY KEY REFERENCES analysis_pipeline_profile(id) ON DELETE CASCADE,
    provider_id  TEXT NOT NULL,
    model_id     TEXT NOT NULL,
    options_json TEXT NOT NULL,
    options_hash TEXT NOT NULL
);

INSERT INTO analysis_profile_explore_binding (profile_id, provider_id, model_id, options_json, options_hash)
SELECT profile_id, provider_id, model_id, options_json, options_hash
FROM analysis_pipeline_binding
WHERE stage_id = 'translation';

-- Run operation / subject / phase columns ---------------------------------------
-- operation_type defaults to the only legacy operation; subject columns stay
-- empty for legacy article rows. phase starts empty: rows written before this
-- migration existed are not phase-managed, so terminal legacy rows are
-- backfilled to 'finished' below and in-flight rows stay '' until the startup
-- recovery finalizes them. New code sets queued/running/finished explicitly.
ALTER TABLE analysis_run ADD COLUMN operation_type TEXT NOT NULL DEFAULT 'article_analysis'
    CHECK (operation_type IN ('article_analysis', 'explore', 'sentence_translation'));
ALTER TABLE analysis_run ADD COLUMN subject_id TEXT NOT NULL DEFAULT '';
ALTER TABLE analysis_run ADD COLUMN subject_label TEXT NOT NULL DEFAULT '';
ALTER TABLE analysis_run ADD COLUMN phase TEXT NOT NULL DEFAULT ''
    CHECK (phase IN ('', 'queued', 'running', 'finished'));
UPDATE analysis_run SET phase = 'finished' WHERE status IN ('succeeded', 'failed');

-- Per-attempt failure phase, empty by default for legacy attempts.
ALTER TABLE analysis_stage_attempt ADD COLUMN error_phase TEXT NOT NULL DEFAULT ''
    CHECK (error_phase IN ('', 'preflight', 'provider', 'stage_validation',
                           'final_validation', 'storage', 'interrupted'));

-- Explore preflight failure evidence: the run that last touched the entry.
-- Nullable with ON DELETE SET NULL so run retention never deletes entries.
ALTER TABLE dictionary_entry ADD COLUMN last_run_id TEXT REFERENCES analysis_run(id) ON DELETE SET NULL;
