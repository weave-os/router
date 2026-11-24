-- Admits the prod/beta serving target in the two tables that persist a target.
--
-- model_router_request_telemetry is large and written on every turn. Re-adding
-- a CHECK normally scans every row under ACCESS EXCLUSIVE, which would stall
-- those writes. So the first transaction swaps each constraint for its NOT
-- VALID form, which needs that lock only briefly and does not scan. The second
-- transaction validates under SHARE UPDATE EXCLUSIVE, which lets reads and
-- writes continue during the scan. Validation cannot fail: the new set is a
-- superset of the old one, which every existing row already satisfies.
BEGIN;

ALTER TABLE router.session_release_bindings
    DROP CONSTRAINT session_release_bindings_target_check;

ALTER TABLE router.session_release_bindings
    ADD CONSTRAINT session_release_bindings_target_check
        CHECK (target IN ('staging', 'prod/stable', 'prod/beta', 'prod/weave-internal')) NOT VALID;

ALTER TABLE router.model_router_request_telemetry
    DROP CONSTRAINT model_router_request_telemetry_serving_target_check;

ALTER TABLE router.model_router_request_telemetry
    ADD CONSTRAINT model_router_request_telemetry_serving_target_check
        CHECK (serving_target IS NULL OR serving_target IN ('staging', 'prod/stable', 'prod/beta', 'prod/weave-internal')) NOT VALID;

COMMIT;

BEGIN;

ALTER TABLE router.session_release_bindings
    VALIDATE CONSTRAINT session_release_bindings_target_check;

ALTER TABLE router.model_router_request_telemetry
    VALIDATE CONSTRAINT model_router_request_telemetry_serving_target_check;

COMMIT;
