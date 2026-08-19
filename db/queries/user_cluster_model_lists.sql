-- Returns every configured per-cluster model selection for one router user.
-- Writes are owned by the Weave control plane (direct inserts to this schema,
-- mirroring cluster_model_lists); the router only reads on the auth path.
-- name: GetUserClusterModelListsByUser :many
SELECT *
FROM router.model_router_user_cluster_model_lists
WHERE router_user_id = @router_user_id::uuid
ORDER BY cluster_label;
