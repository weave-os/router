-- Absent configuration inherits the installation's existing behavior.
-- name: GetInstallationRoutingPolicy :one
SELECT
    COALESCE(policy.mode, 'inherit')::text AS mode,
    COALESCE(policy.revision, 0)::bigint AS revision
FROM (SELECT @installation_id::uuid AS installation_id) installation
LEFT JOIN router.installation_routing_policies policy USING (installation_id);

-- Only assignments from the loaded policy revision may enable automatic routing.
-- name: HasInstallationRoutingAssignment :one
SELECT EXISTS (
    SELECT 1
    FROM router.installation_routing_assignments assignment
    WHERE assignment.installation_id = @installation_id::uuid
      AND assignment.router_user_id = @router_user_id::uuid
      AND assignment.revision = @revision::bigint
) AS router_on;
