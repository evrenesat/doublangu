-- 010: persist explicit truncation flags for attempt diagnostics. Attempt
-- usage/timing/metadata are bounded structured JSON (a sentinel object
-- replaces oversized values so they stay valid); stderr and error detail
-- are bounded excerpts. Every bound carries its flag so run detail can say
-- what was cut instead of presenting chopped values as complete.
ALTER TABLE analysis_stage_attempt
    ADD COLUMN usage_truncated INTEGER NOT NULL DEFAULT 0 CHECK (usage_truncated IN (0, 1));
ALTER TABLE analysis_stage_attempt
    ADD COLUMN timing_truncated INTEGER NOT NULL DEFAULT 0 CHECK (timing_truncated IN (0, 1));
ALTER TABLE analysis_stage_attempt
    ADD COLUMN metadata_truncated INTEGER NOT NULL DEFAULT 0 CHECK (metadata_truncated IN (0, 1));
ALTER TABLE analysis_stage_attempt
    ADD COLUMN stderr_truncated INTEGER NOT NULL DEFAULT 0 CHECK (stderr_truncated IN (0, 1));
ALTER TABLE analysis_stage_attempt
    ADD COLUMN error_detail_truncated INTEGER NOT NULL DEFAULT 0 CHECK (error_detail_truncated IN (0, 1));
