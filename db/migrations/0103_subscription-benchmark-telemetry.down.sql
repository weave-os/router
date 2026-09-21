BEGIN;

ALTER TABLE router.model_router_request_telemetry
    DROP CONSTRAINT model_router_request_telemetry_settlement_failed_check,
    DROP CONSTRAINT model_router_request_telemetry_prepaid_usage_check,
    DROP CONSTRAINT model_router_request_telemetry_linked_usage_check,
    DROP CONSTRAINT model_router_request_telemetry_included_usage_check,
    DROP CONSTRAINT model_router_request_telemetry_retail_usage_check,
    DROP CONSTRAINT model_router_request_telemetry_capacity_source_check,
    DROP CONSTRAINT model_router_request_telemetry_entitlement_version_check,
    DROP CONSTRAINT model_router_request_telemetry_subscriber_plan_check;

ALTER TABLE router.model_router_request_telemetry
    DROP COLUMN boost_optimizer_version,
    DROP COLUMN serving_binding_id,
    DROP COLUMN serving_release_id,
    DROP COLUMN serving_profile_version,
    DROP COLUMN serving_profile_id,
    DROP COLUMN settlement_failed,
    DROP COLUMN prepaid_usage_usd_micros,
    DROP COLUMN linked_usage_usd_micros,
    DROP COLUMN included_usage_usd_micros,
    DROP COLUMN retail_usage_usd_micros,
    DROP COLUMN capacity_source,
    DROP COLUMN entitlement_version,
    DROP COLUMN subscriber_plan;

COMMIT;
