---
name: router-feedback
description: "Submit feedback about a Weave Router decision or model performance."
---

<!-- weave-router managed router-feedback skill -->

When the user invokes `$router-feedback <feedback>` (or `$rf`), run this skill's `scripts/emit.sh` using the
absolute path shown in this skill's `<path>` (its directory + `/scripts/emit.sh`).
Pass the feedback text as arguments, quoted as a single argument list.
Do not send a user message and do not type a Codex slash command.

The script prints a leading-space `/router-feedback` line. The Weave Router reads that
exec output, records the feedback, and continues this turn — so run the script once and
do not repeat its output as your answer. Then report the router's response.
