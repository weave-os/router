-- name: GetBillingAdmission :one
SELECT EXISTS (
    SELECT 1 FROM router.organization_billing_overrides o
    WHERE o.organization_id = @org_id::varchar AND (o.expires_at IS NULL OR o.expires_at > NOW())
) AS has_override,
    (b.organization_id IS NOT NULL)::boolean AS has_balance,
    COALESCE(b.balance_usd_micros, 0)::bigint AS balance,
    (k.id IS NOT NULL)::boolean AS has_key,
    k.spend_cap_usd_micros AS key_cap,
    COALESCE(k.spent_usd_micros, 0)::bigint AS key_spent,
    COALESCE(s.spent_usd_micros, 0)::bigint AS session_spent,
    COALESCE(t.spent_usd_micros, 0)::bigint AS task_spent,
    l.org_monthly_limit_usd_micros AS org_limit,
    COALESCE(m.spent_usd_micros, 0)::bigint AS org_spent
FROM (SELECT 1) AS anchor
LEFT JOIN router.organization_credit_balance b ON b.organization_id = @org_id::varchar
LEFT JOIN router.model_router_api_keys k ON k.id = sqlc.narg(key_id)::uuid AND k.deleted_at IS NULL
LEFT JOIN router.delegated_token_spend s ON s.issuer_key_id = sqlc.narg(issuer_id)::uuid
    AND s.scope_kind = 'session' AND s.scope_id = @session_id::varchar
LEFT JOIN router.delegated_token_spend t ON t.issuer_key_id = sqlc.narg(issuer_id)::uuid
    AND t.scope_kind = 'task' AND t.scope_id = @task_id::varchar
LEFT JOIN router.organization_spend_limits l ON l.organization_id = @org_id::varchar
LEFT JOIN router.organization_monthly_spend m ON m.organization_id = @org_id::varchar
    AND m.month = DATE_TRUNC('month', NOW() AT TIME ZONE 'utc')::date;
