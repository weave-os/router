# Legacy worker compatibility fixture

This boots the real ORT-enabled worker in both `selfhosted` and `managed`
modes against an already-migrated, disposable loopback Postgres database.
It starts its own loopback Pub/Sub gRPC fixture. No real provider or cloud
credentials are inherited, and no inference provider is called.

```bash
ROUTER_TEST_DATABASE_URL='postgres://fixture-user:fixture-password@localhost:5432/router?sslmode=disable&search_path=router' \
ROUTER_TEST_WORKER_BINARY=/absolute/path/to/router \
ROUTER_ONNX_ASSETS_DIR=/absolute/path/to/assets \
ROUTER_ONNX_LIBRARY_DIR=/absolute/path/to/native/lib \
go run ./scripts/legacy_runtime_check
```

Use only a newly created local database, never a shared developer database.
The command creates random installations/keys and retains its worker logs in
the OS temporary directory. It never prints the raw fixture routing key.

The checks require readiness, successful shared-key authentication, the Codex
model-list response, a successful self-hosted route decision, and the managed
prepaid-credit gate. Invalid managed-serving configuration is deliberately
present while the assertion key is absent, proving the feature stays dormant.

`.github/workflows/test.yml` prepares native assets pinned by `Dockerfile`,
denies the fixture database role access to all new managed-serving tables,
and runs this command against both the proposed binary and the retained base
binary on the upgraded schema. Migrations run up/down/up only on that CI
database; a deployed database must never be downgraded for worker rollback.

This fixture exercises legacy cluster configuration. Private GCS/IAM,
production HMM/classifier identity, gateway cutover, and real streaming
provider behavior still require their separate validation gates.
