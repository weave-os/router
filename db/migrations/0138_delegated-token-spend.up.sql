BEGIN;

-- Lifetime spend of delegated session tokens, bumped in the same statement as
-- the org debit (like model_router_api_keys.spent_usd_micros) so a cap check
-- reads one indexed row. scope_kind 'session' is keyed by the token's sub,
-- 'task' by its task claim; both are scoped to the issuer that signed them.
CREATE TABLE router.delegated_token_spend (
    issuer_key_id    UUID NOT NULL
        REFERENCES router.model_router_api_keys (id) ON DELETE CASCADE,
    scope_kind       VARCHAR(16) NOT NULL CHECK (scope_kind IN ('session', 'task')),
    scope_id         VARCHAR(255) NOT NULL,
    spent_usd_micros BIGINT NOT NULL DEFAULT 0,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (issuer_key_id, scope_kind, scope_id)
);

COMMIT;
