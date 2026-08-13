BEGIN;

-- Records the policy-internal cluster/group the pinned decision was drawn from
-- (the HMM complexity cluster), copied from RoutingMetadata.PolicyGroup. It is
-- refreshed on a genuine policy re-route and preserved on a same-model sticky
-- refresh, mirroring the paired_provider/paired_model maintenance below it.
--
-- The arm-selector-unavailable pin-sticky guard compares this against the fresh
-- decision's group: the legacy pairwise bandit only draws within one cluster, so
-- a matching group means the reroute is fallback noise the pin may suppress,
-- while a differing group is a real classifier escalation that must switch
-- through. Empty string for pins written outside a policy router (force-model,
-- loop-break, cluster/planner routing) or by sidecars that report no group.
ALTER TABLE router.session_pins
    ADD COLUMN policy_group VARCHAR(64) NOT NULL DEFAULT '';

COMMIT;
