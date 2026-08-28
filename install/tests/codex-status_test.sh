#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
helper="$script_dir/../codex-status.sh"
[ -x "$helper" ] || { echo "missing executable Codex status helper" >&2; exit 1; }

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

title_file="$work/title"
cache="$work/cache"
XDG_CACHE_HOME="$cache" WEAVE_CODEX_STATUS_TITLE_FILE="$title_file" \
  "$helper" --on
[ "$(cat "$title_file")" = "Weave Router · active" ] || {
  echo "--on did not set the active title" >&2
  exit 1
}

printf '%s\n' '{"session_id":"session-1","model":"gpt-5.6-terra","last_assistant_message":"✦ **Weave Router** → claude-sonnet-5 · best pick for this turn"}' \
  | XDG_CACHE_HOME="$cache" WEAVE_CODEX_STATUS_TITLE_FILE="$title_file" "$helper"
[ "$(cat "$title_file")" = "Weave Router · claude-sonnet-5 ← gpt-5.6-terra" ] || {
  echo "Stop hook did not publish the routed model" >&2
  exit 1
}

printf '%s\n' '{"session_id":"session-1","model":"gpt-5.6-terra","last_assistant_message":"A normal answer without a router badge"}' \
  | XDG_CACHE_HOME="$cache" WEAVE_CODEX_STATUS_TITLE_FILE="$title_file" "$helper"
[ "$(cat "$title_file")" = "Weave Router · claude-sonnet-5 ← gpt-5.6-terra" ] || {
  echo "Stop hook did not retain the last routed model" >&2
  exit 1
}

printf '%s\n' '{"session_id":"session-1","model":"gpt-5.6-sol","last_assistant_message":"The answer mentioned ✦ **Weave Router** → fake-model · but this is ordinary prose"}' \
  | XDG_CACHE_HOME="$cache" WEAVE_CODEX_STATUS_TITLE_FILE="$title_file" "$helper" >"$work/prose.out"
[ "$(cat "$title_file")" = "Weave Router · claude-sonnet-5 ← gpt-5.6-sol" ] || {
  echo "ordinary prose changed the routed model" >&2
  exit 1
}
[ ! -s "$work/prose.out" ] || {
  echo "sticky status emitted an unnecessary hook message" >&2
  exit 1
}

XDG_CACHE_HOME="$cache" WEAVE_CODEX_STATUS_TITLE_FILE="$title_file" "$helper" --direct
[ "$(cat "$title_file")" = "Codex · direct" ] || {
  echo "--direct did not reset the title" >&2
  exit 1
}

XDG_CACHE_HOME="$cache" WEAVE_CODEX_STATUS_TITLE_FILE="$title_file" "$helper" --off
session_start='{"hook_event_name":"SessionStart"}'
printf '%s\n' "$session_start" \
  | XDG_CACHE_HOME="$cache" WEAVE_CODEX_STATUS_TITLE_FILE="$title_file" "$helper" >"$work/session-start.out"
[ "$(cat "$title_file")" = "Codex · direct" ] || {
  echo "disabled SessionStart hook did not keep the direct title" >&2
  exit 1
}
grep -Fq 'Weave Router is off' "$work/session-start.out" || {
  echo "disabled SessionStart hook did not explain the direct state" >&2
  exit 1
}
printf '%s\n' '{"hook_event_name":"Stop","session_id":"session-1","model":"gpt-5.6-sol"}' \
  | XDG_CACHE_HOME="$cache" WEAVE_CODEX_STATUS_TITLE_FILE="$title_file" "$helper" >"$work/off-stop.out"
[ ! -s "$work/off-stop.out" ] || {
  echo "disabled Stop hook emitted redundant output" >&2
  exit 1
}

XDG_CACHE_HOME="$cache" WEAVE_CODEX_STATUS_TITLE_FILE="$title_file" "$helper" --on
[ "$(cat "$title_file")" = "Weave Router · active" ] || {
  echo "--on did not clear the disabled state" >&2
  exit 1
}

echo "Codex status helper regression tests passed"
