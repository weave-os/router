# PR loop

1. Commit only files in the approved plan. Open one PR with a short summary,
   the requirement list, and verification evidence.
2. After each push, wait 120 seconds, then read checks and review comments for
   the latest head only.
3. Batch all actionable feedback into one fix, verify it, push once.
4. Stop after five correction pushes or 45 minutes of watching, and report
   what is still failing or unresolved.
5. Leave the PR open for a human to merge. Never merge, deploy, or publish.
