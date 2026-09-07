# Installer endpoint trust fix

## Problem

Project-scoped Claude Code installs intentionally split configuration: the
tracked `.claude/settings.json` stores the router endpoint while the ignored
`.claude/settings.local.json` stores each developer's router key. `update`
currently carries the endpoint forward without checking its provenance, then
validates and persists the key against that endpoint. A repository-controlled
endpoint can therefore exfiltrate the key and become a persistent Claude Code
MITM.

## Proposed solution

- Reuse the installer’s endpoint-provenance model for `update`, before any
  settings write or health/key probe.
- Treat an explicit `--base-url`, the hosted default, a same-file endpoint/key,
  an untracked endpoint file, or a matching untracked project marker as trusted.
- Treat a tracked endpoint with no independent user proof as untrusted,
  including when the key came from `WEAVE_ROUTER_KEY`; require an explicit
  `--base-url` to approve it.
- Remove the `models` command’s environment-key bypass so both commands enforce
  the same trust boundary.
- Add offline regression coverage proving hostile project endpoints receive no
  request, while hosted, explicitly approved, and marked self-hosted layouts
  continue to work.

## Files

- `install/install.sh`: endpoint/key source helpers and trust gate.
- `install/tests/models_test.sh`: model-command environment-key regression,
  update exfiltration regression, and trusted-layout cases.

## Implementation order

1. Refactor source lookup into reusable helpers without changing accepted
   trusted layouts.
2. Gate `update` after key resolution and before identity/config writes or
   network probes.
3. Apply the same gate to `models` for environment-provided keys.
4. Add tests and run the focused installer suites, then the full pre-commit
   checks available for the router module.

## Risks and decisions

- A project-scoped custom endpoint must have been installed by this installer
  (the ignored marker) or be explicitly repeated with `--base-url`; this is a
  deliberate safety trade-off for unattended updates.
- User-scope and `--dir` installs remain trusted because their endpoint and key
  are not supplied by a checked-out repository.
- No database, router API, or migration changes are needed.
