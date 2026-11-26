-- Admits the prod/beta serving target in the two tables that persist a target.
--
-- model_router_request_telemetry is large and written on every turn. Re-adding
-- a CHECK normally scans every row under ACCESS EXCLUSIVE, which would stall
-- those writes. So the first step swaps each constraint for its NOT VALID form,
-- which needs that lock only briefly and does not scan. The second transaction
-- validates under SHARE UPDATE EXCLUSIVE, which lets reads and writes continue
-- during the scan. Validation cannot fail: the new set is a superset of the
-- old one, which every existing row already satisfies.
--
-- The swap waits for its lock at most 250ms per attempt, below the application
-- pool's 1s lock_timeout. A timed-out attempt is retried at most 5 times, with
-- jittered backoff, inside a 10s total deadline; after that the migration fails
-- fast. A failed run leaves version 164 dirty: run `migrate force 163` and
-- retry once the table is quiet. The validation step takes no lock that blocks
-- readers or writers, so it is bounded only by statement_timeout.
BEGIN;

DO $swap$
DECLARE
    attempt integer;
    deadline timestamptz := clock_timestamp() + interval '10 seconds';
BEGIN
    FOR attempt IN 1..5 LOOP
        BEGIN
            SET LOCAL lock_timeout = '250ms';
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
            RETURN;
        EXCEPTION WHEN lock_not_available THEN
            IF attempt = 5 OR clock_timestamp() + interval '2.25 seconds' > deadline THEN
                RAISE;
            END IF;
            PERFORM pg_sleep(least(0.25 * power(2, attempt - 1), 2) * (0.5 + random() / 2));
        END;
    END LOOP;
END
$swap$;

COMMIT;

BEGIN;

SET LOCAL statement_timeout = '15min';

ALTER TABLE router.session_release_bindings
    VALIDATE CONSTRAINT session_release_bindings_target_check;

ALTER TABLE router.model_router_request_telemetry
    VALIDATE CONSTRAINT model_router_request_telemetry_serving_target_check;

COMMIT;
