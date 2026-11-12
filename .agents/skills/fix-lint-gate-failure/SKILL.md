---
name: fix-lint-gate-failure
description: Fix a failing architecture ratchet, complexity, file-size, `dupl` or inference-boundary gate by splitting code along a concept boundary. Use when `make arch-ratchet`, golangci-lint, `go test ./internal/architecture/...` or `make inference-boundary` fails locally or in CI.
---

# Fix a lint or ratchet gate failure

The gates stop the code from getting worse. A failure means the change put new code in the wrong shape or place; fix the shape, not the gate.

## Steps

1. **Reproduce and read the failure.** Note the gate, the symbol or file, the limit and the measured value.
   ```bash
   make arch-ratchet          # when the target exists
   golangci-lint run --new-from-rev=origin/main ./...
   go test ./internal/architecture/...
   make inference-boundary
   ```
2. **Find the concept boundary.** Read the function or file and name the distinct jobs it does (parse, decide, call, record…). Use `where-does-this-go` to see which context each job belongs to.
3. **Split along that boundary.**
   - Function too long, complex, deep or wide: extract each job into a named function or small collaborator with explicit inputs and outputs. Replace long parameter lists with a request struct only when the fields belong together.
   - File too large: move one concept into its own focused file.
   - `proxy.Service` grew or a proxy context key appeared: turn the new method into a free function or collaborator and pass the values as parameters.
   - Model-name string match: replace it with a typed catalog field (`add-model-or-quirk`).
   - `dupl` clone: extract the shared logic once, in the package that owns it.
   - Inference-boundary violation: describe the call with `inference.InvocationRequest` and execute through the policy-resolved plan.
   Don't chop code into arbitrary `helper1`/`part2` functions; each piece must have a name that says what it does.
4. Keep behavior identical. If goldens exist for the path, they must pass without `-update` (`golden-traces`).

## Never

- edit a baseline or limit upward
- add `//nolint`
- add an entry to `internal/architecture/ratchet_exceptions.json` to pass your own PR

## Escalate

If a split truly isn't possible (generated code, a protocol switch that must stay exhaustive), stop and ask the package CODEOWNER (`.github/CODEOWNERS`) for a reviewed exception, giving the gate, the value and why a split doesn't work.

## Verify

```bash
make arch-ratchet          # when the target exists
go test ./internal/architecture/...
make inference-boundary
make check-changed
git diff origin/main | rg '^\+.*//nolint'   # expect no output
```
