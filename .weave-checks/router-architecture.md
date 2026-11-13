---
name: Router Architecture
description: Flags semantic I/O and provider-boundary leaks not covered by mechanical architecture gates
intelligence: medium
files: **/*.go
---

## When to Check

Review only changed non-test Go under `internal/` or `cmd/`, excluding generated code and
`internal/architecture/testdata/`. Otherwise PASS immediately. Continue only when the PR adds
I/O behavior, a provider-shaped type, provider-specific business logic or an execution path;
otherwise PASS immediately.

The root `CLAUDE.md` names these I/O-free inner-ring packages: `internal/inference`,
`internal/router`, `internal/providers` (the root interface package, not its adapters),
`internal/translate`, `internal/sse`, `internal/timing`, `internal/requestcontext`,
`internal/feedback`, `internal/router/{breaker,catalog,handover,planner,policy,sessionpin,turntype}`
and `internal/proxy/usage`. It also describes `internal/websearch` as pure. This is not a ban on
I/O in every descendant: `internal/providers/<adapter>` packages are adapters.

## Flag

- New HTTP, database, queue or filesystem effects inside an I/O-free package, including effects
  hidden behind a callback or generic interface. Show the changed call, its concrete effect and
  why it is not merely defining a port or manipulating in-memory data.
- A new domain request/response or port shaped by a provider's wire fields, statuses or SDK types,
  rather than neutral values. Identify the provider-only field and the domain consumer forced to
  understand it; naming a protocol alone is insufficient.
- A provider/model name used outside adapters or catalog data to decide a business rule that
  belongs in a typed capability or catalog property. Show both the identity-dependent decision
  and the semantic property it encodes, not just a string occurrence.
- New provider execution that semantically bypasses the `inference.Executor` / `internal/dispatch`
  path, losing resolved-plan authorization, target validation or attempt reporting. Trace the
  changed reachable path to upstream I/O and name the bypassed guarantee.

Report only semantic gaps not already rejected by depguard, `internal/architecture` tests or the
architecture ratchet. Direct client calls, provider maps, fixed-target construction and raw HTTP
inference already covered by those tests are not additional findings here. Evidence must be in
the supplied diff/context; scoped checks cannot inspect unrelated files. If proof is missing, PASS.

## Do Not Flag

- Existing unchanged code, pure moves, tests, fixtures, generated `internal/sqlc`, other generated
  code and docs. A legacy exception is not a demand to complete its migration now.
- Provider wire types, native names and format conversion at protocol ingress/egress, in
  `internal/translate` or inside provider adapters; those boundaries must speak external protocols.
- Canonical provider constants in `internal/providers`, catalog entries, composition-root
  registration, or names merely carried as opaque data or diagnostic labels.
- Catalog/capability lookup instead of identity-based policy, and adapter-specific transport details.
- A port declaration, injected executor invocation, pure reader/writer framing or memory buffer
  operation. An `io.Reader`/`io.Writer` parameter alone does not prove external I/O.
- Linter import findings, deterministic architecture-test findings or repeated findings already
  owned by another check. Do not ask to ban utility dependencies or rewrite existing boundaries.
