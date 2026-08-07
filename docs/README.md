# Router documentation

Index of Markdown documentation in the `router/` repo.

| Doc | What it covers |
|---|---|
| [SEMANTICS.md](SEMANTICS.md) | **Canonical terminology** for sessions, rounds, turns, actions, and steps. Read this first before other docs. |
| [CONFIGURATION.md](CONFIGURATION.md) | Environment variables, BYOK encryption, OTel knobs, cluster routing artifacts. |
| [ANALYTICS_EXPORT.md](ANALYTICS_EXPORT.md) | `/v1/analytics/*` raw routing-decision export: read-only keys, cursor paging, row grain, field reference. |
| [POLICY_ROUTER_HARNESS.md](POLICY_ROUTER_HARNESS.md) | Contract for out-of-process policy sidecars. |
| [TRANSLATION_COMPATIBILITY.md](TRANSLATION_COMPATIBILITY.md) | Cross-format translation requirements and rollout modes. |

For engineering conventions (layer model, package layout, recipes), see the
root [`CLAUDE.md`](../CLAUDE.md) (and its mirror [`AGENTS.md`](../AGENTS.md)).
