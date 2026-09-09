-- Loads the installation experiment and the materialized assignment for one
-- router user. A missing configuration is returned as configured=false so the
-- authentication path can fail open without treating absence as a DB error.
-- name: GetBlindRouterExperimentForUser :one
SELECT
    (configuration.installation_id IS NOT NULL)::boolean AS configured,
    COALESCE(configuration.enabled, FALSE)::boolean AS enabled,
    COALESCE(configuration.router_on_percentage, 100)::smallint AS router_on_percentage,
    COALESCE(configuration.seed::text, '')::text AS seed,
    assignment.canonical_subject_key,
    assignment.automatic_arm,
    assignment.manual_override
FROM router.model_router_users router_user
LEFT JOIN router.blind_router_experiment_configurations configuration
    ON configuration.installation_id = router_user.installation_id
LEFT JOIN router.blind_router_experiment_assignments assignment
    ON assignment.router_user_id = router_user.id
    AND assignment.installation_id = router_user.installation_id
WHERE router_user.id = @router_user_id::uuid
  AND router_user.installation_id = @installation_id::uuid
  AND router_user.deleted_at IS NULL;
