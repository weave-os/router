---
name: extract-to-context
description: Move existing code out of `proxy.Service` into its bounded-context package as a move-only, behavior-preserving refactor. Use for any structural move or extraction, including splitting a large function or file into a context package.
---

# Extract to a bounded context

A structural move changes where code lives, never what it does. Pick the context with `where-does-this-go`; extract the code with the least shared mutable state first.

## Steps

1. **Confirm golden coverage** of the code being moved (`golden-traces`). If a path is uncovered, add the scenario in its own PR first.
2. **Declare the port and types** in the new package (`go list ./internal/...` to find it, or create it with a `CLAUDE.md`). Wire the concrete implementation in `cmd/router`, and have `Service` hold the port instead of the fields it replaces.
3. **Move-only commit.** Cut and paste; change only identifiers and imports the move forces. Verify:
   ```bash
   git diff --color-moved=dimmed-zebra --color-moved-ws=allow-indentation-change HEAD~1
   ```
   Everything except signatures and call sites should render as moved.
4. **Tidy commit** on top: renames, dead parameters, comments.
5. **Never mix a move with a fix.** A bug found mid-move becomes a separate PR that lands a synthetic failing regression first, per the root `CLAUDE.md` incident rule.

## PR description

Add a before → after table for every baseline the move touches:

| Metric | Before | After |
|---|---|---|
| `proxy.Service` fields / methods | | |
| over-limit functions / files | | |
| duplicate-code clones | | |
| inference-boundary baseline entries | | |

Every row must shrink or stay equal. Moved code must meet the limits, or sit in the baseline with a smaller value than before.

## Verify

```bash
go test ./internal/proxy -run TestGolden   # unchanged, no -update
go test ./internal/architecture/...
make inference-boundary
make arch-ratchet                          # when the target exists
make check-changed
```

Moves in Inference Execution or Credential Brokerage also need a canary watch on error rate, failover rate, billing rows per request and decision distribution; flag that in the PR. Escalate to the CODEOWNER when the port signature would change another context's contract.
