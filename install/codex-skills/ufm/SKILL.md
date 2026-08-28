---
name: ufm
description: "Alias for unforce-model — clear the active Weave Router model pin."
---

<!-- weave-router managed ufm skill -->

When the user invokes `$ufm`, run this skill's `scripts/emit.sh`. Do not send
a user message and do not type a Codex slash command. The script prints a
leading-space `/unforce-model` line; the Weave Router intercepts that exec
output, clears the pin, and continues this turn. Then report the router's
response.
