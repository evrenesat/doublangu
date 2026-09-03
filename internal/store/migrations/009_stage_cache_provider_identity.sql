-- 009: include the provider connection identity in the stage-cache
-- uniqueness. Same provider ID/model/options under a different endpoint,
-- secret, timeout, type, or enabled state must not reuse an artifact cached
-- under the old configuration. Rows written before this migration carry
-- empty type/fingerprint values, so they naturally miss lookups built from
-- resolved bindings (which always carry both).
DROP INDEX IF EXISTS idx_stage_cache_identity;

CREATE UNIQUE INDEX idx_stage_cache_identity
    ON analysis_stage_cache(stage_id, input_hash, upstream_artifact_hash,
        contract_version, prompt_version, provider_id, provider_type,
        provider_config_fingerprint, model_id, options_hash);
