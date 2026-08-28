#!/usr/bin/env bash
# <!-- weave-router managed codex status -->
#
# Codex lifecycle hook for the Weave Router. Codex passes a JSON object on
# stdin; the Stop hook includes the last assistant message, which carries the
# router's routed-model marker when the selected model changes. The helper
# keeps the last known routed model per session and reflects it in the terminal
# title, so the active router remains visible between turns without injecting
# another message into the conversation.

set -euo pipefail

state_root="${XDG_CACHE_HOME:-$HOME/.cache}/weave-router/codex"
helper_dir="$(cd "$(dirname "$0")" 2>/dev/null && pwd -P)"
disabled_marker="$helper_dir/.weave-router-disabled"
router_badge_sentinel=$'⁣⁠⁣⁠'

emit_title() {
  local title="$1"
  if [ -n "${WEAVE_CODEX_STATUS_TITLE_FILE:-}" ]; then
    printf '%s\n' "$title" >"$WEAVE_CODEX_STATUS_TITLE_FILE"
  elif [ -w /dev/tty ]; then
    printf '\033]0;%s\007' "$title" >/dev/tty
  fi
}

safe_session_id() {
  local id="$1"
  case "$id" in
    ''|*[!A-Za-z0-9._-]*) return 1 ;;
  esac
  [ "${#id}" -le 128 ] || return 1
  printf '%s' "$id"
}

safe_display_value() {
  printf '%s' "$1" | sed 's/[^A-Za-z0-9._:\/-]//g' | cut -c1-128
}

state_file_for() {
  local id
  id="$(safe_session_id "$1")" || return 1
  printf '%s/%s.state' "$state_root" "$id"
}

read_state() {
  local file="$1" key value
  [ -f "$file" ] || return 0
  while IFS='=' read -r key value; do
    case "$key" in
      routed_model) routed_model="$value" ;;
    esac
  done <"$file"
}

write_state() {
  local file="$1" tmp
  mkdir -p "$state_root"
  chmod 700 "$state_root"
  tmp="$(mktemp "$state_root/.state.XXXXXX")"
  printf 'requested_model=%s\nrouted_model=%s\n' "$requested_model" "$routed_model" >"$tmp"
  chmod 600 "$tmp"
  mv "$tmp" "$file"
}

set -e

case "${1:-hook}" in
  --direct)
    emit_title "Codex · direct"
    exit 0
    ;;
  --on)
    rm -f "$disabled_marker"
    emit_title "Weave Router · active"
    exit 0
    ;;
  --off)
    [ ! -L "$disabled_marker" ] || exit 0
    : >"$disabled_marker"
    chmod 600 "$disabled_marker"
    emit_title "Codex · direct"
    exit 0
    ;;
esac

payload="$(cat)"
[ -n "$payload" ] || exit 0
command -v jq >/dev/null 2>&1 || exit 0
jq -e . >/dev/null 2>&1 <<<"$payload" || exit 0

hook_event_name="$(jq -r '.hook_event_name // ""' <<<"$payload")"
if [ "$hook_event_name" = "SessionStart" ]; then
  if [ -f "$disabled_marker" ]; then
    emit_title "Codex · direct"
    jq -cn '{systemMessage:"Codex direct · Weave Router is off"}'
  else
    emit_title "Weave Router · active"
    jq -cn '{systemMessage:"Weave Router active · routed model appears in the terminal title"}'
  fi
  exit 0
fi

if [ -f "$disabled_marker" ]; then
  exit 0
fi

requested_model="$(safe_display_value "$(jq -r '.model // ""' <<<"$payload")")"
routed_model=""
session_id="$(jq -r '.session_id // ""' <<<"$payload")"
last_assistant_message="$(jq -r '.last_assistant_message // ""' <<<"$payload")"

file=""
if file="$(state_file_for "$session_id" 2>/dev/null)"; then
  read_state "$file"
fi

# The marker is intentionally matched only at the beginning of the assistant
# message. Do not treat ordinary prose that mentions the heading as metadata.
first_line="${last_assistant_message%%$'\n'*}"
marker_model=""
force_model=""
case "$first_line" in
  "${router_badge_sentinel}✦ **Weave Router** → "*)
    marker_model="${first_line#"${router_badge_sentinel}✦ **Weave Router** → "}"
    marker_model="${marker_model%% ·*}"
    ;;
  "✦ **Weave Router** → "*)
    marker_model="${first_line#"✦ **Weave Router** → "}"
    marker_model="${marker_model%% ·*}"
    ;;
esac
case "$first_line" in
  "Weave Router: force-model applied: "*" ("*)
    force_model="${first_line#"Weave Router: force-model applied: "}"
    force_model="${force_model%% (*}"
    ;;
esac
if [ -n "$marker_model" ]; then
  routed_model="$(safe_display_value "$marker_model")"
elif [ -n "$force_model" ]; then
  routed_model="$(safe_display_value "$force_model")"
else
  routed_model="$(safe_display_value "$routed_model")"
fi

if [ -n "$file" ]; then
  write_state "$file"
fi

if [ -n "$routed_model" ] && [ -n "$requested_model" ] && [ "$routed_model" != "$requested_model" ]; then
  title="Weave Router · $routed_model ← $requested_model"
elif [ -n "$routed_model" ]; then
  title="Weave Router · $routed_model"
elif [ -n "$requested_model" ]; then
  title="Weave Router · active ← $requested_model"
else
  title="Weave Router · active"
fi
emit_title "$title"
if [ -n "$marker_model" ] || [ -n "$force_model" ]; then
  printf '%s' "$title" | jq -Rc '{systemMessage: .}'
fi
