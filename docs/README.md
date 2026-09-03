# Router documentation

Index of Markdown documentation in the `router/` repo.

| Doc | What it covers |
|---|---|
| [SEMANTICS.md](SEMANTICS.md) | **Canonical terminology** for sessions, rounds, turns, actions, and steps. Read this first before other docs. |
| [CONFIGURATION.md](CONFIGURATION.md) | Environment variables, provider keys (gateways, model aliases, key-pair auth), BYOK encryption, OTel knobs, cluster routing artifacts. |
| [ANALYTICS_EXPORT.md](ANALYTICS_EXPORT.md) | `/v1/analytics/*` raw routing-decision export: read-only keys, cursor paging, row grain, field reference. |
| [POLICY_ROUTER_HARNESS.md](POLICY_ROUTER_HARNESS.md) | Contract for out-of-process policy sidecars. |
| [HMM_GO_SELECTION.md](HMM_GO_SELECTION.md) | Architecture, `policy_router_v3` split, and rollback story for Go-owned HMM roster ownership and deterministic arm selection. |
| [TRANSLATION_COMPATIBILITY.md](TRANSLATION_COMPATIBILITY.md) | Cross-format translation requirements and rollout modes. |
| [SMOKE.md](SMOKE.md) | Pre-merge record/replay smoke-suite scenarios, cassettes, and local workflow. |

For engineering conventions (layer model, package layout, recipes), see the
root [`CLAUDE.md`](../CLAUDE.md) (and its mirror [`AGENTS.md`](../AGENTS.md)).
