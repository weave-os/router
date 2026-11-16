# rotate_key_race_check

Reproduces concurrent `RotateAPIKey` calls on the same key against a live Postgres.
Rotation lists the installation's keys, soft-deletes the old key and issues a
successor without a transaction or rows-affected check, so several concurrent
rotations of one key can leave more than one live successor. The check creates a
fresh installation and key, fires the concurrent rotations, and reports how many
succeeded and how many active keys remain.

A start-only barrier misses the race: List → SoftDelete takes microseconds, so one
caller soft-deletes before the others list. `holdingAPIKeyRepo` therefore parks every
`ListForInstallation` caller at a reusable N-party barrier (`listHold`) until all
rotators have observed the pre-delete key set. Final verification uses the unwrapped
repository, which would otherwise wait forever for more arrivals. The pool is sized
above the number of concurrent rotations so the pool itself never serializes them.

It is a separate `main` package, not a test, so `go test ./...` never touches
Postgres. It is gated on `ROUTER_TEST_DATABASE_URL` (a database with the router
migrations applied) and is a no-op without it. See
[`rotate_key_race_ext_check`](../rotate_key_race_ext_check/README.md) for the
extended sweeps.

## Usage (from the repo root)

```bash
ROUTER_TEST_DATABASE_URL="postgres://router:router@localhost:5433/router?search_path=router" \
    go run ./scripts/rotate_key_race_check
```
