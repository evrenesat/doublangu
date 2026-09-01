-- Migration 005: semantic reader, durable jobs, speech renders, and workers.
-- The migration is deliberately additive. The tables created by migration 004
-- remain available for the compatibility enrichment endpoint and for old rows;
-- v2 analysis never reads legacy learning_state as semantic identity.

ALTER TABLE article ADD COLUMN content_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE article ADD COLUMN analysis_status TEXT NOT NULL DEFAULT 'needs_analysis'
    CHECK (analysis_status IN ('needs_analysis', 'queued', 'processing', 'ready', 'failed'));
ALTER TABLE article ADD COLUMN analysis_revision TEXT NOT NULL DEFAULT '';
ALTER TABLE article ADD COLUMN analysis_error_code TEXT NOT NULL DEFAULT '';
ALTER TABLE article ADD COLUMN narration_status TEXT NOT NULL DEFAULT 'not_requested'
    CHECK (narration_status IN ('not_requested', 'queued', 'generating', 'partial', 'ready', 'failed', 'purged'));
ALTER TABLE article ADD COLUMN narration_error_code TEXT NOT NULL DEFAULT '';

CREATE INDEX idx_article_analysis_status ON article(analysis_status, updated_at);
CREATE INDEX idx_article_content_hash ON article(content_hash, source_language, target_language);

CREATE TABLE job (
    id                 TEXT PRIMARY KEY,
    job_type           TEXT NOT NULL CHECK (job_type IN ('reader.analysis.v2', 'tts.avspeech.v1', 'tts.chatterbox.v3')),
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

CREATE INDEX idx_job_claim ON job(execution_target, state, available_at, priority DESC, created_at, id);
CREATE INDEX idx_job_owner ON job(owner_type, owner_id, job_type);
CREATE INDEX idx_job_lease_expiry ON job(state, lease_expires_at);

CREATE TABLE job_dependency (
    job_id            TEXT NOT NULL REFERENCES job(id) ON DELETE CASCADE,
    dependency_job_id TEXT NOT NULL REFERENCES job(id) ON DELETE CASCADE,
    PRIMARY KEY (job_id, dependency_job_id),
    CHECK (job_id <> dependency_job_id)
);

CREATE INDEX idx_job_dependency_dependency ON job_dependency(dependency_job_id, job_id);

CREATE TABLE analysis_cache (
    id                      TEXT PRIMARY KEY,
    content_hash            TEXT NOT NULL,
    source_language         TEXT NOT NULL,
    target_language         TEXT NOT NULL,
    contract_version        TEXT NOT NULL,
    provider_id             TEXT NOT NULL,
    provider_model          TEXT NOT NULL DEFAULT '',
    prompt_version          TEXT NOT NULL,
    validated_response_json TEXT NOT NULL,
    response_hash           TEXT NOT NULL,
    created_at              TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (content_hash, source_language, target_language, contract_version)
);

CREATE TABLE semantic_item (
    id              TEXT PRIMARY KEY,
    source_language TEXT NOT NULL,
    kind            TEXT NOT NULL CHECK (kind IN ('word', 'phrase', 'idiom', 'expression', 'proverb')),
    canonical_form  TEXT NOT NULL CHECK (length(canonical_form) > 0),
    normalized_form TEXT NOT NULL CHECK (length(normalized_form) > 0),
    lemma           TEXT NOT NULL DEFAULT '',
    part_of_speech  TEXT NOT NULL DEFAULT '',
    created_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX idx_semantic_item_lookup ON semantic_item(source_language, normalized_form, lemma);

CREATE TABLE semantic_sense (
    id                        TEXT PRIMARY KEY,
    semantic_item_id          TEXT NOT NULL REFERENCES semantic_item(id) ON DELETE CASCADE,
    target_language           TEXT NOT NULL,
    sense_discriminator       TEXT NOT NULL CHECK (length(sense_discriminator) > 0),
    primary_translation       TEXT NOT NULL CHECK (length(primary_translation) > 0),
    alternatives_json         TEXT NOT NULL DEFAULT '[]',
    literal_translation       TEXT NOT NULL DEFAULT '',
    meaning_note              TEXT NOT NULL DEFAULT '',
    usage_note                TEXT NOT NULL DEFAULT '',
    parts_note                TEXT NOT NULL DEFAULT '',
    canonical_pronunciation_text TEXT NOT NULL,
    provider_id               TEXT NOT NULL DEFAULT '',
    provider_model            TEXT NOT NULL DEFAULT '',
    analysis_contract_version TEXT NOT NULL,
    created_at                TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at                TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    retired_at                TEXT NOT NULL DEFAULT ''
);

CREATE UNIQUE INDEX idx_semantic_sense_active_discriminator
    ON semantic_sense(semantic_item_id, target_language, sense_discriminator)
    WHERE retired_at = '';
CREATE INDEX idx_semantic_sense_item ON semantic_sense(semantic_item_id, target_language);

CREATE TABLE semantic_learning_state (
    semantic_sense_id TEXT PRIMARY KEY REFERENCES semantic_sense(id) ON DELETE CASCADE,
    status            TEXT NOT NULL CHECK (status IN ('learned', 'unlearned')),
    updated_at        TEXT NOT NULL
);

CREATE TABLE article_sentence (
    id              TEXT PRIMARY KEY,
    article_block_id TEXT NOT NULL REFERENCES article_block(id) ON DELETE CASCADE,
    sentence_index  INTEGER NOT NULL CHECK (sentence_index >= 0),
    start_utf16     INTEGER NOT NULL CHECK (start_utf16 >= 0),
    end_utf16       INTEGER NOT NULL CHECK (end_utf16 > start_utf16),
    source_text     TEXT NOT NULL CHECK (length(source_text) > 0),
    source_hash     TEXT NOT NULL,
    UNIQUE (article_block_id, sentence_index),
    UNIQUE (article_block_id, start_utf16, end_utf16)
);

CREATE INDEX idx_article_sentence_block ON article_sentence(article_block_id, sentence_index);

CREATE TABLE article_occurrence (
    id                  TEXT PRIMARY KEY,
    article_block_id    TEXT NOT NULL REFERENCES article_block(id) ON DELETE CASCADE,
    article_sentence_id TEXT REFERENCES article_sentence(id) ON DELETE SET NULL,
    semantic_sense_id   TEXT REFERENCES semantic_sense(id) ON DELETE RESTRICT,
    kind                TEXT NOT NULL CHECK (kind IN ('word', 'phrase', 'idiom', 'expression', 'proverb')),
    role                TEXT NOT NULL CHECK (role IN ('token', 'contiguous_construction', 'discontinuous_construction')),
    shadow_policy       TEXT NOT NULL CHECK (shadow_policy IN ('token', 'group', 'marker', 'none')),
    shadow_text         TEXT NOT NULL DEFAULT '',
    canonical_pronunciation_text TEXT NOT NULL DEFAULT '',
    context_pronunciation_key TEXT NOT NULL DEFAULT '',
    confidence_milli    INTEGER NOT NULL DEFAULT 0 CHECK (confidence_milli BETWEEN 0 AND 1000),
    created_at          TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX idx_article_occurrence_block ON article_occurrence(article_block_id, id);
CREATE INDEX idx_article_occurrence_sentence ON article_occurrence(article_sentence_id, id);
CREATE INDEX idx_article_occurrence_sense ON article_occurrence(semantic_sense_id);

CREATE TABLE article_occurrence_span (
    id                    TEXT PRIMARY KEY,
    article_occurrence_id TEXT NOT NULL REFERENCES article_occurrence(id) ON DELETE CASCADE,
    span_index            INTEGER NOT NULL CHECK (span_index >= 0),
    start_utf16           INTEGER NOT NULL CHECK (start_utf16 >= 0),
    end_utf16             INTEGER NOT NULL CHECK (end_utf16 > start_utf16),
    source_text           TEXT NOT NULL CHECK (length(source_text) > 0),
    UNIQUE (article_occurrence_id, span_index)
);

CREATE INDEX idx_article_occurrence_span_lookup ON article_occurrence_span(article_occurrence_id, span_index);

CREATE TABLE speech_unit (
    id                      TEXT PRIMARY KEY,
    language                TEXT NOT NULL,
    unit_kind               TEXT NOT NULL CHECK (unit_kind IN ('word', 'phrase', 'sentence')),
    spoken_text             TEXT NOT NULL CHECK (length(spoken_text) > 0),
    normalized_text_hash    TEXT NOT NULL,
    context_pronunciation_key TEXT NOT NULL DEFAULT '',
    semantic_sense_id       TEXT REFERENCES semantic_sense(id) ON DELETE SET NULL,
    created_at              TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE UNIQUE INDEX idx_speech_unit_identity
    ON speech_unit(language, unit_kind, normalized_text_hash, context_pronunciation_key);
CREATE INDEX idx_speech_unit_lookup ON speech_unit(language, unit_kind, normalized_text_hash);

CREATE TABLE speech_profile (
    id                  TEXT PRIMARY KEY,
    engine              TEXT NOT NULL,
    model_revision      TEXT NOT NULL,
    language            TEXT NOT NULL,
    voice_identifier     TEXT NOT NULL,
    reference_audio_hash TEXT NOT NULL DEFAULT '',
    speed_milli         INTEGER NOT NULL CHECK (speed_milli > 0),
    pitch_cents         INTEGER NOT NULL,
    mapping_version     TEXT NOT NULL,
    mime_type           TEXT NOT NULL,
    codec               TEXT NOT NULL,
    sample_rate_hz      INTEGER NOT NULL CHECK (sample_rate_hz > 0),
    channels            INTEGER NOT NULL CHECK (channels > 0),
    active              INTEGER NOT NULL CHECK (active IN (0, 1)),
    created_at          TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at          TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX idx_speech_profile_active ON speech_profile(engine, language, active);

CREATE TABLE audio_render (
    id               TEXT PRIMARY KEY,
    speech_unit_id   TEXT NOT NULL REFERENCES speech_unit(id) ON DELETE CASCADE,
    speech_profile_id TEXT NOT NULL REFERENCES speech_profile(id) ON DELETE RESTRICT,
    request_hash     TEXT NOT NULL UNIQUE,
    retention_class  TEXT NOT NULL CHECK (retention_class IN ('lexical_permanent', 'article_narration')),
    state            TEXT NOT NULL CHECK (state IN ('queued', 'generating', 'ready', 'failed', 'purged')),
    error_code       TEXT NOT NULL DEFAULT '',
    duration_ms      INTEGER NOT NULL DEFAULT 0 CHECK (duration_ms >= 0),
    size_bytes       INTEGER NOT NULL DEFAULT 0 CHECK (size_bytes >= 0),
    created_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    ready_at         TEXT NOT NULL DEFAULT ''
);

CREATE INDEX idx_audio_render_unit_profile ON audio_render(speech_unit_id, speech_profile_id, retention_class);
CREATE INDEX idx_audio_render_state ON audio_render(state, retention_class);

CREATE TABLE audio_blob_reference (
    audio_render_id TEXT PRIMARY KEY REFERENCES audio_render(id) ON DELETE CASCADE,
    blob_digest     TEXT NOT NULL REFERENCES blob(digest) ON DELETE RESTRICT
);

CREATE INDEX idx_audio_blob_reference_digest ON audio_blob_reference(blob_digest);

CREATE TABLE article_occurrence_audio (
    article_occurrence_id TEXT NOT NULL REFERENCES article_occurrence(id) ON DELETE CASCADE,
    audio_render_id       TEXT NOT NULL REFERENCES audio_render(id) ON DELETE RESTRICT,
    purpose               TEXT NOT NULL CHECK (purpose = 'pronunciation'),
    preferred             INTEGER NOT NULL DEFAULT 1 CHECK (preferred IN (0, 1)),
    PRIMARY KEY (article_occurrence_id, audio_render_id, purpose)
);

CREATE TABLE article_sentence_audio (
    article_id          TEXT NOT NULL REFERENCES article(id) ON DELETE CASCADE,
    article_sentence_id TEXT NOT NULL REFERENCES article_sentence(id) ON DELETE CASCADE,
    audio_render_id     TEXT NOT NULL REFERENCES audio_render(id) ON DELETE RESTRICT,
    sequence_index      INTEGER NOT NULL CHECK (sequence_index >= 0),
    purpose             TEXT NOT NULL CHECK (purpose = 'narration'),
    PRIMARY KEY (article_sentence_id, purpose),
    UNIQUE (article_sentence_id, audio_render_id, purpose),
    UNIQUE (article_id, sequence_index)
);

CREATE INDEX idx_article_sentence_audio_render ON article_sentence_audio(audio_render_id);
CREATE INDEX idx_article_sentence_audio_article ON article_sentence_audio(article_id, sequence_index);

CREATE TABLE speech_worker (
    id               TEXT PRIMARY KEY,
    name             TEXT NOT NULL,
    protocol_version TEXT NOT NULL,
    token_hash       TEXT NOT NULL UNIQUE,
    revoked_at       TEXT NOT NULL DEFAULT '',
    last_seen_at     TEXT NOT NULL DEFAULT '',
    capabilities_json TEXT NOT NULL DEFAULT '{}',
    software_version TEXT NOT NULL DEFAULT '',
    created_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX idx_speech_worker_active ON speech_worker(revoked_at, last_seen_at);

CREATE TABLE speech_worker_enrollment (
    id         TEXT PRIMARY KEY,
    token_hash TEXT NOT NULL UNIQUE,
    expires_at TEXT NOT NULL,
    used_at    TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX idx_speech_worker_enrollment_expiry ON speech_worker_enrollment(expires_at, used_at);
