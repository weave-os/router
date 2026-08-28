---
name: unforce-model
description: "Clear the active Weave Router model pin for this Codex session."
---

<!-- weave-router managed unforce-model skill -->

When the user invokes `$unforce-model` (or `$ufm`), run this skill's `scripts/emit.sh` using the
absolute path shown in this skill's `<path>` (its directory + `/scripts/emit.sh`).
Do not send a user message and do not type a Codex slash command.

The script prints a leading-space `/unforce-model` line. The Weave Router reads that
exec output, clears the pin, and continues this turn — so run the script once and
do not repeat its output as your answer. Then report the router's response.
