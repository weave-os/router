BEGIN;

-- Applied token-efficiency evidence, mirrored by the control plane from its
-- measurement store, so session savings use the same measured work ratios and
-- pass-rate penalty as the dashboard. Empty on deployments without one; the
-- session-cost endpoint then reports no normalized savings.
CREATE TABLE router.token_efficiency_ratios (
    harness        TEXT NOT NULL,
    baseline_model TEXT NOT NULL,
    served_model   TEXT NOT NULL,
    input_point    DOUBLE PRECISION NOT NULL,
    input_lower    DOUBLE PRECISION NOT NULL,
    output_point   DOUBLE PRECISION NOT NULL,
    output_lower   DOUBLE PRECISION NOT NULL,
    native         BOOLEAN NOT NULL,
    valid_from     TIMESTAMPTZ NOT NULL,
    valid_to       TIMESTAMPTZ
);

CREATE INDEX token_efficiency_ratios_lookup_idx
    ON router.token_efficiency_ratios (harness, baseline_model);

CREATE TABLE router.token_efficiency_quality_factors (
    harness                  TEXT NOT NULL,
    baseline_model           TEXT NOT NULL,
    router_pass_rate_lower   DOUBLE PRECISION NOT NULL,
    baseline_pass_rate_upper DOUBLE PRECISION NOT NULL,
    valid_from               TIMESTAMPTZ NOT NULL,
    valid_to                 TIMESTAMPTZ
);

CREATE INDEX token_efficiency_quality_factors_lookup_idx
    ON router.token_efficiency_quality_factors (harness, baseline_model);

-- Switch-turn prefill a session kept on one model would have read from a warm
-- cache; the comparison-model repricing reads it as cache reads.
ALTER TABLE router.model_router_request_telemetry
    ADD COLUMN baseline_warm_prefill_tokens INTEGER;

COMMIT;
