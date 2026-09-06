-- Migration 012: ailocals.v1 universal worker presence and common-result
-- digests. Purely additive: legacy clients keep their existing behavior and
-- never read these columns.
--
-- ailocals_presence_json stores the validated full common capability-state
-- snapshot, per-capability last-seen timestamps, and the protocol marker. No
-- endpoints, keys, or model paths.
--
-- ailocals_result_sha256 is written atomically with common-protocol
-- completion and fences byte-identical common retries independently of the
-- existing canonical relay/domain result hash, which stays authoritative.
ALTER TABLE speech_worker ADD COLUMN ailocals_presence_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE job ADD COLUMN ailocals_result_sha256 TEXT NOT NULL DEFAULT '';
