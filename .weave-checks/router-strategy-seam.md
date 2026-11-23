---
name: Router Strategy Seam
description: Flags semantic strategy leaks across the serving/router seam that the architecture AST tests cannot see
intelligence: medium
files: **/*.go
---

## When to Check

Review only changed non-test Go under `internal/` or `cmd/`, excluding generated code and
`internal/architecture/testdata/`. Otherwise PASS immediately. Continue only when the PR changes
`router.Request`, `router.Decision`, `router.SessionView`, `policy.Capabilities`, a strategy package
(`internal/router/hmm`, `internal/router/decisionmodel`, `internal/router/policy`) or code that
consumes a `router.Decision`; otherwise PASS immediately.

The serving layer decides which models can serve; one strategy decides which eligible model should
serve (see `docs/ROUTER_ARCHITECTURE.md`). The seam is `router.Router`: filtered candidates in,
one checked decision out.

## Flag

- A capability flag, config field or `Request`/`Decision` field whose meaning is "this is HMM" (or
  another single strategy) under a neutral name, which the serving layer then branches on. Show the
  new field, the only strategy that sets it, and the serving-layer decision that reads it.
- A strategy-specific fact added to `router.Request`, `router.Decision`, `router.SessionView` or
  routing metadata for the serving layer to act on, instead of the strategy acting on it itself.
  Name the fact, its producing strategy and the serving-layer consumer.
- A strategy that calls another strategy's `Route`, or wraps another strategy to change its pick.
  Strategies compose shared helpers, not each other.
- Strategy state carried between turns through `context.Context`, a package variable or a cache
  outside the session store, so another strategy or the serving layer can observe it.
- Serving-layer code that re-ranks or replaces a valid strategy pick for a reason other than
  eligibility, an explicit command or failover, so it silently overrides the strategy's choice.

Anchor each finding to new code in the diff and show both sides of the leak. If the evidence is
not in the supplied diff/context, PASS.

## Do Not Flag

- Existing unchanged code, pure moves, tests, fixtures, generated code and docs.
- Anything the `internal/architecture` atomization ratchets already count: imports between strategy
  packages and serving adapters (`strategy_serving_adapter_imports`, `serving_strategy_internal_imports`),
  named-strategy references in the serving layer (`serving_strategy_constant_references`),
  `router.StrategyFromContext` callers (`strategy_from_context_callers`), HMM-only metadata reads and
  reason-text comparisons in `internal/proxy` (`proxy_hmm_metadata_references`,
  `proxy_reason_string_comparisons`), router-layer helper and escalation calls from `internal/proxy`
  (`proxy_router_layer_helper_calls`, `proxy_escalation_internal_references`), and HMM-only pin
  fields or switch history outside the HMM accessor (`hmm_pin_field_references`,
  `hmm_history_identifier_references`).
- Generic capability flags that several strategies can honor, such as those already in
  `policy.Capabilities`.
- Eligibility, explicit commands, usage bypass and failover in the serving layer; those are its job.
- Wiring a concrete strategy in `cmd/router`.
