# internal/billing

The inner-ring credit-billing domain for the managed router: preflight balance
and spend-cap checks, override resolution, and atomic debit-with-ledger. It is
pure orchestration over the `Repo` port; the Postgres adapter is
`internal/postgres/billing_repo.go` and the autopay signaller is a Pub/Sub
adapter in `internal/pubsub`, both wired by `cmd/router`.

## Funding owners

Money lives in typed prepaid books keyed by `Owner`: an organization's balance,
an individual Max/Boost subscriber's balance (keyed by credential subject, never
by an organization they work in), and an isolated internal test book. A kind
with no registered book fails closed with `ErrOwnerKindUnsupported`, meaning
"this owner cannot pay here", never a fall back to another owner's funds.
`NewService` binds only the organization book; `WithSubscriberPrepaid` and
`WithInternalTestPrepaid` opt the others in. `prepaid_test.go` and
`test_budget_test.go` pin the isolation.

## Preflight gates

- A balance at or below `MinBalanceMicros` (zero) blocks new requests with 402;
  debits already in flight still settle below it, matching provider prepaid
  semantics
  (`TestWithBalanceCheck_ProductionThresholdBlocksAtZeroAndAdmitsOneMicro`).
- A missing balance row (`ErrBalanceRowMissing`) gets the same 402 as a depleted
  balance, with a distinct operator log line; the organization needs a backfill.
- Spend caps (API key lifetime, delegated session/task, engineer and organization
  UTC-month) are read fresh from storage, bypassing the auth cache, so a cap is
  never judged against a stale cached spend. They are preflight checks, not a
  hard guarantee: spend is recorded after the turn serves, so requests already in
  flight when a cap is nearly reached can together exceed it. A read failure fails closed
  with 503 (`ErrSpendLimitCheckUnavailable`); a key deleted mid-request has no cap
  to enforce.

## Subscription-only reasons

A gate that bars paid capacity marks the request with a `SubscriptionOnlyReason`.
Every reason is served the same way, on the caller's own subscription or (for
depleted credits and reached caps) their own provider key, with no Weave-funded
failover and no inference debit. The reason only decides what a refused turn
says (top-up CTA for depleted credits, a cap notice for a reached cap) and which
marker a served turn carries. The reason is a required argument so a new gate
must decide what the caller is told. A recorded reason is never downgraded:
depleted credits outrank a reached cap, which outranks a linked-first
preference. `ReleaseLinkedFirst` lets a linked-first turn fall through to the
organization balance it was already admitted against.
`subscription_only_reason_test.go` pins the precedence.

## Debits

`DebitForInference` writes one ledger row at cost; margin is collected at
top-up by the backend. Every row records the notional cost as a shadow trail.

| Turn | Inference row delta | Extra row |
| --- | --- | --- |
| Billing override or subscription-served | 0 | none |
| BYOK (customer's own provider key) | 0 | `byok_fee` at the configured rate, rounded half up to the micro; none when the rate is zero or the organization could not pay (depleted credits, reached cap) |
| Individual allowance or subscriber prepaid | never touches the organization book | none |
| Everything else | minus the notional cost | none |

Override and subscription outrank BYOK. An override debit returns the unchanged
balance. Ledger `entry_type` values must stay in the
`router.organization_credit_ledger` CHECK constraint
(`TestLedgerEntryTypesMatchTheLatestCheckConstraint`). A billable turn whose
model has no catalog pricing logs a warning because it is about to debit zero.

## Autopay

After a debit that moves the balance, the service signals a recharge once, on
the debit that crosses from at-or-above the owner's autopay threshold to below
it. A config read failure is logged and dropped because the debit already
committed and the control plane's reconciliation sweep backstops a missed
signal. Without a notifier (self-hosted, or the topic unset) the check is off.
