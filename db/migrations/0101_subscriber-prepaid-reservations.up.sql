BEGIN;

CREATE TABLE router.subscriber_credit_reservations (
    action_id              VARCHAR(128) PRIMARY KEY,
    subscriber_id          UUID NOT NULL REFERENCES router.credential_subjects(id) ON DELETE CASCADE,
    router_request_id      VARCHAR(64) NOT NULL,
    api_key_id             VARCHAR(255),
    requested_model        VARCHAR(128) NOT NULL,
    reserved_usd_micros    BIGINT NOT NULL CHECK (reserved_usd_micros > 0),
    settled_usd_micros     BIGINT NOT NULL DEFAULT 0 CHECK (settled_usd_micros >= 0),
    state                  VARCHAR(16) NOT NULL DEFAULT 'reserved'
                           CHECK (state IN ('reserved', 'settled', 'released')),
    capacity_source        VARCHAR(32) NOT NULL DEFAULT 'prepaid'
                           CHECK (capacity_source = 'prepaid'),
    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX subscriber_credit_reservations_subscriber_created_idx
    ON router.subscriber_credit_reservations (subscriber_id, created_at DESC);

ALTER TABLE router.subscriber_credit_ledger
    ADD COLUMN authorization_action_id VARCHAR(128)
        REFERENCES router.subscriber_credit_reservations(action_id) ON DELETE SET NULL,
    ADD COLUMN action_id VARCHAR(128),
    ADD COLUMN capacity_source VARCHAR(32);

CREATE UNIQUE INDEX subscriber_credit_ledger_action_id_uidx
    ON router.subscriber_credit_ledger (action_id)
    WHERE action_id IS NOT NULL;

COMMIT;