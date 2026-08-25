BEGIN;

DROP VIEW router.production_request_telemetry;

ALTER TABLE router.model_router_request_telemetry
    DROP COLUMN planner_shadow_savings_usd_micros,
    DROP COLUMN planner_shadow_outcome,
    DROP COLUMN planner_pin_cache_cold,
    DROP COLUMN planner_eviction_cost_usd_micros,
    DROP COLUMN planner_expected_savings_usd_micros,
    DROP COLUMN planner_pin_provider,
    DROP COLUMN planner_pin_model,
    DROP COLUMN planner_reason,
    DROP COLUMN planner_outcome;

CREATE VIEW router.production_request_telemetry AS
SELECT * FROM router.model_router_request_telemetry
WHERE span_type = 'router.upstream'
  AND (client_app IS NULL OR client_app NOT LIKE 'weave-eval%');

COMMIT;
