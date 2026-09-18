# Subscriber entitlement integration check

This command verifies the projected entitlement repository against a migrated
ephemeral Postgres database. It rejects non-loopback database URLs.

```bash
ROUTER_TEST_DATABASE_URL='postgres://router:router@localhost:5432/router?sslmode=disable&search_path=router' \
  go run ./scripts/subscriber_entitlement_check
```
