# Fail-open harness bundle (copy into weave-os/router)

This directory is **not** the router checkout. Main copies files into the
router module using the mapping below.

Source tree (this bundle):

```
.context/fail-open-harness/
  scripts/smoke/fail_open.sh
  scripts/smoke/fail_open/go.mod
  scripts/smoke/fail_open/controller.go
  scripts/smoke/fail_open/controller_test.go
  docker-compose.fail-open.yml
  docs/FAIL_OPEN_TESTING.md
  README.md          (this file; optional to copy)
```

Relative mapping inside `router-internal/router/` (or `weave-os/router`):

| Bundle path | Router path |
|---|---|
| `scripts/smoke/fail_open.sh` | `scripts/smoke/fail_open.sh` |
| `scripts/smoke/fail_open/*` | `scripts/smoke/fail_open/*` |
| `docker-compose.fail-open.yml` | `docker-compose.fail-open.yml` |
| `docs/FAIL_OPEN_TESTING.md` | `docs/FAIL_OPEN_TESTING.md` |

Suggested docs index row (do not apply here):

`| [FAIL_OPEN_TESTING.md](FAIL_OPEN_TESTING.md) | Isolated compose + TCP fault controller for original-model fail-open (PR2 policy/sidecar faults; PR3–4 extend). |`

chmod +x `scripts/smoke/fail_open.sh` after copy.
