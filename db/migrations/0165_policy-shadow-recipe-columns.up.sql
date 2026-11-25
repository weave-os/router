BEGIN;

-- Recipe shadow: what another routing model's serving profile would have
-- picked from the same classifier facts. Identifiers, model names, arm and
-- label only; never prompt or response content.
ALTER TABLE router.policy_shadow_decisions
    ADD COLUMN shadow_routing_model        VARCHAR(64),
    ADD COLUMN serving_profile_key         VARCHAR(64),
    ADD COLUMN shadow_profile_key          VARCHAR(64),
    ADD COLUMN classifier_predicted_label  VARCHAR(64),
    ADD COLUMN serving_policy_group        VARCHAR(64),
    ADD COLUMN shadow_policy_group         VARCHAR(64),
    ADD COLUMN serving_roster_arm_id       VARCHAR(128),
    ADD COLUMN shadow_roster_arm_id        VARCHAR(128);

CREATE INDEX policy_shadow_decisions_shadow_routing_model_idx
    ON router.policy_shadow_decisions (installation_id, shadow_routing_model, created_at DESC)
    WHERE shadow_routing_model IS NOT NULL;

COMMIT;
