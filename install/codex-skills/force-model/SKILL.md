---
name: force-model
description: "Pin this Codex session to a specific model through the Weave Router."
---

<!-- weave-router managed force-model skill -->

When the user invokes `$force-model <model-id>` (or `$fm`), run this skill's
`scripts/emit.sh` with the model id as arguments. Do not send a user message
and do not type a Codex slash command. The script prints a leading-space
`/force-model` line; the Weave Router intercepts that exec output, pins the
session, and continues this turn. Then report the router's response.
