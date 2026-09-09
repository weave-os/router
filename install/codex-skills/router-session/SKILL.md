---
name: router-session
description: "Print the session id that correlates this Codex session in Weave Router telemetry and logs."
---

<!-- weave-router managed router-session skill -->

When the user invokes `$router-session`, run this skill's `scripts/emit.sh` using the
absolute path shown in this skill's `<path>` (its directory + `/scripts/emit.sh`).
It takes no arguments.

The script prints the session id on its own line. Report that id back in one line,
formatted as inline code so the user can copy it. Do not send a user message and do
not type a Codex slash command.

The router carries this id as `Session-Id` on every request and stores it for
analytics joins, so it is also the lookup key in the router dashboard.

The Weave Router also installs a `UserPromptSubmit` hook that answers
`$router-session` directly, with no model turn at all. This skill is the fallback
for sessions where that hook is unavailable.
