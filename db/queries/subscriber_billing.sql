-- Returns an individual subscriber's current prepaid balance in USD micros.
-- The owner is the credential subject, never an organization id, so a
-- subscriber's funds are unreachable from any other subscriber in the same
-- organization. A missing row surfaces as a no-row error, as with the
-- organization book, so callers decide whether "no row" differs from zero.
-- name: GetSubscriberCreditBalance :one
SELECT balance_usd_micros
FROM router.subscriber_credit_balance
WHERE subscriber_id = @subscriber_id::uuid;

-- Atomic debit against one subscriber's prepaid book: move the balance and
-- append the matching ledger row in a single statement, both keyed by the
-- same subscriber_id so a debit can never land on another owner's balance.
-- delta_usd_micros is the signed change (negative for a real debit, zero for
-- a pass-through), while notional_cost_micros always records the would-be
-- charge for the shadow trail.
--
-- No `balance >= amount` guard, matching DebitOrgCredits: concurrent turns can
-- both clear the preflight check and both must still be recorded.
--
-- Returns the post-debit balance; zero rows means the subscriber had no
-- balance row, which the caller maps to a missing-balance error.
-- name: DebitSubscriberCredits :one
WITH updated AS (
    UPDATE router.subscriber_credit_balance
    SET balance_usd_micros = balance_usd_micros + @delta_usd_micros::bigint,
        updated_at = NOW()
    WHERE subscriber_id = @subscriber_id::uuid
    RETURNING balance_usd_micros
)
INSERT INTO router.subscriber_credit_ledger (
    subscriber_id,
    delta_usd_micros,
    notional_cost_micros,
    balance_after_micros,
    entry_type,
    router_request_id,
    router_model
)
SELECT
    @subscriber_id::uuid,
    @delta_usd_micros::bigint,
    @notional_cost_micros::bigint,
    updated.balance_usd_micros,
    @entry_type::varchar,
    sqlc.narg('router_request_id')::varchar,
    sqlc.narg('router_model')::varchar
FROM updated
RETURNING balance_after_micros;
