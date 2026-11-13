---
name: Router Credential Isolation
description: Flags misplaced credential decisions, mixed credential authority and demonstrated secret disclosure
intelligence: medium
files: **/*.go
---

## When to Check

Review only changed non-test Go under `internal/` or `cmd/`, excluding generated code and
`internal/architecture/testdata/`. Otherwise PASS immediately. Continue only for changes that
select, propagate or expose deployment keys, BYOK, OAuth or subscription credentials;
otherwise PASS immediately.

Credential Brokerage owns credential selection and credential-backed provider eligibility.
Existing dedicated credential selectors, auth/OAuth/subscription code and their adapters are
the current owners; do not invent a mandatory new package layout. Routing Policy may consume
eligibility results, ingress may carry request authority and adapters may apply selected credentials.

## Flag

- New selection, refresh, account-pool or credential fallback logic in protocol rendering, model
  policy or a provider transport rather than the owning broker/selector. Show the actual authority
  decision and the owner it duplicates or bypasses; reading a selected credential is not selection.
- One selection decision combining caller BYOK and deployment-paid credentials as interchangeable
  candidates or silently falling back between them. Trace the changed branch that loses credential
  provenance or caller opt-in, and its consequence for eligibility, billing or upstream authority.
- A credential value reaching a log, telemetry attribute, returned error or committed fixture
  payload from changed production code. Trace source to sink, including any redaction in between.
  Report the symbols and exposure path, never quote a secret or ask for one to prove the finding.

Each finding needs a changed line, credential authority, reachable decision or sink, and concrete
consequence supported by the supplied diff/context. Missing code is not proof of missing isolation;
when the path cannot be established, PASS. Do not duplicate a linter, test or another check's finding.

## Do Not Flag

- Existing unchanged code, pure moves, tests and synthetic fixtures, generated `internal/sqlc`,
  other generated code and docs. Do not audit test files through this production-code check.
- Applying an already-selected credential in a provider adapter, or carrying it through
  `internal/requestcontext` to its intended recipient.
- Composition-root loading/injection of deployment keys, auth middleware parsing credentials or
  request authority, and encryption/storage through the existing credential owner and adapter.
- Separate BYOK and deployment decisions whose provenance and authorization remain distinct;
  merely handling both modes in one function is not mixing them.
- Opaque key/account IDs, credential-source enums, redacted diagnostics and the documented safe
  API-key prefix/suffix. A variable named `key` is not evidence that its value is secret.
- Catalog/model capability filtering without credential selection, or ordinary transport retries
  using the same selected authority. Do not demand a second broker or duplicate enforcement.
