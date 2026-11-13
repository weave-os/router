BEGIN;

ALTER TABLE router.model_router_request_telemetry
    DROP COLUMN authority_shadow_fresh_score,
    DROP COLUMN authority_shadow_stay_score,
    DROP COLUMN authority_shadow_corrected_savings_usd_micros,
    DROP COLUMN authority_shadow_corrected_outcome,
    DROP COLUMN authority_shadow_pin_cache_cold,
    DROP COLUMN authority_shadow_eviction_cost_usd_micros,
    DROP COLUMN authority_shadow_savings_usd_micros,
    DROP COLUMN authority_shadow_stay_provider,
    DROP COLUMN authority_shadow_stay_model,
    DROP COLUMN authority_shadow_reason,
    DROP COLUMN authority_shadow_would_diverge,
    DROP COLUMN authority_shadow_outcome;

COMMIT;
