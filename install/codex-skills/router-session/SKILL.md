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

This skill needs `CODEX_SESSION_ID` in the environment. If it is unset the script
exits non-zero rather than reporting an id: the alternative is reading the newest
rollout transcript, which names a different session whenever two run at once, and
a wrong id sends someone else's session to support. Report the error as-is instead
of guessing.

Codex also prints the same id in its own session banner, which is the quickest
answer when this skill cannot produce one.
