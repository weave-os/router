# serving_admission_check

Loopback-only primary-database admission harness; see
[SERVING_CONTROL.md](../../docs/SERVING_CONTROL.md). It refuses non-loopback
databases, never logs routing credentials, and fails if any fixture row survives.

```bash
ROUTER_TEST_DATABASE_URL="postgres://router:router@127.0.0.1:5432/router?sslmode=disable&search_path=router" \
  go run ./scripts/serving_admission_check
```
