BEGIN;

ALTER TABLE router.session_pins
    ADD COLUMN last_completed_request_id VARCHAR NOT NULL DEFAULT '',
    ADD COLUMN last_completed_route_id VARCHAR NOT NULL DEFAULT '',
    ADD COLUMN last_completed_model VARCHAR NOT NULL DEFAULT '',
    ADD COLUMN last_completed_strategy VARCHAR NOT NULL DEFAULT '',
    ADD COLUMN last_completed_at TIMESTAMPTZ;

COMMIT;
