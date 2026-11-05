---
name: Router Bounded Contexts
description: Flags new router behavior or ports placed outside the context that owns the decision
intelligence: medium
files: **/*.go
---

## When to Check

Review only changed non-test Go under `internal/` or `cmd/`, excluding generated code and
`internal/architecture/testdata/`. Otherwise PASS immediately. Only continue for a new package,
port, behavior-owning type, or `proxy.Service` field or method; otherwise PASS immediately.

The five responsibility boundaries are:

- **Inference Execution:** provider calls, retries, failover, target validation and attempt reporting
  behind `inference.Executor`, implemented by `internal/dispatch`.
- **Protocol & Ingress:** Anthropic Messages, OpenAI Chat, OpenAI Responses and Gemini handling,
  HTTP concerns, response rendering and stream lifecycle.
- **Routing Policy:** strategy selection, eligibility filtering, planner, policy resolution and
  catalog checks. Produces a typed execution plan without HTTP, billing or provider-client concerns.
- **Credential Brokerage:** deployment keys, BYOK, OAuth, subscriptions, credential-backed provider
  eligibility and credential selection.
- **Conversation Lifecycle:** turn classification, commands, compaction, handover, tool-result
  transitions and continuation state.

These are responsibilities, not a requirement to create five new packages in this PR. Existing
focused packages remain owners while the legacy orchestration is migrated.

## Flag

- A new umbrella package (`common`, `util`, `shared`, `types`) collecting unrelated context behavior
  or ports. Identify the distinct concepts it joins and their owning contexts.
- A new port declared outside the context that owns its operation, such as a credential-selection
  interface declared in an ingress handler. Identify the operation and its semantic owner; a name
  alone is not evidence. Ports belong with the concept they serve, not in a generic interfaces box.
- New decision logic placed in the wrong context: for example, a renderer choosing credentials or
  a planner implementing conversation transitions. Name the changed decision and its proper owner.
- A new `proxy.Service` field or method that implements one of these contexts' rules instead of
  delegating to its focused owner. Show the substantive rule or state it adds; a count increase
  alone belongs to the architecture ratchet, not this check.

Anchor each finding to new code. Establish ownership from the supplied diff and context; if that
evidence is insufficient, PASS rather than infer a missing boundary or demand a broad rewrite.

## Do Not Flag

- Existing code the PR does not change, pure moves or renames, tests, fixtures, generated
  `internal/sqlc`, other generated code and docs.
- Wiring in `cmd/`, constructor injection, thin delegation, value mapping or orchestration that
  sequences owners without reimplementing their rules.
- A focused existing package or a staged extraction that preserves behavior and reduces legacy
  ownership. Do not demand the remaining migration in the same PR.
- A port with standard Go names, or one defined by its consumer within the owning context.
- Credential-backed eligibility in Credential Brokerage versus model-capability eligibility in
  Routing Policy; these are distinct decisions.
- Import violations, function/file size, field/method counts or anything already enforced by
  depguard, the architecture ratchet or architecture tests. Do not duplicate another check's finding.
