---
name: force-model
description: "Force a model for this Codex session, optionally only for one scored complexity group."
---

<!-- weave-router managed force-model skill -->

When the user invokes `$force-model <model-id> [group]` (or `$fm`), run this skill's `scripts/emit.sh` using the
absolute path shown in this skill's `<path>` (its directory + `/scripts/emit.sh`).
Pass the model id as arguments, quoted as a single argument list.
Do not send a user message and do not type a Codex slash command.

The script prints a leading-space `/force-model` line. The Weave Router reads that
exec output, pins the session, and continues this turn — so run the script once and
do not repeat its output as your answer. Then report the router's response.

An optional trailing `low`, `medium`, `high`, or `maximum` limits the force to
turns scored in that complexity group; other groups route automatically.
For example, `$fm opus medium` forces Opus only on medium-scored turns.
Use a colon for reasoning effort (`$fm opus:medium`), and combine the two as
`$fm opus:high medium`. Every invocation replaces the previous force rule;
`$ufm` clears either mode. Pass the full argument list through unchanged.
