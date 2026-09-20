BEGIN;

CREATE TABLE router.subscriber_autopay_config (
    subscriber_id                      UUID        PRIMARY KEY REFERENCES router.credential_subjects(id) ON DELETE CASCADE,
    enabled                            BOOLEAN     NOT NULL DEFAULT false,
    threshold_usd_micros               BIGINT      NOT NULL CHECK (threshold_usd_micros > 0),
    recharge_usd_micros                BIGINT      NOT NULL CHECK (recharge_usd_micros > 0),
    has_payment_method                 BOOLEAN     NOT NULL DEFAULT false,
    state                              VARCHAR(32) NOT NULL DEFAULT 'active'
        CHECK (state IN ('active','recharging','failing','disabled')),
    consecutive_failures               INT         NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
    checkout_id                        UUID,
    last_attempt_id                    UUID,
    last_attempt_at                    TIMESTAMPTZ,
    last_success_at                    TIMESTAMPTZ,
    cooldown_until                     TIMESTAMPTZ,
    monthly_recharge_cap_usd_micros    BIGINT CHECK (monthly_recharge_cap_usd_micros > 0),
    recharged_month                    DATE,
    recharged_month_usd_micros         BIGINT      NOT NULL DEFAULT 0 CHECK (recharged_month_usd_micros >= 0),
    created_by                         VARCHAR(36),
    created_at                         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at                         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (threshold_usd_micros <= recharge_usd_micros)
);

CREATE INDEX subscriber_autopay_config_enabled_cooldown_idx
    ON router.subscriber_autopay_config (cooldown_until)
    WHERE enabled;

COMMIT;