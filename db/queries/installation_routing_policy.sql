-- Absent configuration inherits the installation's existing behavior.
-- name: GetInstallationRoutingPolicy :one
SELECT
    COALESCE(policy.mode, 'inherit')::text AS mode,
    COALESCE(policy.revision, 0)::bigint AS revision,
    COALESCE(snapshot.id, 0)::bigint AS experiment_snapshot_id,
    COALESCE(snapshot.settings->'router_user_ids', '[]'::jsonb)::text AS experiment_router_user_ids
FROM (SELECT @installation_id::uuid AS installation_id) installation
LEFT JOIN router.installation_routing_policies policy USING (installation_id)
LEFT JOIN router.experiment_settings_snapshots snapshot
    ON snapshot.id = policy.experiment_snapshot_id
    AND snapshot.installation_id = policy.installation_id
    AND snapshot.settings->>'source_experiment_id' = policy.reporting_experiment_id::text
    AND snapshot.mode = 'teams'
    AND jsonb_typeof(snapshot.settings->'router_user_ids') = 'array'
    AND snapshot.settings->>'algorithm_version' = '1'
    AND policy.mode = 'assigned'
    AND policy.reporting_updated_at = policy.updated_at;

-- Only assignments from the loaded policy revision may enable automatic routing.
-- name: HasInstallationRoutingAssignment :one
SELECT EXISTS (
    SELECT 1
    FROM router.installation_routing_assignments assignment
    WHERE assignment.installation_id = @installation_id::uuid
      AND assignment.router_user_id = @router_user_id::uuid
      AND assignment.revision = @revision::bigint
) AS router_on;
