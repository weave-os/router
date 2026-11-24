-- Restores the constraints without prod/beta, using the same two-step shape as
-- the up migration so the telemetry scan never runs under ACCESS EXCLUSIVE.
--
-- The first transaction swaps in the narrow constraints as NOT VALID. From its
-- commit on, no new prod/beta row can be written. The second transaction then
-- removes the prod/beta rows written before that commit and validates.
--
-- A session binding pins a conversation to a target's activation, so a
-- prod/beta binding is meaningless once the target is gone: delete it.
-- Telemetry rows are request history, so keep them and clear only the target,
-- matching rows recorded before serving_target existed.
BEGIN;

ALTER TABLE router.session_release_bindings
    DROP CONSTRAINT session_release_bindings_target_check;

ALTER TABLE router.session_release_bindings
    ADD CONSTRAINT session_release_bindings_target_check
        CHECK (target IN ('staging', 'prod/stable', 'prod/weave-internal')) NOT VALID;

ALTER TABLE router.model_router_request_telemetry
    DROP CONSTRAINT model_router_request_telemetry_serving_target_check;

ALTER TABLE router.model_router_request_telemetry
    ADD CONSTRAINT model_router_request_telemetry_serving_target_check
        CHECK (serving_target IS NULL OR serving_target IN ('staging', 'prod/stable', 'prod/weave-internal')) NOT VALID;

COMMIT;

BEGIN;

DELETE FROM router.session_release_bindings WHERE target = 'prod/beta';

UPDATE router.model_router_request_telemetry
    SET serving_target = NULL
    WHERE serving_target = 'prod/beta';

ALTER TABLE router.session_release_bindings
    VALIDATE CONSTRAINT session_release_bindings_target_check;

ALTER TABLE router.model_router_request_telemetry
    VALIDATE CONSTRAINT model_router_request_telemetry_serving_target_check;

COMMIT;
