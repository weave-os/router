---
name: router-feedback
description: "Submit feedback about a Weave Router decision or model performance."
---

<!-- weave-router managed router-feedback skill -->

When the user invokes `$router-feedback <feedback>` (or `$rf`), run this
skill's `scripts/emit.sh` with the feedback text as arguments. Do not send a
user message and do not type a Codex slash command. The script prints a
leading-space `/router-feedback` line; the Weave Router intercepts that exec
output, records the feedback, and continues this turn. Then report the
router's response.
