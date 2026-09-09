#!/usr/bin/env bash
# Print this Codex session's id. Local only -- no router call, no model turn.
set -uo pipefail

if [ -n "${CODEX_SESSION_ID:-}" ]; then
  printf '%s\n' "$CODEX_SESSION_ID"
  exit 0
fi

# Fall back to the newest rollout transcript, whose filename ends in the session
# id (…/sessions/YYYY/MM/DD/rollout-<timestamp>-<session-id>.jsonl). The
# date-partitioned path and leading timestamp make a lexical sort newest-last.
sessions_dir="${CODEX_HOME:-$HOME/.codex}/sessions"
if [ ! -d "$sessions_dir" ]; then
  echo "session id unavailable: no sessions directory at $sessions_dir" >&2
  exit 1
fi

newest="$(find "$sessions_dir" -type f -name 'rollout-*.jsonl' 2>/dev/null | sort | tail -n 1)"
if [ -z "$newest" ]; then
  echo "session id unavailable: no rollout transcript under $sessions_dir" >&2
  exit 1
fi

session_id="$(
  basename "$newest" |
    sed -E 's/^rollout-.*-([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})\.jsonl$/\1/'
)"
case "$session_id" in
  ''|*.jsonl)
    echo "session id unavailable: could not parse a session id from $newest" >&2
    exit 1
    ;;
esac
printf '%s\n' "$session_id"
