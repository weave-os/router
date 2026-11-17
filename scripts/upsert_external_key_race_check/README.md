# upsert_external_key_race_check

Races two concurrent `UpsertExternalAPIKey` calls for the same installation and
provider, holding both between soft-delete and create, to check that the
`UNIQUE (installation_id, provider) WHERE deleted_at IS NULL` index fails the
loser instead of leaving two live BYOK rows. No-op unless
`ROUTER_TEST_DATABASE_URL` is set; use a disposable local database.

```bash
ROUTER_TEST_DATABASE_URL="postgres://router:router@localhost:5433/router?search_path=router" \
  go run ./scripts/upsert_external_key_race_check
```
