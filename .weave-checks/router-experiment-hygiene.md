---
name: Router Experiment Hygiene
description: Flags ownerless temporary serving controls and shadow evaluations that can affect live decisions
intelligence: medium
files: **/*.go
---

## When to Check

Review only changed non-test Go under `internal/` or `cmd/`, excluding generated code and
`internal/architecture/testdata/`. Otherwise PASS immediately. Continue only when the PR introduces
a serving feature flag, shadow evaluation or temporary routing heuristic, or changes a shadow
path's side effects. Otherwise PASS immediately.

## Flag

- A new experimental flag, shadow or temporary heuristic with no identifiable owner or removal
  condition in the supplied code/design context. Name the control and the missing item. An owner
  may be a role or owning component; a removal condition may be a promotion/rollback decision,
  measurable exit criterion or deadline. Do not require personal names, a ticket or a fixed date.
- A shadow path that mutates a live decision, execution plan, pin, eligibility set, credential
  choice, retry state or other state subsequently used to serve requests. Trace the shadow write
  through the shared object/store to a serving read; include aliases, callbacks and future turns.
  A returned comparison result alone does not prove a live mutation.

Use a changed line and concrete evidence, not a hypothetical race or an assumption that missing
context means missing ownership. If the supplied diff/context cannot establish the temporary
control or the shadow-to-serving path, PASS. Report one finding per underlying defect, and do not
repeat anything already enforced by a linter or architecture test.

## Do Not Flag

- Existing unchanged flags/heuristics, pure moves, tests, fixtures, generated `internal/sqlc`, other
  generated code and docs. Do not require retroactive metadata for existing experiments.
- Permanent supported configuration, ordinary algorithm thresholds or reliability controls whose
  stable contract is evident. A boolean or an environment variable is not automatically an experiment.
- An owner and exit criterion stated once in supplied PR/design context rather than repeated at
  every call site; concise metadata is enough.
- Isolated shadow state, comparison logs/metrics, or observability-only counters never read by the
  serving path. Do not infer a write merely because shadow and serving share a type.
- An explicitly serving experiment arm or deliberate promotion to serving behavior. It still needs
  experiment ownership/removal criteria, but it is not a shadow leak just because decisions change.
- Renaming/removing a control, promoting it to documented permanent behavior, or simplifying an
  existing experiment without adding a new temporary mechanism.
