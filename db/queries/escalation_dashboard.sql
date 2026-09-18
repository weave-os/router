-- name: DeleteExpiredEscalationDashboardSnapshots :exec
-- Removes expired materialized dashboard pages before creating a replacement.
DELETE FROM router.escalation_dashboard_snapshots
WHERE expires_at <= @read_at::timestamptz;

-- name: CreateEscalationDashboardSnapshot :one
-- Materializes a newest-first retained-session cohort and returns its first page.
WITH xgb_evaluations AS MATERIALIZED (
    SELECT
        encode(c.scope, 'hex') AS session_id,
        (c.checkpoint->>'ordinal')::integer AS progress,
        (c.checkpoint->'prediction'->>'score') IS NOT NULL AS valid,
        COALESCE((c.checkpoint->'prediction'->>'escalate')::boolean, false) AS recommended,
        COALESCE(c.checkpoint->'decision'->>'outcome' = 'promoted'
            AND (c.checkpoint->'decision'->>'constrained')::boolean, false) AS applied,
        COALESCE(c.checkpoint->'decision'->>'outcome' = 'floor_applied'
            AND (c.checkpoint->'decision'->>'constrained')::boolean, false) AS constrained,
        false AS invalid
    FROM router.escalation_checkpoints c
    JOIN router.escalation_sessions s ON s.scope = c.scope
    WHERE s.expires_at > @captured_at::timestamptz
), xgb_sessions AS MATERIALIZED (
    SELECT
        encode(s.scope, 'hex') AS session_id,
        encode(s.scope, 'hex') AS scope,
        ''::text AS lifetime,
        i.external_id::text AS organization_id,
        s.installation_id::text AS installation_id,
        'xgb'::text AS service,
        CASE WHEN s.session_state->>'mode' IN ('active', 'shadow')
            THEN s.session_state->>'mode' ELSE 'unknown' END AS mode,
        CASE WHEN s.session_state ? 'epoch' THEN (s.session_state->>'epoch')::integer END AS epoch,
        s.ordinal::integer AS observed_progress,
        count(*) FILTER (WHERE e.valid)::integer AS evaluations,
        count(*) FILTER (WHERE e.valid AND e.recommended)::integer AS recommendations,
        min(e.progress) FILTER (WHERE e.valid AND e.recommended)::integer AS first_recommendation,
        count(*) FILTER (WHERE e.applied)::integer AS escalations_applied,
        count(*) FILTER (WHERE e.constrained)::integer AS floor_constrained_requests,
        0::integer AS invalid_evaluations,
        NULLIF(s.session_state->>'floor', '')::text AS floor,
        s.expires_at - interval '24 hours' AS last_activity_at,
        s.continuity_broken
    FROM router.escalation_sessions s
    JOIN router.model_router_installations i ON i.id = s.installation_id AND i.deleted_at IS NULL
    LEFT JOIN xgb_evaluations e ON e.session_id = encode(s.scope, 'hex')
    WHERE s.expires_at > @captured_at::timestamptz AND s.ordinal > 0
    GROUP BY s.scope, i.external_id
), switchyard_evaluations AS MATERIALIZED (
    SELECT
        s.lifetime::text AS session_id,
        (j.job->>'checkpoint')::integer AS progress,
        j.status IN ('completed', 'applied') AND j.job->'judgment' IS NOT NULL AS valid,
        j.status IN ('completed', 'applied') AND j.job->'judgment' IS NOT NULL
            AND COALESCE((j.job->'judgment'->>'escalate')::boolean, false) AS recommended,
        j.status = 'applied' AND s.state->'config'->>'mode' = 'active' AS applied,
        false AS constrained,
        j.status IN ('failed', 'skipped', 'stale') AS invalid
    FROM router.llm_escalation_jobs j
    JOIN router.llm_escalation_sessions s ON s.lifetime = j.lifetime
    WHERE s.expires_at > @captured_at::timestamptz
), switchyard_sessions AS MATERIALIZED (
    SELECT
        s.lifetime::text AS session_id,
        encode(s.scope, 'hex') AS scope,
        s.lifetime::text AS lifetime,
        i.external_id::text AS organization_id,
        s.installation_id::text AS installation_id,
        'switchyard_llm_v1'::text AS service,
        CASE WHEN s.state->'config'->>'mode' IN ('active', 'shadow')
            THEN s.state->'config'->>'mode' ELSE 'unknown' END AS mode,
        CASE WHEN s.state->'config' ? 'epoch' THEN (s.state->'config'->>'epoch')::integer END AS epoch,
        COALESCE((s.state->>'completed_turns')::integer, 0) AS observed_progress,
        count(*) FILTER (WHERE e.valid)::integer AS evaluations,
        count(*) FILTER (WHERE e.valid AND e.recommended)::integer AS recommendations,
        min(e.progress) FILTER (WHERE e.valid AND e.recommended)::integer AS first_recommendation,
        count(*) FILTER (WHERE e.applied)::integer AS escalations_applied,
        0::integer AS floor_constrained_requests,
        count(*) FILTER (WHERE e.invalid)::integer AS invalid_evaluations,
        NULLIF(s.state->>'floor', '')::text AS floor,
        COALESCE((s.state->>'last_activity_at')::timestamptz, s.updated_at) AS last_activity_at,
        false AS continuity_broken
    FROM router.llm_escalation_sessions s
    JOIN router.model_router_installations i ON i.id = s.installation_id AND i.deleted_at IS NULL
    LEFT JOIN switchyard_evaluations e ON e.session_id = s.lifetime::text
    WHERE s.expires_at > @captured_at::timestamptz
    GROUP BY s.scope, s.lifetime, i.external_id
), all_sessions AS MATERIALIZED (
    SELECT * FROM xgb_sessions
    UNION ALL
    SELECT * FROM switchyard_sessions
), selected_sessions AS MATERIALIZED (
    SELECT * FROM all_sessions
    WHERE (@service::text = '' OR service = @service::text)
        AND (@mode::text = '' OR mode = @mode::text)
        AND (@organization_id::text = '' OR organization_id = @organization_id::text)
        AND (@installation_id::text = '' OR installation_id = @installation_id::text)
), all_evaluations AS MATERIALIZED (
    SELECT 'xgb'::text AS service, e.*, s.mode
    FROM xgb_evaluations e JOIN xgb_sessions s USING (session_id)
    UNION ALL
    SELECT 'switchyard_llm_v1'::text AS service, e.*, s.mode
    FROM switchyard_evaluations e JOIN switchyard_sessions s USING (session_id)
), selected_evaluations AS MATERIALIZED (
    SELECT e.* FROM all_evaluations e JOIN selected_sessions s USING (session_id, service, mode)
), matching_sessions AS MATERIALIZED (
    SELECT * FROM selected_sessions
    WHERE @session_outcome::text = ''
        OR (@session_outcome::text = 'recommended' AND recommendations > 0)
        OR (@session_outcome::text = 'applied' AND escalations_applied > 0)
        OR (@session_outcome::text = 'shadow_recommendation' AND mode = 'shadow' AND recommendations > 0)
        OR (@session_outcome::text = 'no_evaluation' AND evaluations = 0)
), ordered_sessions AS MATERIALIZED (
    SELECT
        matching_sessions.*,
        service || ':' || session_id AS id,
        (row_number() OVER (ORDER BY last_activity_at DESC, service, session_id) - 1)::integer AS position
    FROM matching_sessions
), summary AS (
    SELECT
        count(*)::integer AS observed_sessions,
        count(*) FILTER (WHERE evaluations > 0)::integer AS evaluated_sessions,
        count(*) FILTER (WHERE recommendations > 0)::integer AS recommended_sessions,
        COALESCE(sum(evaluations), 0)::integer AS evaluations,
        COALESCE(sum(recommendations), 0)::integer AS recommendations,
        COALESCE(sum(escalations_applied), 0)::integer AS escalations_applied,
        COALESCE(sum(recommendations) FILTER (WHERE mode = 'shadow'), 0)::integer AS shadow_recommendations,
        COALESCE(sum(floor_constrained_requests), 0)::integer AS floor_constrained_requests,
        COALESCE(sum(invalid_evaluations), 0)::integer AS invalid_evaluations
    FROM selected_sessions
), outcome_breakdown AS (
    SELECT service, mode, sum(evaluations)::integer AS evaluations,
        sum(recommendations)::integer AS recommendations,
        sum(invalid_evaluations)::integer AS invalid
    FROM selected_sessions GROUP BY service, mode
), progress_distribution AS (
    SELECT service, mode, ((progress - 1) / 10) * 10 + 1 AS first_progress,
        count(*) FILTER (WHERE valid)::integer AS evaluations,
        count(*) FILTER (WHERE valid AND recommended)::integer AS recommendations
    FROM selected_evaluations
    WHERE valid
    GROUP BY service, mode, first_progress
), first_recommendation_distribution AS (
    SELECT service, mode, ((first_recommendation - 1) / 10) * 10 + 1 AS first_progress,
        count(*)::integer AS sessions
    FROM selected_sessions WHERE first_recommendation IS NOT NULL
    GROUP BY service, mode, first_progress
), floor_distribution AS (
    SELECT service, mode, floor, count(*)::integer AS sessions
    FROM selected_sessions GROUP BY service, mode, floor
), organization_breakdown AS (
    SELECT organization_id, installation_id, service, mode,
        count(*)::integer AS observed_sessions,
        count(*) FILTER (WHERE evaluations > 0)::integer AS evaluated_sessions,
        count(*) FILTER (WHERE recommendations > 0)::integer AS recommended_sessions,
        sum(evaluations)::integer AS evaluations,
        sum(recommendations)::integer AS recommendations,
        sum(escalations_applied)::integer AS escalations_applied,
        COALESCE(sum(recommendations) FILTER (WHERE mode = 'shadow'), 0)::integer AS shadow_recommendations
    FROM selected_sessions
    GROUP BY organization_id, installation_id, service, mode
), snapshot_insert AS (
    INSERT INTO router.escalation_dashboard_snapshots (
        captured_at,
        expires_at,
        summary,
        outcome_breakdown,
        progress_distribution,
        first_recommendation_distribution,
        floor_distribution,
        organizations,
        matching_sessions
    )
    SELECT
        @captured_at::timestamptz,
        @snapshot_expires_at::timestamptz,
        (SELECT to_jsonb(summary) FROM summary),
        (SELECT COALESCE(jsonb_agg(to_jsonb(o) ORDER BY o.service, o.mode), '[]'::jsonb) FROM outcome_breakdown o),
        (SELECT COALESCE(jsonb_agg(to_jsonb(p) ORDER BY p.service, p.mode, p.first_progress), '[]'::jsonb) FROM progress_distribution p),
        (SELECT COALESCE(jsonb_agg(to_jsonb(f) ORDER BY f.service, f.mode, f.first_progress), '[]'::jsonb) FROM first_recommendation_distribution f),
        (SELECT COALESCE(jsonb_agg(to_jsonb(f) ORDER BY f.service, f.mode, f.floor), '[]'::jsonb) FROM floor_distribution f),
        (SELECT COALESCE(jsonb_agg(to_jsonb(o) ORDER BY o.recommended_sessions DESC, o.organization_id, o.installation_id, o.service, o.mode), '[]'::jsonb) FROM organization_breakdown o),
        (SELECT count(*)::integer FROM matching_sessions)
    RETURNING *
), session_insert AS (
    INSERT INTO router.escalation_dashboard_snapshot_sessions (snapshot_id, position, session)
    SELECT snapshot_insert.id, ordered_sessions.position, to_jsonb(ordered_sessions) - 'position'
    FROM snapshot_insert CROSS JOIN ordered_sessions
    RETURNING position, session
)
SELECT jsonb_build_object(
    'snapshot_id', snapshot_insert.id::text,
    'captured_at', snapshot_insert.captured_at,
    'summary', snapshot_insert.summary,
    'outcome_breakdown', snapshot_insert.outcome_breakdown,
    'progress_distribution', snapshot_insert.progress_distribution,
    'first_recommendation_distribution', snapshot_insert.first_recommendation_distribution,
    'floor_distribution', snapshot_insert.floor_distribution,
    'organizations', snapshot_insert.organizations,
    'sessions', (
        SELECT COALESCE(jsonb_agg(page.session ORDER BY page.position), '[]'::jsonb)
        FROM (
            SELECT position, session FROM session_insert
            ORDER BY position LIMIT @page_limit::integer
        ) page
    ),
    'matching_sessions', snapshot_insert.matching_sessions
)
FROM snapshot_insert;

-- name: GetEscalationDashboardSnapshotPage :one
-- Returns one page from an unexpired immutable dashboard snapshot.
SELECT jsonb_build_object(
    'snapshot_id', snapshot.id::text,
    'captured_at', snapshot.captured_at,
    'summary', snapshot.summary,
    'outcome_breakdown', snapshot.outcome_breakdown,
    'progress_distribution', snapshot.progress_distribution,
    'first_recommendation_distribution', snapshot.first_recommendation_distribution,
    'floor_distribution', snapshot.floor_distribution,
    'organizations', snapshot.organizations,
    'sessions', (
        SELECT COALESCE(jsonb_agg(page.session ORDER BY page.position), '[]'::jsonb)
        FROM (
            SELECT stored.position, stored.session
            FROM router.escalation_dashboard_snapshot_sessions stored
            WHERE stored.snapshot_id = snapshot.id
                AND stored.position >= @page_start::integer
            ORDER BY stored.position
            LIMIT @page_limit::integer
        ) page
    ),
    'matching_sessions', snapshot.matching_sessions
)
FROM router.escalation_dashboard_snapshots snapshot
WHERE snapshot.id = @snapshot_id::uuid
    AND snapshot.expires_at > @read_at::timestamptz;
