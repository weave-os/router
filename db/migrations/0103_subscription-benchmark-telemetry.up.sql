BEGIN;

ALTER TABLE router.model_router_request_telemetry
    ADD COLUMN subscriber_plan VARCHAR,
    ADD COLUMN entitlement_version BIGINT,
    ADD COLUMN capacity_source VARCHAR,
    ADD COLUMN retail_usage_usd_micros BIGINT,
    ADD COLUMN included_usage_usd_micros BIGINT,
    ADD COLUMN linked_usage_usd_micros BIGINT,
    ADD COLUMN prepaid_usage_usd_micros BIGINT,
    ADD COLUMN settlement_failed BOOLEAN,
    ADD COLUMN serving_profile_id VARCHAR,
    ADD COLUMN serving_profile_version VARCHAR,
    ADD COLUMN serving_release_id VARCHAR,
    ADD COLUMN serving_binding_id VARCHAR,
    ADD COLUMN boost_optimizer_version VARCHAR;

ALTER TABLE router.model_router_request_telemetry
    ADD CONSTRAINT model_router_request_telemetry_subscriber_plan_check
        CHECK (subscriber_plan IS NULL OR subscriber_plan IN ('max', 'boost')),
    ADD CONSTRAINT model_router_request_telemetry_entitlement_version_check
        CHECK (entitlement_version IS NULL OR entitlement_version > 0),
    ADD CONSTRAINT model_router_request_telemetry_capacity_source_check
        CHECK (
            capacity_source IS NULL
            OR capacity_source IN (
                'included_router',
                'linked_claude',
                'linked_codex',
                'prepaid',
                'billing_override'
            )
        ),
    ADD CONSTRAINT model_router_request_telemetry_retail_usage_check
        CHECK (
            (capacity_source IS NULL AND retail_usage_usd_micros IS NULL)
            OR (
                capacity_source IS NOT NULL
                AND retail_usage_usd_micros IS NOT NULL
                AND retail_usage_usd_micros >= 0
            )
        ),
    ADD CONSTRAINT model_router_request_telemetry_included_usage_check
        CHECK (
            (
                capacity_source = 'included_router'
                AND included_usage_usd_micros IS NOT NULL
                AND included_usage_usd_micros = retail_usage_usd_micros
            )
            OR (
                capacity_source IS DISTINCT FROM 'included_router'
                AND included_usage_usd_micros IS NULL
            )
        ),
    ADD CONSTRAINT model_router_request_telemetry_linked_usage_check
        CHECK (
            (
                capacity_source IN ('linked_claude', 'linked_codex')
                AND linked_usage_usd_micros IS NOT NULL
                AND linked_usage_usd_micros = retail_usage_usd_micros
            )
            OR (
                capacity_source IS NULL
                OR capacity_source NOT IN ('linked_claude', 'linked_codex')
            )
            AND linked_usage_usd_micros IS NULL
        ),
    ADD CONSTRAINT model_router_request_telemetry_prepaid_usage_check
        CHECK (
            (
                capacity_source = 'prepaid'
                AND prepaid_usage_usd_micros IS NOT NULL
                AND prepaid_usage_usd_micros = retail_usage_usd_micros
            )
            OR (
                capacity_source IS DISTINCT FROM 'prepaid'
                AND prepaid_usage_usd_micros IS NULL
            )
        ),
    ADD CONSTRAINT model_router_request_telemetry_settlement_failed_check
        CHECK (
            settlement_failed IS NULL
            OR capacity_source IN ('included_router', 'prepaid')
        );

COMMIT;
