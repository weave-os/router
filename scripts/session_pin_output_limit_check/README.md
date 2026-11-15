# session_pin_output_limit_check

Exercises the session-pin output-limit marker lifecycle against a migrated disposable
database: the `UpsertSessionPin` preserve/reset rules and the atomic
`UpdateSessionPinUsage` write. An in-memory fake cannot prove the SQL, so the check
runs the real Postgres adapter inside one transaction. Rolling that transaction back
is the only cleanup, on success and failure alike. Timestamps are truncated to
microseconds, Postgres's precision, before comparison.

The serving identities are synthetic; the SQL treats them as opaque. The check also
writes a second role, `hmmHistoryRole`, which mirrors the proxy's unexported
HMM-history role suffix; only `(session_key, role)` isolation matters here.

Lifecycle, in order:

1. Fresh rows for both roles start with no evidence.
2. A healthy high-output turn records usage without a marker.
3. A confirmed cap binds the marker to the same turn-end instant.
4. An older healthy completion must not erase newer cap evidence or regress the usage
   and serving identity the next continuation reads.
5. A same-strategy refresh preserves the marker with the other usage fields.
6. Re-anchoring to another model under the same strategy keeps the prior served
   outcome: the guard names `LastServedModel`, not the new anchor.
7. A later healthy turn clears the marker in the same write as its usage.
8. A cap or a healthy write reported by a strategy that no longer owns the row must
   neither mark nor clear it.
9. The history role, never written, inherits nothing.
10. A strategy replacement resets the marker with the other prior usage.
11. Marking the history role leaves the default role untouched, and `Consume` hands the
    marker back with the row it removes.

Set `ROUTER_TEST_DATABASE_URL` to opt in; unset means the check is skipped, not
passed.

## Usage (from the repo root)

```bash
ROUTER_TEST_DATABASE_URL="postgres://router:router@localhost:5432/router?sslmode=disable&search_path=router" \
    go run ./scripts/session_pin_output_limit_check
```
