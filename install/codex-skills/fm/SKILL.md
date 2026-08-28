---
name: fm
description: "Alias for force-model — pin this Codex session to a specific model through the Weave Router."
---

<!-- weave-router managed fm skill -->

When the user invokes `$fm <model-id>`, run this skill's `scripts/emit.sh`
with the model id as arguments. Do not send a user message and do not type a
Codex slash command. The script prints a leading-space `/force-model` line;
the Weave Router intercepts that exec output, pins the session, and continues
this turn. Then report the router's response.
