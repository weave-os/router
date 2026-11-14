# Verification and review

Run both after implementation and again after any fix. Both workers start
fresh and receive only their assignment.

## Verification

- Use the verifier template. List every requirement and the exact commands.
- Visual or interactive changes need the real app running and screenshots of
  each changed view; a passing unit test does not verify a UI.
- A requirement the verifier could not exercise stays "not verified" and goes
  in the final report.

## Independent review

- Use the reviewer template. Give the requirements and the diff range only;
  never the implementation conversation or your own conclusions.
- Address every blocking finding, then re-verify and re-review the new head.
  Results for an older head are stale.
- You may decline a should-fix or nit with a stated reason.
