BEGIN;

-- Canonical model IDs named by the request's x-weave-allowed-models header,
-- before intersection with the installation allowlist. NULL when absent.
ALTER TABLE router.model_router_request_telemetry
    ADD COLUMN requested_allowed_models VARCHAR[];

COMMIT;
