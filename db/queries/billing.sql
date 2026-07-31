-- Returns the org's current prepaid balance in USD micros. NULL is mapped to
-- a no-row error so callers can detect missing balance rows and decide
-- explicitly whether 0 or "no row" is the right answer (the middleware
-- treats both as a 402 candidate).
-- name: GetOrgCreditBalance :one
SELECT balance_usd_micros
FROM router.organization_credit_balance
WHERE organization_id = @organization_id::varchar;

-- Reports whether the org currently has an active billing override
-- (free credits, internal account, enterprise trial). Returns the matching
-- row id when present so callers can distinguish "no override" from a real
-- failure path. Uses a one-shot existence query rather than fetching the
-- override body — middleware only needs the boolean.
-- name: GetActiveBillingOverride :one
SELECT EXISTS (
    SELECT 1
    FROM router.organization_billing_overrides
    WHERE organization_id = @organization_id::varchar
      AND (expires_at IS NULL OR expires_at > NOW())
)::boolean AS has_override;

-- Atomic debit: decrement the balance and append a matching ledger row in a
-- single statement. delta_usd_micros is the signed change on the inference row
-- (negative for an inference debit, zero for an override pass-through).
-- charged_usd_micros is the signed total the balance actually moves
-- (delta + fee), pre-combined by the caller. notional_cost_micros
-- is always the would-be charge, populated for both override and real
-- debits so we keep a shadow billing trail.
--
-- No `balance >= amount` guard: concurrent requests can both pass the
-- preflight balance check and both debit; both debits must be recorded
-- even if the resulting balance is briefly negative. The min-balance
-- threshold on the middleware bounds the typical dip.
--
-- Returns the post-debit balance so middleware/log lines can report the
-- new value without a follow-up read.
--
-- When api_key_id is supplied, the same statement also bumps that key's
-- lifetime spent_usd_micros by the charged magnitude (-charged: the real cost
-- on a debit, zero on an override/subscription pass-through), so per-key cap
-- enforcement reads a single up-to-date row. The key_spend CTE is
-- data-modifying, so Postgres runs it to completion even though the final
-- SELECT does not reference it; it no-ops when api_key_id is NULL.
-- When fee_usd_micros is non-zero, a second ledger row of fee_entry_type is
-- written in the same statement. BYOK turns use this: the inference row carries
-- notional upstream cost at delta 0 (the customer paid their own provider) and
-- the fee row holds Weave's platform charge. Both rows must land together or a
-- crash between them would serve inference with no fee, so they share one CTE.
-- name: DebitOrgCredits :one
WITH updated AS (
    UPDATE router.organization_credit_balance
    SET balance_usd_micros = balance_usd_micros + @charged_usd_micros::bigint,
        updated_at = NOW()
    WHERE organization_id = @organization_id::varchar
    RETURNING balance_usd_micros
),
ledger AS (
    INSERT INTO router.organization_credit_ledger (
        organization_id,
        delta_usd_micros,
        notional_cost_micros,
        balance_after_micros,
        entry_type,
        router_request_id,
        router_model
    )
    SELECT
        @organization_id::varchar,
        @delta_usd_micros::bigint,
        @notional_cost_micros::bigint,
        -- The fee row is logically second, so this row records the balance as of
        -- before the fee landed: final minus the fee's signed value.
        updated.balance_usd_micros - @fee_usd_micros::bigint,
        @entry_type::varchar,
        sqlc.narg('router_request_id')::varchar,
        sqlc.narg('router_model')::varchar
    FROM updated
    RETURNING balance_after_micros
),
fee_ledger AS (
    -- Weave's platform fee on a BYOK turn. No-ops when fee_usd_micros is 0,
    -- which is every non-BYOK turn.
    INSERT INTO router.organization_credit_ledger (
        organization_id,
        delta_usd_micros,
        notional_cost_micros,
        balance_after_micros,
        entry_type,
        router_request_id,
        router_model
    )
    SELECT
        @organization_id::varchar,
        @fee_usd_micros::bigint,
        0,
        updated.balance_usd_micros,
        @fee_entry_type::varchar,
        sqlc.narg('router_request_id')::varchar,
        sqlc.narg('router_model')::varchar
    FROM updated
    WHERE @fee_usd_micros::bigint <> 0
),
key_spend AS (
    -- charged_usd_micros is the signed total the balance moved (delta + fee),
    -- pre-combined in Go: sqlc's CTE rewriter only accepts a single param in
    -- this `SET x = x - @p` form, and an unqualified `spent_usd_micros` is
    -- ambiguous against the `updated` CTE. Negative on a real debit, so
    -- subtracting it adds the spend magnitude; zero on override/subscription
    -- pass-throughs leaves it flat. The BYOK fee is included, else a capped key
    -- would never trip its cap on BYOK traffic (delta 0, fee is the whole
    -- charge).
    -- Gated on `updated` producing a row: if the org balance row was missing
    -- (the debit no-ops and the app sees ErrBalanceRowMissing) we must NOT bump
    -- the key's lifetime spend, or a capped key could trip its cap with no
    -- matching ledger debit.
    UPDATE router.model_router_api_keys
    SET spent_usd_micros = spent_usd_micros - @charged_usd_micros::bigint
    WHERE id = sqlc.narg('api_key_id')::uuid
      AND EXISTS (SELECT 1 FROM updated)
),
user_month_spend AS (
    -- Month-bucketed per-engineer spend counter for monthly limit
    -- enforcement. Same gating and sign convention as key_spend; no-ops when
    -- the request carried no resolvable user identity (router_user_id NULL).
    -- Also no-ops when the user row no longer exists (stale cached id after a
    -- cascade delete mid-request) so a dangling FK can't roll back the debit
    -- after inference was already served.
    INSERT INTO router.model_router_user_monthly_spend (router_user_id, month, spent_usd_micros, updated_at)
    SELECT
        sqlc.narg('router_user_id')::uuid,
        DATE_TRUNC('month', NOW() AT TIME ZONE 'utc')::date,
        -(@charged_usd_micros::bigint),
        NOW()
    WHERE sqlc.narg('router_user_id')::uuid IS NOT NULL
      AND EXISTS (SELECT 1 FROM updated)
      AND EXISTS (
          SELECT 1 FROM router.model_router_users
          WHERE id = sqlc.narg('router_user_id')::uuid
      )
    ON CONFLICT (router_user_id, month) DO UPDATE
    SET spent_usd_micros = router.model_router_user_monthly_spend.spent_usd_micros + EXCLUDED.spent_usd_micros,
        updated_at = NOW()
),
org_month_spend AS (
    -- Month-bucketed org spend counter for the org-wide monthly cap.
    INSERT INTO router.organization_monthly_spend (organization_id, month, spent_usd_micros, updated_at)
    SELECT
        @organization_id::varchar,
        DATE_TRUNC('month', NOW() AT TIME ZONE 'utc')::date,
        -(@charged_usd_micros::bigint),
        NOW()
    WHERE EXISTS (SELECT 1 FROM updated)
    ON CONFLICT (organization_id, month) DO UPDATE
    SET spent_usd_micros = router.organization_monthly_spend.spent_usd_micros + EXCLUDED.spent_usd_micros,
        updated_at = NOW()
)
-- Returns the TRUE post-debit balance (from `updated`, after both delta and
-- fee), not a ledger row's balance_after. The inference row deliberately
-- records the pre-fee balance for audit ordering, so returning it here would
-- hand callers a balance the org never actually had -- and
-- maybeSignalRecharge would reconstruct a bogus pre-debit balance from it and
-- miss autopay threshold crossings on BYOK fee turns.
SELECT (SELECT balance_usd_micros FROM updated) AS balance_after_micros
FROM ledger;

-- Paginated read for the dashboard ledger panel. Sorted newest-first so the
-- UI can render without an extra ORDER BY in Go.
-- name: ListOrgLedger :many
SELECT
    id,
    organization_id,
    delta_usd_micros,
    notional_cost_micros,
    balance_after_micros,
    entry_type,
    stripe_payment_intent_id,
    router_request_id,
    router_model,
    created_at
FROM router.organization_credit_ledger
WHERE organization_id = @organization_id::varchar
ORDER BY created_at DESC
LIMIT @row_limit::int;

-- Returns true if every table the billing debit path touches exists in the
-- router schema. Used by the router boot-time health check so a
-- missing-migration state disables billing rather than 500ing on every
-- request. Includes the monthly-spend counter and limit tables because
-- DebitOrgCredits writes the counters in the same statement as the debit.
-- name: CheckBillingTablesExist :one
SELECT (
    SELECT COUNT(*) FROM information_schema.tables
    WHERE table_schema = 'router'
      AND table_name IN (
        'organization_credit_balance',
        'organization_credit_ledger',
        'organization_billing_overrides',
        'model_router_user_monthly_spend',
        'organization_monthly_spend',
        'organization_spend_limits',
        'model_router_user_spend_limits'
      )
) = 7 AS billing_tables_exist;
