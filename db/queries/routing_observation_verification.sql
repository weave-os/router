-- name: GetRoutingObservationSummary :one
WITH observations AS MATERIALIZED (
    SELECT cluster_ids
    FROM router.model_router_request_telemetry
    WHERE installation_id = @installation_id::uuid
      AND span_type = 'router.upstream'
      AND timestamp >= @from_time::timestamptz
      AND timestamp < @to_time::timestamptz
)
SELECT
    COUNT(*)::bigint AS upstream_rows,
    COUNT(*) FILTER (
        WHERE COALESCE(cardinality(array_remove(observations.cluster_ids, NULL)), 0) = 0
    )::bigint AS rows_without_clusters,
    ARRAY(
        SELECT DISTINCT cluster_id
        FROM observations
        CROSS JOIN LATERAL unnest(observations.cluster_ids) AS clusters(cluster_id)
        WHERE cluster_id IS NOT NULL
        ORDER BY cluster_id
    )::int[] AS cluster_ids
FROM observations;

-- name: CountRoutingObservationModelPicks :many
SELECT COALESCE(decision_model, '')::varchar AS decision_model, COUNT(*)::bigint AS upstream_rows
FROM router.model_router_request_telemetry
WHERE installation_id = @installation_id::uuid
  AND span_type = 'router.upstream'
  AND timestamp >= @from_time::timestamptz
  AND timestamp < @to_time::timestamptz
GROUP BY COALESCE(decision_model, '')
ORDER BY upstream_rows DESC, decision_model;
