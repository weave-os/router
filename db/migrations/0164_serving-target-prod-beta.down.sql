-- Restores the constraints without prod/beta, using the same two-step shape as
-- the up migration so the telemetry scan never runs under ACCESS EXCLUSIVE.
--
-- The first step swaps in the narrow constraints as NOT VALID, waiting for its
-- lock at most 250ms per attempt (below the application pool's 1s lock_timeout)
-- and retrying a timed-out attempt up to 40 times inside the migration. From
-- its commit on, no new prod/beta row can be written. The second transaction
-- then removes the prod/beta rows written before that commit and validates.
-- It takes only ROW EXCLUSIVE and SHARE UPDATE EXCLUSIVE, which block neither
-- readers nor writers, so it waits without a bound and cannot be aborted by a
-- lock timeout after the narrowed constraints have committed.
--
-- A session binding pins a conversation to a target's activation, so a
-- prod/beta binding is meaningless once the target is gone: delete it.
-- Telemetry rows are request history, so keep them and clear only the target,
-- matching rows recorded before serving_target existed. The UPDATE scans the
-- table without an index (CREATE INDEX CONCURRENTLY cannot run inside a
-- migration transaction); it runs only on a manual rollback.
BEGIN;

DO $swap$
DECLARE
    attempt integer;
BEGIN
    FOR attempt IN 1..40 LOOP
        BEGIN
            SET LOCAL lock_timeout = '250ms';
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
            RETURN;
        EXCEPTION WHEN lock_not_available THEN
            IF attempt = 40 THEN
                RAISE;
            END IF;
            PERFORM pg_sleep(0.5);
        END;
    END LOOP;
END
$swap$;

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
