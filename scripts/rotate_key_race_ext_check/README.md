# rotate_key_race_ext_check

Extends [`rotate_key_race_check`](../rotate_key_race_check/README.md) along four axes,
against a live Postgres:

1. **Concurrency sweep.** The list-held-open barrier at N = 2, 3, 10 and 25.
2. **Rotate vs delete.** `DeleteAPIKey` is forced to win the soft-delete after
   `RotateAPIKey` has already listed the key as active (`signalListRepo` parks the
   rotation after its read and signals once it holds the pre-delete snapshot). A
   rotation that still issues a live successor for the deleted key counts as a zombie
   successor.
3. **HTTP level.** `POST /admin/v1/keys/:id/rotate` through gin `httptest` with the
   real `WithAdminOnly` cookie auth (not a live listener). The gin harness is built
   outside any request-scoped function because handlers derive their context from each
   request. Admin cookie sessions act on `EnsureAdminInstallation`'s singleton, which
   may hold leftovers, so only keys named for the race are counted.
4. **Timing sensitivity.** N = 2, 20 runs each: first a post-List sleep sweep with a
   simultaneous start (widening the soft-delete delay), then a start-stagger sweep with
   no sleep. The largest stagger with at least one hit in 20 approximates the
   real-world overlap window, such as a client retrying a timed-out rotate.
   `timedListRepo` records List completion times so a hit reports the observed
   inter-List gap: the minimum for the sleep sweep, the maximum for the stagger sweep.

It is a separate `main` package, gated on `ROUTER_TEST_DATABASE_URL` and a no-op
without it.

## Usage (from the repo root)

```bash
ROUTER_TEST_DATABASE_URL="postgres://router:router@localhost:5433/router?search_path=router" \
    go run ./scripts/rotate_key_race_ext_check
```
