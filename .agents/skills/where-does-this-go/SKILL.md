---
name: where-does-this-go
description: Decide where new router behavior belongs before writing code — which layer, atom, package and file. Use for any new feature, fix that adds logic, new I/O, model quirk or flag, and whenever you are about to add a method or field to `proxy.Service`.
---

# Where does this go?

The router has two layers and one owner package per atom; [docs/ROUTER_ARCHITECTURE.md](../../../docs/ROUTER_ARCHITECTURE.md) is the reference. New behavior goes in the owner of its concept, never onto `proxy.Service`.

## First: which layer?

- It decides which models **can** serve (a hard constraint for every strategy): **serving layer**.
- It decides which eligible model **should** serve: **router layer**, inside a strategy or a helper the strategy calls.

## Decision table

| If the change… | It goes in |
|---|---|
| parses or renders a client wire format, or streams | `internal/ingress`; pure format conversion in `internal/translate` |
| touches keys, BYOK, OAuth or subscriptions | `internal/credentials` |
| removes a model from what may serve (eligibility, exclusions, context window, capability) | `internal/router/candidates` |
| is an explicit user or client command, policy pin, utility hard pin or usage bypass | `internal/routingpolicy/overrides` |
| picks which strategy serves an installation or session | `internal/router` strategy selection, `internal/router/sessionstrategy` |
| changes how one strategy chooses among eligible models | that strategy: `internal/router/hmm`, `internal/router/decisionmodel`, or the HTTP harness in `internal/router/policy` |
| reads classifier confidence, margin, policy group or arm scores | `internal/router/hmm` only |
| is pin reuse, stickiness or the cost gate shared by strategies | `internal/router/continuity` |
| is stay-or-switch economics | `internal/router/planner` |
| is escalation scoring, judging or the floor | `internal/router/escalation`, `internal/router/escalation/floor`, `internal/router/llmescalation` |
| does retry, fallback or provider calls | `internal/inference` port, `internal/dispatch` |
| stores or reads session pins | `internal/router/sessionpin`; HMM-only pin fields in `internal/router/hmm/pinstate` |
| rewrites history on a model switch | `internal/router/handover` |
| records usage, charges or telemetry | `internal/billing`, `internal/telemetry` |
| is a model quirk | catalog data (see `add-model-or-quirk`) |
| is new I/O | a port in the owning package, an adapter package, wiring in `cmd/router` (see `add-context-port`) |

## Steps

1. Find where the concept lives today:
   ```bash
   rg -l '<concept>' internal --type go
   go list ./internal/...
   ```
2. Add the logic to the owner package. If the owner has no file for it, add a **new focused file** there, as a free function or a small collaborator struct whose dependencies are explicit parameters. Pass request-scoped values as arguments.
3. Write the placement in the PR description: `Placement: <layer>/<package>, because <reason>.`

## Red flags: stop and re-place

- a new `Service` method or field (`func (s *Service)`, a new field in `type Service struct`)
- serving-layer code that branches on a named strategy, a decision reason string or an HMM-only field
- a strategy that imports a serving-layer adapter (`auth`, `billing`, `subscriptions`, `credentials`, `dispatch`, `postgres`, `providers/<name>`)
- a new `context.WithValue` key or context-key type
- `strings.HasPrefix(model` or any model/provider-name string match outside `internal/router/catalog`
- an otel or other adapter import from an inner-ring package
- a raw `os.Getenv` outside `internal/config`

## Verify

```bash
git diff origin/main -- internal/proxy | rg '^\+func \(s \*Service\)|context\.WithValue|strings\.HasPrefix\(model'   # expect no output
go test ./internal/architecture/...
make arch-ratchet
make check-changed
```

Escalate to the package CODEOWNER (`.github/CODEOWNERS`) when the change spans two atoms and no split keeps each half in one owner.
