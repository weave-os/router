BEGIN;

DROP INDEX router.policy_shadow_decisions_shadow_routing_model_idx;

ALTER TABLE router.policy_shadow_decisions
    DROP COLUMN shadow_roster_arm_id,
    DROP COLUMN serving_roster_arm_id,
    DROP COLUMN shadow_policy_group,
    DROP COLUMN serving_policy_group,
    DROP COLUMN classifier_predicted_label,
    DROP COLUMN shadow_profile_key,
    DROP COLUMN serving_profile_key,
    DROP COLUMN shadow_routing_model;

COMMIT;
