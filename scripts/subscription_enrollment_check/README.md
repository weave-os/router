# Subscription enrollment regression

Against a disposable, fully migrated loopback Postgres database:

```sh
ROUTER_TEST_DATABASE_URL='postgres://router:router@localhost:5432/router?sslmode=disable&search_path=router' \
  go test -count=1 -v ./scripts/subscription_enrollment_check
```

Requires Bash, Python 3 (PTY support), curl, jq, and OpenSSL. The test drives the
real interactive Claude installer against a local OAuth fixture and the real
subscription API, auth encryption, and Postgres repository. Only OAuth and the
already-authenticated caller context are stubbed; no provider credentials or
external OAuth calls are used. All database writes roll back after the test.

Covers same-account-and-organization reconnect after key rotation, distinct
Claude organizations and accounts, latest encrypted credentials,
enabled/cooldown reset, access-cache/lease reset, stale refresher fencing, Codex
API deduplication, and secret-free API/CLI output.
Missing/malformed OAuth identities are covered offline by
`bash install/tests/subscription_cli_test.sh`.
