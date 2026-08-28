---
name: fm
description: "Alias for force-model — pin this Codex session to a specific model through the Weave Router."
---

<!-- weave-router managed fm skill -->

When the user invokes `$fm <model-id>`, run this skill's `scripts/emit.sh` using the
absolute path shown in this skill's `<path>` (its directory + `/scripts/emit.sh`).
Pass the model id as arguments, quoted as a single argument list.
Do not send a user message and do not type a Codex slash command.

The script prints a leading-space `/force-model` line. The Weave Router reads that
exec output, pins the session, and continues this turn — so run the script once and
do not repeat its output as your answer. Then report the router's response.
