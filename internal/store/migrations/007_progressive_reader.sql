-- Migration 007: progressive paragraph publication, deterministic source
-- sentences, exact construction membership, and the owner reader preference.
--
-- The migration is deterministic and provider-free. It never identifies,
-- mutates, enqueues, or reanalyzes an article specially, and it never guesses
-- construction membership from legacy spans.

-- Active analysis job identity and sentence provenance on the article.
ALTER TABLE article ADD COLUMN analysis_job_id TEXT NOT NULL DEFAULT '';
ALTER TABLE article ADD COLUMN sentence_revision TEXT NOT NULL DEFAULT '';

-- Per-paragraph analysis lifecycle and published-materialization provenance.
ALTER TABLE article_block ADD COLUMN analysis_job_id TEXT NOT NULL DEFAULT '';
ALTER TABLE article_block ADD COLUMN analysis_status TEXT NOT NULL DEFAULT 'pending'
    CHECK (analysis_status IN ('pending', 'processing', 'ready', 'failed'));
ALTER TABLE article_block ADD COLUMN analysis_error_code TEXT NOT NULL DEFAULT '';
ALTER TABLE article_block ADD COLUMN published_analysis_job_id TEXT NOT NULL DEFAULT '';
ALTER TABLE article_block ADD COLUMN published_analysis_run_id TEXT NOT NULL DEFAULT '';
ALTER TABLE article_block ADD COLUMN published_analysis_revision TEXT NOT NULL DEFAULT '';
ALTER TABLE article_block ADD COLUMN published_analysis_model TEXT NOT NULL DEFAULT '';
ALTER TABLE article_block ADD COLUMN published_analysis_effort TEXT NOT NULL DEFAULT '';
ALTER TABLE article_block ADD COLUMN published_at TEXT NOT NULL DEFAULT '';

-- Exact lexical membership for constructions. Rows are written only by the
-- validated v3 persistence path; the migration never infers membership from
-- legacy broad spans.
CREATE TABLE article_construction_member (
    construction_occurrence_id TEXT NOT NULL REFERENCES article_occurrence(id) ON DELETE CASCADE,
    token_occurrence_id        TEXT NOT NULL REFERENCES article_occurrence(id) ON DELETE CASCADE,
    member_index               INTEGER NOT NULL CHECK (member_index >= 0),
    PRIMARY KEY (construction_occurrence_id, token_occurrence_id),
    UNIQUE (construction_occurrence_id, member_index),
    CHECK (construction_occurrence_id <> token_occurrence_id)
);

CREATE INDEX idx_construction_member_token
    ON article_construction_member(token_occurrence_id, member_index);

-- Singleton owner-wide reader preference. The server remains authoritative;
-- local storage only mirrors the last successful value to avoid flicker.
CREATE TABLE reader_settings (
    id                 INTEGER PRIMARY KEY CHECK (id = 1),
    pronounce_on_hover INTEGER NOT NULL DEFAULT 1 CHECK (pronounce_on_hover IN (0, 1)),
    updated_at         TEXT NOT NULL
);

INSERT INTO reader_settings (id, pronounce_on_hover, updated_at)
VALUES (1, 1, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));

-- Backfill rules ------------------------------------------------------------
-- 1. Articles that already have source sentence rows keep them as the
--    authoritative narration anchors. The legacy revision marker prevents
--    silent re-segmentation; articles without rows remain blank so the code
--    can lazily create deterministic sentences when it first needs them.
UPDATE article SET sentence_revision = 'legacy.analysis'
WHERE EXISTS (
    SELECT 1
    FROM article_block AS b
    JOIN article_sentence AS s ON s.article_block_id = b.id
    WHERE b.article_id = article.id
);

-- 2. Blocks carrying an accepted semantic materialization keep that
--    materialization published and readable. Their published provenance is
--    copied from the article because pre-007 runs never recorded a per-block
--    job or run identity. Legacy v2 rows are not republished or requeued.
UPDATE article_block AS block SET
    analysis_status = 'ready',
    analysis_job_id = '',
    analysis_error_code = '',
    published_analysis_job_id = '',
    published_analysis_run_id = '',
    published_analysis_revision = COALESCE((
        SELECT analysis_revision FROM article WHERE article.id = block.article_id), ''),
    published_analysis_model = COALESCE((
        SELECT analysis_model FROM article WHERE article.id = block.article_id), ''),
    published_analysis_effort = COALESCE((
        SELECT analysis_effort FROM article WHERE article.id = block.article_id), ''),
    published_at = COALESCE((
        SELECT updated_at FROM article WHERE article.id = block.article_id), '')
WHERE EXISTS (
    SELECT 1
    FROM article_occurrence AS occurrence
    WHERE occurrence.article_block_id = block.id
);

-- 3. Blocks without an accepted materialization stay pending. Under a failed
--    article the first unresolved block carries the failure so the reader can
--    point the owner at exactly where a retry continues; later blocks remain
--    pending. A failed article whose blocks all still hold an older accepted
--    materialization keeps every block readable with no failed marker.
UPDATE article_block AS block SET
    analysis_status = 'failed'
WHERE block.analysis_status = 'pending'
  AND EXISTS (
    SELECT 1 FROM article AS article_row
    WHERE article_row.id = block.article_id
      AND article_row.analysis_status = 'failed'
  )
  AND block.block_index = (
    SELECT MIN(pending.block_index)
    FROM article_block AS pending
    WHERE pending.article_id = block.article_id
      AND pending.analysis_status = 'pending'
  );

-- Incompatible pre-v3 analysis jobs ------------------------------------------
-- Contract v3 cannot be satisfied by queued or in-flight v2 jobs, and their
-- payloads are rejected by the new runner without ever transitioning the
-- article. Cancel them deterministically (no provider is invoked) and move
-- their articles to the recoverable failed state so the owner sees the Retry
-- and Run fresh actions instead of a permanently polling queued state.
UPDATE job SET
    state = 'canceled',
    error_code = 'v1.analysis_contract_upgraded',
    lease_owner = '',
    lease_token_hash = '',
    lease_expires_at = '',
    completed_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE job_type = 'reader.analysis.v2'
  AND state IN ('queued', 'leased', 'running');

UPDATE article SET
    analysis_status = 'failed',
    analysis_error_code = 'v1.analysis_contract_upgraded',
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE analysis_status IN ('queued', 'processing');
