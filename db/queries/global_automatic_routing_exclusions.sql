-- Lists every model disabled for automatic routing across the deployment.
-- Ordered so the request-path cache snapshot and the admin read agree.
-- name: ListGlobalAutomaticRoutingExclusions :many
SELECT model, reason, created_at, created_by
FROM router.global_automatic_routing_exclusions
ORDER BY model;
