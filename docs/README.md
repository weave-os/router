# Router documentation

Start with the task you need to complete:

| Task | Documentation |
|---|---|
| Connect a coding tool to the hosted router | [Hosted quickstart](../README.md#30-second-quickstart) and [installer flags](../install/npm/README.md) |
| Run the router and database yourself | [Self-hosted setup](../README.md#or-self-host-the-whole-stack) |
| Configure a client, choose models, or switch routing off | [Coding-tool setup](../README.md#wire-it-into-your-tools) and [installer guide](../install/README.md) |
| Find API endpoints | [Endpoint index](../README.md#endpoints) |
| Understand per-action routing | [Canonical semantics](SEMANTICS.md) |
| Configure providers, credentials, or telemetry | [Configuration reference](CONFIGURATION.md) |
| Read session costs and routing decisions | [Session cost](SESSION_COST.md) and [analytics export](ANALYTICS_EXPORT.md) |
| Inspect cross-format behavior | [Translation compatibility](TRANSLATION_COMPATIBILITY.md) |

The hosted quickstart and self-hosted stack have different operating and credential requirements. Follow the instructions for the deployment you use. Client and model compatibility also vary; the README identifies Cursor as an early beta.

## Engineering reference

Index of Markdown documentation in the `router/` repo.

| Doc | What it covers |
|---|---|
| [SEMANTICS.md](SEMANTICS.md) | **Canonical terminology** for sessions, rounds, turns, actions, and steps. Read this first before other docs. |
| [CONFIGURATION.md](CONFIGURATION.md) | Environment variables, provider keys (gateways, model aliases, key-pair auth), BYOK encryption, OTel knobs, cluster routing artifacts. |
| [INFERENCE_BOUNDARY.md](INFERENCE_BOUNDARY.md) | Inference operation inventory, policy-boundary exceptions, and staged migration enforcement. |
| [POLICY_INFERENCE.md](POLICY_INFERENCE.md) | Generated static registry of inference purposes, selection policy, constraints, fallback, and migration status. |
| [ANALYTICS_EXPORT.md](ANALYTICS_EXPORT.md) | `/v1/analytics/*` raw routing-decision export: read-only keys, cursor paging, row grain, field reference. |
| [SUBSCRIPTION_ROUTING.md](SUBSCRIPTION_ROUTING.md) | Personal and shared subscription candidate admission, alternatives, native OAuth dispatch with quota checks, and API fallback. |
| [DELEGATED_SESSION_TOKENS.md](DELEGATED_SESSION_TOKENS.md) | Experimental `rs_` session JWTs signed by a registered Ed25519 issuer: token claims, issuer registration and revocation. |
| [SESSION_COST.md](SESSION_COST.md) | `GET /v1/sessions/:session_id/cost`: `rk_`/`ra_` auth, installation scope, response fields, errors, rate limit. |
| [POLICY_ROUTER_HARNESS.md](POLICY_ROUTER_HARNESS.md) | Contract for out-of-process policy sidecars. |
| [LLM_CLASSIFIER_CONTEXT.md](LLM_CLASSIFIER_CONTEXT.md) | Staged atomic-response classifier input contract and unresolved admission/recovery requirements; not serving-enabled. |
| [SERVING_CONTROL.md](SERVING_CONTROL.md) | Immutable managed releases, gateway admission, activation, rollback, and deployment prerequisites. |
| [HMM_GO_SELECTION.md](HMM_GO_SELECTION.md) | Architecture, `policy_router_v3` split, and rollback story for Go-owned HMM roster ownership and deterministic arm selection. |
| [TRANSLATION_COMPATIBILITY.md](TRANSLATION_COMPATIBILITY.md) | Cross-format translation requirements and rollout modes. |
| [ROUTER_NOTICES.md](ROUTER_NOTICES.md) | Typed router notices, client-protocol sinks, capabilities, and retry lifecycle. |
| [SMOKE.md](SMOKE.md) | Pre-merge record/replay smoke-suite scenarios, cassettes, and local workflow. |
| [CI.md](CI.md) | CI timing baseline, completed improvements, and the remaining optimization backlog. |

For engineering conventions (layer model, package layout, recipes), see the
root [`CLAUDE.md`](../CLAUDE.md) (and its mirror [`AGENTS.md`](../AGENTS.md)).
