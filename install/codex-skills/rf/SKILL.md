---
name: rf
description: "Alias for router-feedback — submit feedback about a Weave Router decision or model performance."
---

<!-- weave-router managed rf skill -->

When the user invokes `$rf <feedback>`, run this skill's `scripts/emit.sh` using the
absolute path shown in this skill's `<path>` (its directory + `/scripts/emit.sh`).
Pass the feedback text as arguments, quoted as a single argument list.
Do not send a user message and do not type a Codex slash command.

The script prints a leading-space `/router-feedback` line. The Weave Router reads that
exec output, records the feedback, and continues this turn — so run the script once and
do not repeat its output as your answer. Then report the router's response.
