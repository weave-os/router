BEGIN;
CREATE TABLE router.internal_test_budgets (
    subject_id uuid PRIMARY KEY REFERENCES router.credential_subjects(id),
    installation_id uuid NOT NULL REFERENCES router.model_router_installations(id),
    label varchar(128) NOT NULL,
    balance_usd_micros bigint NOT NULL,
    enabled boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE router.internal_test_credit_ledger (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    subject_id uuid NOT NULL REFERENCES router.internal_test_budgets(subject_id),
    delta_usd_micros bigint NOT NULL CHECK (delta_usd_micros <= 0),
    router_request_id varchar NOT NULL,
    router_model varchar NOT NULL,
    api_key_id uuid NOT NULL REFERENCES router.model_router_api_keys(id),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX internal_test_credit_ledger_subject ON router.internal_test_credit_ledger(subject_id, created_at);
CREATE TABLE router.internal_test_plan_launches (
    id uuid PRIMARY KEY,
    token_sha256 varchar(64) UNIQUE NOT NULL,
    subject_id uuid NOT NULL REFERENCES router.internal_test_budgets(subject_id),
    installation_id uuid NOT NULL REFERENCES router.model_router_installations(id),
    launch jsonb NOT NULL,
    session_id uuid,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL CHECK (expires_at > created_at AND expires_at <= created_at + interval '1 hour'),
    revoked_at timestamptz
);
COMMIT;
