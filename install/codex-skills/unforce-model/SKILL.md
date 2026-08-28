---
name: unforce-model
description: "Clear the active Weave Router model pin for this Codex session."
---

<!-- weave-router managed unforce-model skill -->

When the user invokes `$unforce-model` (or `$ufm`), run this skill's
`scripts/emit.sh`. Do not send a user message and do not type a Codex slash
command. The script prints a leading-space `/unforce-model` line; the Weave
Router intercepts that exec output, clears the pin, and continues this turn.
Then report the router's response.
