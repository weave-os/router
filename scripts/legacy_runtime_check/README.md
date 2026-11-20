# Legacy worker compatibility fixture

This boots the real worker in both `selfhosted` and `managed`
modes against an already-migrated, disposable loopback Postgres database.
It starts its own loopback Pub/Sub gRPC fixture and serves the stub HMM
classifier and baked policy release from `smoke/hmmstub`, because workers boot
on the `hmm_embedding` default. No real provider or cloud credentials are
inherited, and no inference provider is called.

```bash
ROUTER_TEST_DATABASE_URL='postgres://fixture-user:fixture-password@localhost:5432/router?sslmode=disable&search_path=router' \
ROUTER_TEST_WORKER_BINARY=/absolute/path/to/router \
go run ./scripts/legacy_runtime_check
```

Use only a newly created local database, never a shared developer database.
The command creates random installations/keys and retains its worker logs in
the OS temporary directory. It never prints the raw fixture routing key.

The checks require readiness, successful shared-key authentication, the Codex
model-list response, a successful self-hosted route decision, and the managed
prepaid-credit gate. Neither leg sets a `ROUTER_SERVING_*` variable:
`ROUTER_SERVING_TARGET` enables the serving runtime in either deployment mode,
so these unstamped legacy workers must boot without it.

`.github/workflows/test.yml` builds both workers with `CGO_ENABLED=0`,
denies the fixture database role access to all new managed-serving tables,
and runs this command against both the proposed binary and the retained base
binary on the upgraded schema. Migrations run up/down/up only on that CI
database; a deployed database must never be downgraded for worker rollback.

Private GCS/IAM,
production HMM/classifier identity, gateway cutover, and real streaming
provider behavior still require their separate validation gates.
