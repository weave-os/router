BEGIN;

-- Shadow-only instrumentation for the authoritative-per-turn path.
--
-- Production runs an HMM sidecar that declares authoritative_per_turn_selection,
-- so main_loop and tool_result turns return from the turn loop before
-- hmmCostGatedDecision runs. Migration 0066 proved that empirically: the
-- planner_* columns are NULL on ~100% of production turns. The cache-economics
-- gate is not absent from the codebase, it is simply unreachable on the two
-- turn types that carry the traffic.
--
-- These columns record what that gate WOULD have decided, on every eligible
-- authoritative turn, without changing what is served. They exist so the
-- decision to make the gate reachable is made from production data instead of
-- from a replay against a code path production does not execute.
--
-- Deliberately NOT named planner_*: the planner did not run on these turns and
-- a shared prefix would let a shadow verdict be aggregated as if it were a
-- served decision. Join on pin_tier to partition by which authoritative exit
-- the turn actually took ('authoritative_per_turn' means fresh was served, so
-- the shadow is actionable; the *_sticky and *_confidence_low tiers mean the
-- pin was already kept and the shadow is moot).
--
-- Costs are `_usd_micros` for the same reason as 0066: the pre-existing
-- `*_cost_usd` columns on this table are already bigint micros. Values are
-- signed -- a stay verdict routinely carries negative expected savings, and
-- clamping it to zero destroys the measurement.
ALTER TABLE router.model_router_request_telemetry
    ADD COLUMN authority_shadow_outcome                   VARCHAR,
    ADD COLUMN authority_shadow_would_diverge              BOOLEAN,
    ADD COLUMN authority_shadow_reason                    VARCHAR,
    ADD COLUMN authority_shadow_stay_model                VARCHAR,
    ADD COLUMN authority_shadow_stay_provider             VARCHAR,
    ADD COLUMN authority_shadow_savings_usd_micros        BIGINT,
    ADD COLUMN authority_shadow_eviction_cost_usd_micros  BIGINT,
    ADD COLUMN authority_shadow_pin_cache_cold            BOOLEAN,
    ADD COLUMN authority_shadow_corrected_outcome         VARCHAR,
    ADD COLUMN authority_shadow_corrected_savings_usd_micros BIGINT,
    ADD COLUMN authority_shadow_stay_score                DOUBLE PRECISION,
    ADD COLUMN authority_shadow_fresh_score               DOUBLE PRECISION;

COMMENT ON COLUMN router.model_router_request_telemetry.authority_shadow_outcome IS
    'Shadow verdict of the HMM cache gate on an authoritative turn: stay or switch. NEVER what was served -- authoritative turns always serve decision_model. NULL when the shadow did not run.';
COMMENT ON COLUMN router.model_router_request_telemetry.authority_shadow_would_diverge IS
    'The gate''s own verdict that it would have served authority_shadow_stay_model instead of decision_model. Use this, not a string compare: stay_model is a serving identity that may carry '':effort'' while decision_model is a bare catalog ID.';
COMMENT ON COLUMN router.model_router_request_telemetry.authority_shadow_reason IS
    'Snake-case reason from the shadow gate (ev_positive, ev_negative, same_model, no_pin, no_prior_usage, hmm_upgrade_confidence_low, ...). Read no_pin carefully: it also covers a pin that exists but whose serving identity carries '':effort'', because catalog.ByID strips a date suffix and not an effort suffix, so normalizeHMMStayPin rejects it. no_pin is therefore NOT the same as ''this session had no pin''.';
COMMENT ON COLUMN router.model_router_request_telemetry.authority_shadow_stay_model IS
    'Pin the shadow gate priced against, as a serving identity -- it carries '':effort'' when the pin used one, unlike the bare decision_model. Compare via authority_shadow_would_diverge rather than against decision_model directly.';
COMMENT ON COLUMN router.model_router_request_telemetry.authority_shadow_stay_provider IS
    'Provider binding of authority_shadow_stay_model.';
COMMENT ON COLUMN router.model_router_request_telemetry.authority_shadow_savings_usd_micros IS
    'Signed expected savings as USD micros (USD x 1e6) under the deployed economics config. Negative on a typical stay; not clamped. NULL on an early exit (no_pin, no_prior_usage, same_model, pricing_missing) where the cost arithmetic never ran -- a stored 0 there would be a fabricated measurement.';
COMMENT ON COLUMN router.model_router_request_telemetry.authority_shadow_eviction_cost_usd_micros IS
    'Signed eviction cost as USD micros (USD x 1e6) under the deployed economics config. NULL on an early exit, like the savings column.';
COMMENT ON COLUMN router.model_router_request_telemetry.authority_shadow_pin_cache_cold IS
    'Whether the shadow EV math priced the pin as cache-cold. NULL on an early exit, where the flag is meaningless rather than false.';
COMMENT ON COLUMN router.model_router_request_telemetry.authority_shadow_corrected_outcome IS
    'Verdict under corrected cache-aware economics, computed by planner.Decide as its own shadow on every EV turn regardless of the deployed config. Pre-gate: the upgrade-confidence and same-tier overrides are NOT applied to it, unlike authority_shadow_outcome. NULL on an early exit -- the enum zero value renders as ''stay'', so an uncomputed verdict must never be stored.';
COMMENT ON COLUMN router.model_router_request_telemetry.authority_shadow_corrected_savings_usd_micros IS
    'Signed expected savings under corrected economics as USD micros (USD x 1e6).';
COMMENT ON COLUMN router.model_router_request_telemetry.authority_shadow_stay_score IS
    'Sidecar candidate score for authority_shadow_stay_model this turn. NULL when the sidecar reported no score for the pin -- that NULL rate is the measurement that decides whether a quality tie-band is implementable at all.';
COMMENT ON COLUMN router.model_router_request_telemetry.authority_shadow_fresh_score IS
    'Sidecar candidate score for the served model this turn, paired with authority_shadow_stay_score.';

COMMIT;
