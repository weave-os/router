---
name: add-model-or-quirk
description: Add a model, provider binding, price or per-model behavior quirk as typed catalog data in `internal/router/catalog`. Use when adding or changing a model, or when code needs to behave differently for some models or providers.
---

# Add a model or quirk

`internal/router/catalog` is the single source of truth for per-model data. Code reads typed catalog fields; it never branches on a model or provider name string.

## Steps

1. Read [internal/router/catalog/CLAUDE.md](../../../internal/router/catalog/CLAUDE.md) and find the existing fields:
   ```bash
   rg -n 'type (Model|ProviderBinding) struct' -A40 internal/router/catalog --type go
   ```
2. **New model:** add its catalog entry (tier, provider bindings, pricing, caps) following neighboring entries.
3. **New quirk:** if no field expresses it, add a typed field (a bool, enum or small struct named for the behavior, e.g. "rejects X", not for the model) on the model or the provider binding, whichever the quirk depends on. Set it in data for every affected entry.
4. Change the code that needs the behavior to read the field through the catalog lookup it already has. Remove any name match it replaces.
5. Add a catalog test asserting the field's value for the affected entries, and a behavior test in the consuming package driven by the field rather than a model name.
6. Regenerate derived artifacts: `make generate-statusline` when model or pricing data changed.

## Never

`strings.HasPrefix(model, …)`, `strings.Contains(model, …)`, `strings.EqualFold` on model/provider names, or name maps outside `internal/router/catalog`.

## Escalate

Ask the catalog CODEOWNER (`.github/CODEOWNERS`) before changing pricing for an existing model or removing a model that may be pinned in live sessions.

## Verify

```bash
go test ./internal/router/catalog/...
git diff origin/main -- internal ':!internal/router/catalog' | rg '^\+.*strings\.(HasPrefix|Contains|EqualFold)\(.*(model|provider)'   # expect no output
make arch-ratchet          # when the target exists
make check-changed
```
