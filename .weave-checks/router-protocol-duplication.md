---
name: Router Protocol Duplication
description: Flags a newly copied cross-ingress feature rule that should reuse its existing shared owner
intelligence: medium
files: **/*.go
---

## When to Check

Review only changed non-test Go under `internal/` or `cmd/`, excluding generated code and
`internal/architecture/testdata/`. Otherwise PASS immediately. Continue only when a feature is
added or substantively changed in Anthropic Messages, OpenAI Chat, OpenAI Responses or Gemini
ingress and another implementation of that same feature is visible in the supplied diff/context.
Otherwise PASS immediately; do not infer duplication from an ingress name or similar signatures.

## Flag

- A feature rule, state transition or multi-step algorithm newly copied into one ingress even
  though the same semantics are already implemented for another ingress. Examples include
  repeated turn classification, continuation handling or eligibility orchestration after parsing.
- A protocol path reimplementing a rule already provided by an existing context-owned helper,
  where adapting neutral input/output would preserve the protocol's contract.

For every finding, name both concrete implementations, the matching inputs/conditions and outcome,
and the shared rule that could drift. Anchor to the new copy, show that protocol differences do not
justify it, and recommend reuse in the existing owning context rather than a new umbrella package.
If the second implementation or semantic equivalence is not visible, PASS. Do not claim access to
unseen files: scoped checks have no repository-reading tools.

## Do Not Flag

- Existing unchanged duplication, pure moves, tests, fixtures, generated `internal/sqlc`, other
  generated code and docs.
- Native parsing, wire-format conversion, error envelopes, response rendering or stream framing
  that must differ by protocol. Similar syntax is not a shared business rule.
- Thin wrappers that adapt protocol values and delegate to the same owner, or a staged extraction
  that removes copies without migrating every ingress at once.
- Ordinary two-site boilerplate or short mappings; do not propose a framework, exported shared
  package or premature abstraction just to remove superficially similar lines.
- A feature intentionally supported by only one protocol, or missing feature parity without a
  duplicate implementation. This check does not require every feature on every ingress.
- Duplicate code already reported by `dupl`, another linter, an architecture test or another check.
  This check owns semantic duplication the mechanical gates cannot see, not textual similarity.
