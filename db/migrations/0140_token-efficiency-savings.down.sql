BEGIN;

ALTER TABLE router.model_router_request_telemetry
    DROP COLUMN baseline_warm_prefill_tokens;

DROP TABLE router.token_efficiency_quality_factors;

DROP TABLE router.token_efficiency_ratios;

COMMIT;
