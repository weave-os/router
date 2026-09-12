BEGIN;

ALTER TABLE router.session_pins
    DROP COLUMN last_completed_at,
    DROP COLUMN last_completed_strategy,
    DROP COLUMN last_completed_model,
    DROP COLUMN last_completed_route_id,
    DROP COLUMN last_completed_request_id;

COMMIT;
