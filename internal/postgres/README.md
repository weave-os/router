# internal/postgres

SQLC/pgx adapters for the inner-ring repository ports. Each repo is a thin
translation between SQLC rows and domain types; business rules stay in the
owning context.

## Adapter contracts

- **Money.** Costs are stored as BIGINT USD micros and converted at this
  boundary. Signed conversions keep negative expected values (stay-turn EV);
  unknown usage stays NULL so an unmeasured attempt is never a zero-cost one.
  Subscription-served turns export a $0 served cost.
- **Missing rows.** A missing balance row is `billing.ErrBalanceRowMissing`, not a
  zero balance. A missing session pin makes usage, error-counter and calibration
  updates no-ops. Missing spend or config rows read as zero spend and no limit;
  a malformed id in a spend lookup means "no cap to enforce" rather than a
  rejected request.
- **Tolerant decoding.** A stored JSONB value that fails to decode (tokenizer
  calibration, demotion cooldowns, flag overrides) degrades to its empty value
  instead of failing the read, so one corrupt row cannot take traffic down. A
  session issuer key that fails to decode is treated as an unknown issuer.
- **NULL normalisation.** Empty strings, empty alias maps and malformed or empty
  UUIDs are written as SQL NULL; NULL UUIDs export as `null`, never the zero UUID.
- **Tenant boundary.** `installation_id` (or the credential subject for
  individual subscriber books) is the authorization boundary on every read and
  write. An organization owner is never reinterpreted as a subscriber, and a
  subscription owner with neither subscriber nor key id fails closed instead of
  matching legacy rows.
- **Exactly-once accounting.** Debits update the balance and append the ledger
  row in one statement. Allowance and prepaid holds are keyed by action id: a
  redelivery returns the stored action; a statement that loses a race re-reads
  outside its own snapshot so the winner's commit is seen as a redelivery.
- **Leases and checkpoints.** Escalation claims, commits and releases run in
  short transactions under a per-session lock; a release cannot clear a
  successor's lease and an expired lifetime is never resurrected.
- **Flag registry publish.** Not transactional: every replica publishes the same
  rows at boot, and the registry version prevents an older revision from pruning
  a newer revision's rows.
- **External consumers.** `serving.CredentialLookup` has no caller inside the
  router, but the Weave benchmark harness (`ml_dev/router_benchmarks/harness_setup`)
  builds it from the pinned router tree to verify fixture credentials; keep it.
- **Check fixtures.** `EscalationCheckFixture` and `LLMEscalationCheckFixture`
  only touch the disposable installation the database verification command
  created.
