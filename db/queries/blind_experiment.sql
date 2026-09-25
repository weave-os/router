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
    assignment.manual_override,
    COALESCE(configuration.cohort_experiment_id::text, '')::text AS cohort_experiment_id,
    COALESCE(configuration.cohort_starts_at, 'epoch'::timestamptz)::timestamptz AS cohort_starts_at,
    COALESCE(configuration.cohort_ends_at, 'epoch'::timestamptz)::timestamptz AS cohort_ends_at,
    COALESCE(configuration.cohort_revision, 0)::integer AS cohort_revision,
    COALESCE(membership.group_id, 0)::smallint AS cohort_group_id,
    COALESCE((
        SELECT jsonb_agg(jsonb_build_object(
            'phase_index', phase.phase_index,
            'starts_at', phase.starts_at,
            'ends_at', phase.ends_at,
            'arm', phase.arm
        ) ORDER BY phase.starts_at)
        FROM router.blind_router_experiment_schedule phase
        WHERE phase.installation_id = router_user.installation_id
          AND phase.experiment_id = configuration.cohort_experiment_id
          AND phase.revision = configuration.cohort_revision
          AND phase.group_id = membership.group_id
    ), '[]'::jsonb)::text AS cohort_schedule,
    COALESCE((
        SELECT jsonb_agg(jsonb_build_object(
            'starts_at', emergency.starts_at,
            'ends_at', LEAST(emergency.ends_at, COALESCE(emergency.revoked_at, emergency.ends_at)),
            'arm', emergency.arm
        ) ORDER BY emergency.starts_at)
        FROM router.blind_router_experiment_emergency_overrides emergency
        WHERE emergency.installation_id = router_user.installation_id
          AND emergency.experiment_id = configuration.cohort_experiment_id
          AND emergency.canonical_subject_key = membership.canonical_subject_key
    ), '[]'::jsonb)::text AS cohort_overrides
FROM router.model_router_users router_user
LEFT JOIN router.blind_router_experiment_configurations configuration
    ON configuration.installation_id = router_user.installation_id
LEFT JOIN router.blind_router_experiment_assignments assignment
    ON assignment.router_user_id = router_user.id
    AND assignment.installation_id = router_user.installation_id
LEFT JOIN router.blind_router_experiment_group_memberships membership
    ON membership.installation_id = router_user.installation_id
    AND membership.experiment_id = configuration.cohort_experiment_id
    AND membership.canonical_subject_key = assignment.canonical_subject_key
WHERE router_user.id = @router_user_id::uuid
  AND router_user.installation_id = @installation_id::uuid
  AND router_user.deleted_at IS NULL;
