---
name: add-flag-or-experiment
description: Add a feature flag, org override, shadow or experiment with an owner, an expiry and a removal plan. Use when gating new behavior, running a shadow evaluation or A/B experiment, or adding an org-overridable setting.
---

# Add a flag or experiment

Flags and experiments are temporary unless stated otherwise. Each one has an owner, a kind and an expiry, and experiments observe without changing what is served.

## Steps

1. Read the registry and reuse its patterns:
   ```bash
   rg -n 'Key\w+\s+Key = |type Definition struct' -A3 internal/flags --type go
   rg -ln 'shadow|experiment' internal --type go
   ```
2. **Register the flag** in `internal/flags` with a typed `Key`, `Kind`, env var if any, `Description`, and `OrgOverridable` only when orgs should change it. Read it through the registry, never with raw `os.Getenv` (see `add-context-port`).
3. **Record owner, kind and expiry** in the PR description and the flag's `Description`:
   - kind: `release` (gates new behavior), `experiment` (measures alternatives), `ops` (kill switch) or `setting` (permanent);
   - experiments expire in **60 days or less**; release flags expire once fully on.
4. **Experiments and shadows are observers.** They read the request and the serving decision and record results; they must not change the model chosen, the attempts made, the response or billing. Run them off the serving path or after the decision is final, and bound their cost and latency.
5. **Default off** for new behavior; the default must preserve today's behavior.
6. Test both flag states, and test that the experiment leaves the serving decision unchanged.

## Removal checklist (copy into the PR description)

- [ ] owner and expiry date
- [ ] the metric or result that decides keep vs remove
- [ ] code paths to delete: `rg -n '<FlagKey>' --type go`
- [ ] registry entry, env var and docs to delete
- [ ] stored org overrides to clean up

## Escalate

Ask the CODEOWNER (`.github/CODEOWNERS`) when an experiment must change what is served, touches billing or credentials, or needs to run longer than 60 days.

## Verify

```bash
go test ./internal/flags/...
go test ./internal/<consuming-package>/... -count=5 -race
make arch-ratchet          # when the target exists
make check-changed
```
