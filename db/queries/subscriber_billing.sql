-- Returns an individual subscriber's current prepaid balance in USD micros.
-- The owner is the credential subject, never an organization id, so a
-- subscriber's funds are unreachable from any other subscriber in the same
-- organization. A missing row surfaces as a no-row error, as with the
-- organization book, so callers decide whether "no row" differs from zero.
-- name: GetSubscriberCreditBalance :one
SELECT balance_usd_micros
FROM router.subscriber_credit_balance
WHERE subscriber_id = @subscriber_id::uuid;

-- Locks one subscriber's prepaid balance while an authorization is created or
-- finalized, serializing concurrent reservations for that owner.
-- name: GetSubscriberCreditBalanceForUpdate :one
SELECT balance_usd_micros
FROM router.subscriber_credit_balance
WHERE subscriber_id = @subscriber_id::uuid
FOR UPDATE;

-- Applies a signed balance adjustment while the caller holds the subscriber
-- balance lock.
-- name: UpdateSubscriberCreditBalance :one
UPDATE router.subscriber_credit_balance
SET balance_usd_micros = balance_usd_micros + @delta_usd_micros::bigint,
    updated_at = NOW()
WHERE subscriber_id = @subscriber_id::uuid
RETURNING balance_usd_micros;

-- Returns a durable subscriber prepaid authorization.
-- name: GetSubscriberCreditReservation :one
SELECT *
FROM router.subscriber_credit_reservations
WHERE action_id = @action_id::varchar;

-- Locks a durable subscriber prepaid authorization for settlement/finalization.
-- name: GetSubscriberCreditReservationForUpdate :one
SELECT *
FROM router.subscriber_credit_reservations
WHERE action_id = @action_id::varchar
FOR UPDATE;

-- Inserts the hold that authorizes one request to dispatch against subscriber funds.
-- name: InsertSubscriberCreditReservation :one
INSERT INTO router.subscriber_credit_reservations (
    action_id,
    subscriber_id,
    router_request_id,
    api_key_id,
    requested_model,
    reserved_usd_micros,
    capacity_source
)
VALUES (
    @action_id::varchar,
    @subscriber_id::uuid,
    @router_request_id::varchar,
    sqlc.narg('api_key_id')::varchar,
    @requested_model::varchar,
    @reserved_usd_micros::bigint,
    @capacity_source::varchar
)
RETURNING *;

-- Adds one exact served action to a still-open prepaid authorization.
-- name: AddSubscriberCreditReservationSettlement :one
UPDATE router.subscriber_credit_reservations
SET settled_usd_micros = settled_usd_micros + @retail_usd_micros::bigint,
    updated_at = NOW()
WHERE action_id = @action_id::varchar
  AND state = 'reserved'
RETURNING *;

-- Closes an authorization after returning any unused hold to its owner.
-- name: FinalizeSubscriberCreditReservation :one
UPDATE router.subscriber_credit_reservations
SET state = @state::varchar,
    updated_at = NOW()
WHERE action_id = @action_id::varchar
  AND state = 'reserved'
RETURNING *;

-- Returns an idempotent prepaid settlement by its stable action identifier.
-- name: GetSubscriberCreditSettlement :one
SELECT *
FROM router.subscriber_credit_ledger
WHERE action_id = @action_id::varchar;

-- Appends one exact subscriber prepaid inference debit. The balance_after
-- value includes unused funds still held by the request authorization.
-- name: InsertSubscriberCreditSettlement :one
INSERT INTO router.subscriber_credit_ledger (
    subscriber_id,
    delta_usd_micros,
    notional_cost_micros,
    balance_after_micros,
    entry_type,
    router_request_id,
    router_model,
    authorization_action_id,
    action_id,
    capacity_source
)
VALUES (
    @subscriber_id::uuid,
    @debit_usd_micros::bigint,
    @retail_usd_micros::bigint,
    @balance_after_micros::bigint,
    'inference',
    @router_request_id::varchar,
    @router_model::varchar,
    @authorization_action_id::varchar,
    @action_id::varchar,
    @capacity_source::varchar
)
RETURNING balance_after_micros;

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
