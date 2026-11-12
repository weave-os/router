---
name: golden-traces
description: Add, read and update the golden decision-trace scenarios in `internal/proxy` that pin routing, dispatch, failover and billing decisions. Use before and after any refactor of proxy code, when a golden test fails, and when turning an incident into a regression.
---

# Golden decision traces

The golden harness in `internal/proxy` replays synthetic requests through the router with fake providers and records each request's decision trace (routing decision, attempts, failover, stream commit, billing). A refactor must leave every trace byte-identical.

```bash
go test ./internal/proxy -run TestGolden            # compare against committed goldens
go test ./internal/proxy -run TestGolden -update    # regenerate
rg -l 'TestGolden' internal/proxy                   # find the harness and its scenarios
```

## Adding a scenario

1. Read the existing scenarios and copy the closest one.
2. Keep it synthetic and public-safe: made-up keys, sessions and prompts, no customer data or real transcripts.
3. One behavior per scenario, named for the behavior (`failover_then_success_bills_once`, not `bug_123`).
4. Run with `-update` once to create the golden, then read the new file in full before committing.

## Reading a trace diff

A failing run prints expected vs actual traces. Treat any diff as a behavior change: find the first divergent event (decision, attempt, commit, billing row) and trace it to the code. Ordering and count changes matter as much as values.

## `-update` rules

- Never run `-update` to make a refactor pass. A move-only PR must pass without it.
- Run it only when the behavior change is intended, in the same PR as that change, and list each changed scenario and why in the PR description.
- Review the regenerated diff line by line; commit goldens with the code that changed them.

## Incident workflow

1. Write a synthetic scenario that reproduces the incident and fails on `main`.
2. Commit it, then fix the code; the same scenario must pass unchanged.

## Verify

```bash
go test ./internal/proxy -run TestGolden -count=5
git status --short internal/proxy   # no unexpected golden changes
```

Escalate when a trace diff is intended but touches billing or credentials; name the reviewer for that context in the PR.
