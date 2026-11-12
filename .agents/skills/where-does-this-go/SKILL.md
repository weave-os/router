---
name: where-does-this-go
description: Decide where new router behavior belongs before writing code — which bounded context, package and file. Use for any new feature, fix that adds logic, new I/O, model quirk or flag, and whenever you are about to add a method or field to `proxy.Service`.
---

# Where does this go?

`proxy.Service` is being split into five bounded contexts. New behavior goes in the context that owns its concept, never onto `Service` itself.

## Decision table

| If the change… | It goes in |
|---|---|
| changes wire format or streaming | Protocol & Ingress (pure format conversion stays in `internal/translate`) |
| touches keys, BYOK, OAuth or subscriptions | Credential Brokerage |
| chooses or filters models | Routing Policy (+ the `internal/router/policy` registry for inference purposes) |
| does retry, fallback or provider calls | Inference Execution (port: `inference.Executor`) |
| handles commands, compaction, handover or loops | Conversation Lifecycle |
| is a model quirk | catalog data (see `add-model-or-quirk`) |
| is new I/O | a port in the owning context, an adapter package, wiring in `cmd/router` (see `add-context-port`) |

Accounting, shadows/experiments and the semantic cache stay in the orchestrator for now. The proxy guide's "Where new behavior goes" section in [internal/proxy/CLAUDE.md](../../../internal/proxy/CLAUDE.md) maps each context to the files that own it today.

## Steps

1. Find where the concept lives today and read the owning package's `CLAUDE.md`:
   ```bash
   rg -l '<concept>' internal --type go
   go list ./internal/...
   ls internal/<pkg>/CLAUDE.md
   ```
2. If a package for the context exists, add the logic there. If not, add a **new focused file** next to the files that own the concept, as a free function or a small collaborator struct whose dependencies are explicit parameters. Pass request-scoped values as arguments.
3. Write the placement in the PR description: `Placement: <context/package>, because <reason>.`

## Red flags: stop and re-place

- a new `Service` method or field (`func (s *Service)`, a new field in `type Service struct`)
- a new `context.WithValue` key or context-key type
- `strings.HasPrefix(model` or any model/provider-name string match outside `internal/router/catalog`
- an otel or other adapter import from an inner-ring package
- a raw `os.Getenv` outside `internal/config`

## Verify

```bash
git diff origin/main -- internal/proxy | rg '^\+func \(s \*Service\)|context\.WithValue|strings\.HasPrefix\(model'   # expect no output
make arch-ratchet    # when the target exists
make check-changed
```

Escalate to the package CODEOWNER (`.github/CODEOWNERS`) when the change spans two contexts and no split keeps each half in one.
