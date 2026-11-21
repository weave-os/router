# router — AGENTS

> **Mirror notice.** Generated from [CLAUDE.md](CLAUDE.md). Edit CLAUDE.md, then run `make generate-agent-guides`; CI rejects drift.

Rules live in checks: `golangci-lint`, `make arch-ratchet`, `make inference-boundary`, `go test ./internal/architecture/...` and the golden scenarios in `internal/proxy`. Their failure messages name the fix. This guide keeps only the incident entrypoint and the safety rules no check can enforce.

## Production incident entrypoint

For a router repair, establish a synthetic failing regression before changing behavior
and run it unchanged against the fix. For diagnosis-only requests, investigate without editing. Identify the incident-time
serving revision and configuration before applying current source assumptions. A local
repair does not authorize production mutation or establish production recovery.

| Symptom | Owning code | Initial regression boundary |
| --- | --- | --- |
| Tool/schema rejection | `internal/translate`, `internal/providers`, `internal/proxy/dispatch_error.go` | Translation unit test, then provider conformance |
| Retry, fallback, truncated stream | `internal/proxy`, `internal/dispatch`, `internal/sse`, [inference boundary](docs/INFERENCE_BOUNDARY.md) | Dispatch/SSE behavior, stream-commit and cancellation invariants |
| Wrong model or session pin | `internal/router/candidates`, `internal/routingpolicy/overrides`, `internal/router/continuity`, `internal/router/hmm`, `internal/router/escalation/floor`, `internal/router/sessionpin`, [turn loop](docs/PROXY_TURN_LOOP.md), [Go HMM selection](docs/HMM_GO_SELECTION.md) | Eligibility, precedence, strategy gates and pin transitions |
| Authentication or charges | `internal/auth`, `internal/credentials`, `internal/billing`, `cmd/router` | Credential isolation, entitlement and exactly-once accounting |
| Admission or release | `internal/server/middleware`, `internal/policyregistry`, [serving control](docs/SERVING_CONTROL.md) | Admission and immutable identity checks |
| Client setup or UI | `install/`, `frontend/`, [contributing](CONTRIBUTING.md) | Offline installer tests or frontend checks |

Run `python3 scripts/agent_checks.py doctor` and `plan --base origin/main` from this
root for read-only prerequisite and suite discovery. Missing required checks remain
blocked; do not report local checks as complete CI. Keep private incident notes in
ignored `.context/`; only authored, synthetic reproductions belong in public fixtures.

## Layering hard rules

- Imports flow inward only: presentation (`internal/api`, `internal/server`) and adapters (`internal/postgres`, `internal/providers/<name>`, `internal/pubsub`, `internal/observability/{otel,apm}`) depend on the inner ring; adapters never import each other; only `cmd/router` constructs concrete adapters and wires them with plain function calls.
- Request compatibility is Go-owned. Policy sidecars return classifier facts only; candidate filtering, provider/model eligibility, pins, fallbacks and dispatch stay in Go, which filters candidates before a sidecar call.
- When a design needs an import these rules forbid, declare a port in the inner ring and implement it in an adapter (see `.agents/skills/add-context-port`).

## Things to NEVER do

- **Never mutate production** (deploys, dispatches, `gcloud`/`terraform apply`, production SQL) as part of a repair; a local fix is not production recovery.
- **Never put customer/company/org names or private identifiers in anything committed here.** This repo is public: commit messages, branch names, PR text, code, tests and fixtures. Describe triggers generically ("a customer", "a large agentic session").
- **Never widen a credential across surfaces or tenants.** A client-supplied key serves only its inbound surface's provider, a BYOK gateway displaces every other provider, BYOK and subscription credentials never fund another installation's turn, and Weave credits fund a turn only after the customer's own options are exhausted. `internal/proxy/golden_credentials_test.go` pins this; a refactor leaves it byte-identical.
- **Never debit, refund or record usage twice, or skip it.** Every served turn is accounted exactly once against the binding that actually served it.
- **Never log secrets, raw API keys, bearer tokens, key hashes or full request bodies.** `auth.APIKey`'s 8-char prefix + 4-char suffix is the only safe form; BYOK secrets at rest go through `auth.Encryptor`.
