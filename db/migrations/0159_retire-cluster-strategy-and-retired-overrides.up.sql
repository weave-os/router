BEGIN;

DELETE FROM router.session_pins
WHERE lower(btrim(routing_strategy, E' \t\n\r\f\x0B')) = 'cluster' OR decision_reason = 'loop_escalation';

UPDATE router.model_router_installations SET routing_strategy = NULL WHERE lower(btrim(routing_strategy, E' \t\n\r\f\x0B')) = 'cluster';

UPDATE router.model_router_installations SET policy_shadow_strategy = NULL WHERE lower(btrim(policy_shadow_strategy, E' \t\n\r\f\x0B')) = 'cluster';

UPDATE router.model_router_installations
SET flag_overrides = flag_overrides - ARRAY['struggle_escalation_enabled','struggle_escalation_holdout_pct','struggle_evidence_arming','subscriber_paid_fallback_enabled','subscription_pool_quota_aware_selection_enabled','embed_only_user_message','authoritative_upgrade_holdout_pct','hmm_same_tier_pin','planner_cold_pin_follow_fresh','installation_pool_allowance','struggle_shadow_enabled','spiral_shadow_enabled','loop_escalation_enabled','loop_escalation_holdout_pct']::text[]
WHERE flag_overrides ?| ARRAY['struggle_escalation_enabled','struggle_escalation_holdout_pct','struggle_evidence_arming','subscriber_paid_fallback_enabled','subscription_pool_quota_aware_selection_enabled','embed_only_user_message','authoritative_upgrade_holdout_pct','hmm_same_tier_pin','planner_cold_pin_follow_fresh','installation_pool_allowance','struggle_shadow_enabled','spiral_shadow_enabled','loop_escalation_enabled','loop_escalation_holdout_pct']::text[];

COMMIT;
