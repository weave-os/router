BEGIN;

-- Persist the cache-eviction planner's per-turn verdict onto the existing
-- telemetry row. Today these live only as span attributes / structured logs,
-- so a switch turn loses the pin (decision_model == fresh_decision_model) and
-- over-switching is unrecoverable from Postgres. Nullable throughout: the
-- planner does not run on every path, and a stored zero must not read as
-- evidence that it did.
--
-- The three cost columns are named `_usd_micros` on purpose. The existing
-- `*_cost_usd` columns on this table are bigint micros; that trap has already
-- cost one 1e6-scale misread. These names make the unit unambiguous.
--
-- Do not rebuild production_request_telemetry here. That view was last frozen
-- in 0028; a CREATE VIEW ... SELECT * would pull in columns added by 0033 /
-- 0035 / 0039 and break those migrations' downs (they DROP the column without
-- dropping the view). Planner columns are read from the table.
ALTER TABLE router.model_router_request_telemetry
    ADD COLUMN planner_outcome                      VARCHAR,
    ADD COLUMN planner_reason                       VARCHAR,
    ADD COLUMN planner_pin_model                    VARCHAR,
    ADD COLUMN planner_pin_provider                 VARCHAR,
    ADD COLUMN planner_expected_savings_usd_micros  BIGINT,
    ADD COLUMN planner_eviction_cost_usd_micros     BIGINT,
    ADD COLUMN planner_pin_cache_cold               BOOLEAN,
    ADD COLUMN planner_shadow_outcome               VARCHAR,
    ADD COLUMN planner_shadow_savings_usd_micros    BIGINT;

COMMENT ON COLUMN router.model_router_request_telemetry.planner_outcome IS
    'Planner verdict for this turn: stay or switch. NULL when the planner did not run.';
COMMENT ON COLUMN router.model_router_request_telemetry.planner_reason IS
    'Snake-case planner reason (ev_positive, ev_negative, same_model, no_pin, …). NULL when the planner did not run.';
COMMENT ON COLUMN router.model_router_request_telemetry.planner_pin_model IS
    'Pinned model the planner compared against. On a switch this is the model that was abandoned; decision_model is the one served.';
COMMENT ON COLUMN router.model_router_request_telemetry.planner_pin_provider IS
    'Provider binding of the pin the planner priced. Distinct from decision_provider on a switch.';
COMMENT ON COLUMN router.model_router_request_telemetry.planner_expected_savings_usd_micros IS
    'Planner expected_savings as USD micros (USD × 1e6), not float USD. NULL when the planner did not run.';
COMMENT ON COLUMN router.model_router_request_telemetry.planner_eviction_cost_usd_micros IS
    'Planner eviction_cost as USD micros (USD × 1e6), not float USD. NULL when the planner did not run.';
COMMENT ON COLUMN router.model_router_request_telemetry.planner_pin_cache_cold IS
    'Whether the EV math priced the pin as cache-cold. NULL when the planner did not run.';
COMMENT ON COLUMN router.model_router_request_telemetry.planner_shadow_outcome IS
    'Shadow (corrected-economics) verdict: stay or switch. NULL when the shadow was not computed.';
COMMENT ON COLUMN router.model_router_request_telemetry.planner_shadow_savings_usd_micros IS
    'Shadow expected_savings as USD micros (USD × 1e6). NULL when the shadow was not computed.';

COMMIT;
