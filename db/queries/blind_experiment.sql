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

-- Replaces an installation's experiment configuration. The control plane
-- performs assignment replacement in the same transaction.
-- name: UpsertBlindRouterExperimentConfiguration :exec
INSERT INTO router.blind_router_experiment_configurations (
    installation_id,
    organization_id,
    enabled,
    router_on_percentage,
    seed
) VALUES (
    @installation_id::uuid,
    @organization_id::varchar,
    @enabled::boolean,
    @router_on_percentage::smallint,
    @seed::uuid
)
ON CONFLICT (installation_id) DO UPDATE SET
    organization_id = EXCLUDED.organization_id,
    enabled = EXCLUDED.enabled,
    router_on_percentage = EXCLUDED.router_on_percentage,
    seed = EXCLUDED.seed,
    updated_at = NOW();

-- Removes materialized assignments before an atomic cohort replacement.
-- name: DeleteBlindRouterExperimentAssignments :exec
DELETE FROM router.blind_router_experiment_assignments
WHERE installation_id = @installation_id::uuid;

-- Inserts one materialized router-user assignment after the control plane has
-- validated tenant ownership and computed the deterministic automatic arm.
-- name: InsertBlindRouterExperimentAssignment :exec
INSERT INTO router.blind_router_experiment_assignments (
    router_user_id,
    installation_id,
    organization_id,
    canonical_subject_key,
    automatic_arm,
    manual_override
) VALUES (
    @router_user_id::uuid,
    @installation_id::uuid,
    @organization_id::varchar,
    @canonical_subject_key::varchar,
    @automatic_arm::varchar,
    sqlc.narg('manual_override')::varchar
);

-- Removes subject overrides before the control plane replaces the complete
-- manager-edited set in the same transaction.
-- name: DeleteBlindRouterExperimentSubjectOverrides :exec
DELETE FROM router.blind_router_experiment_subject_overrides
WHERE installation_id = @installation_id::uuid;

-- Inserts one canonical-subject override. Subject rows outlive router-user
-- links so a not-yet-seen engineer inherits the choice when first linked.
-- name: InsertBlindRouterExperimentSubjectOverride :exec
INSERT INTO router.blind_router_experiment_subject_overrides (
    installation_id,
    organization_id,
    canonical_subject_key,
    manual_override
) VALUES (
    @installation_id::uuid,
    @organization_id::varchar,
    @canonical_subject_key::varchar,
    @manual_override::varchar
);
