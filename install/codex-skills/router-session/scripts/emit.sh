#!/usr/bin/env bash
# Print this Codex session's id. Local only -- no router call, no model turn.
set -uo pipefail

if [ -n "${CODEX_SESSION_ID:-}" ]; then
  printf '%s\n' "$CODEX_SESSION_ID"
  exit 0
fi

# No CODEX_SESSION_ID means there is no way to identify *this* session from here.
# Reading the newest rollout transcript would name whichever session wrote last,
# which is a different session whenever two run concurrently -- and a wrong id
# silently attributes feedback to someone else's session. Fail instead.
echo "session id unavailable: CODEX_SESSION_ID is not set in this environment." >&2
echo "Codex prints the same id in its session banner." >&2
exit 1
