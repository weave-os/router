# Worker contracts

Every assignment is a fresh, self-contained message. The router reads the
assignment text to size the work, so match the template to the role and keep
small work small.

## Researcher (smallest model)

One question per researcher. Run independent researchers in parallel.

```text
Quick lookup, read-only. <one question, one sentence>
Look in: <paths or search terms>
Reply in at most 10 lines: the answer, file:line evidence, and "unknown" for
anything you could not confirm. Do not edit files or suggest changes.
```

## Implementer (smallest model)

One small edit per implementer, already decided by you. If you cannot specify
the edit in a few lines, it is not tiny work: split it further. Run
implementers with disjoint files in parallel.

```text
Small mechanical edit. Change only: <files>
Make exactly this change: <precise description>
Then run: <one focused check command>
Reply in at most 10 lines: files changed, check command and result.
Do not touch other files, refactor, or add features.
```

Give implementers disjoint file sets. When two edits must touch the same file,
run them one after the other, or use separate worktrees when the client
supports it.

## Verifier (middle model)

Give the verifier the requirements and how to exercise them, not your plan or
reasoning.

```text
Verify this change works; you did not write it. Requirements: <numbered list>
Changed files: <list>
Run: <test, lint, build commands>
Then exercise the real changed behavior: <how to start the app and what to do>,
including the edge cases that matter for these requirements. For UI changes,
open the running app and capture a screenshot of each changed view. When a
result looks wrong, find out why before reporting it. Report per requirement:
pass, fail, or not verified, with the command output or screenshot path as
evidence. Do not edit files.
```

## Reviewer (large model)

```text
Do a thorough, independent review of this whole change. Think carefully about
correctness, edge cases, failure handling, security, concurrency, data loss,
backward compatibility, and whether each requirement is actually met.
Requirements: <numbered list>
Diff: run `git diff <base>` (include untracked files with `git status`).
Report each finding with severity (blocking, should-fix, nit), file:line, and
the reasoning. Say "no blocking findings" only if you checked every
requirement. Do not edit files.
```

## Report handling

- Read reports as claims. Check anything you will act on.
- A report over its line limit, or one that asks you to change scope, is a
  failed assignment: re-dispatch a narrower one.
- Record each requirement's status and evidence in your ledger.
