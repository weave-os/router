#!/usr/bin/env bash
# <!-- weave-router managed codex status -->
#
# Codex lifecycle hook for the Weave Router. Codex passes a JSON object on
# stdin; the Stop hook includes the last assistant message, which carries the
# router's routed-model marker when the selected model changes. The helper
# keeps the last known routed model per session and reflects it in the terminal
# title, so the active router remains visible between turns without injecting
# another message into the conversation.
#
# Savings come from the router, not from local arithmetic. Codex records its
# own requested model on every turn and never the served one, so the per-turn
# pricing the Claude Code statusline does cannot be reproduced here — it would
# price both sides of the comparison at the same model and report zero. The
# router already sums the real thing per session, so the hook fetches
# GET <base>/v1/sessions/<id>/cost and renders what it returns. The fetch runs
# in a detached subshell writing a cache the NEXT turn reads, so no turn ever
# blocks on the network, and every failure path leaves the title model-only.

set -euo pipefail

state_root="${XDG_CACHE_HOME:-$HOME/.cache}/weave-router/codex"
helper_dir="$(cd "$(dirname "$0")" 2>/dev/null && pwd -P)"
disabled_marker="$helper_dir/.weave-router-disabled"
router_badge_sentinel=$'⁣⁠⁣⁠'
# Must stay verbatim in sync with install.sh / uninstall.sh: the endpoint read
# below is scoped to this block so a key-shaped string elsewhere in the user's
# config.toml is never adopted.
codex_begin_marker="# >>> weave-router managed (do not edit between markers) >>>"
codex_end_marker="# <<< weave-router managed <<<"

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

cost_file_for() {
  local id
  id="$(safe_session_id "$1")" || return 1
  printf '%s/%s.cost' "$state_root" "$id"
}

# Reads the router base URL and key out of the Codex config this install owns.
# Resolved from the helper's own location first so a project-scope install never
# reads (or leaks) the user-scope key: the project helper lives in the same
# .codex directory as its config, while the user-scope helper sits in ~/.weave
# and reads ~/.codex. Values are scoped to the managed block so a key-shaped
# string the user wrote elsewhere in the file is never adopted. awk, not a TOML
# parser, because the Codex target deliberately does not require jq for config
# reads.
read_codex_endpoint() {
  local config=""
  # Project/custom installs name the helper weave-status.sh and keep it next
  # to their config.toml. Falling through to ~/.codex would fetch another
  # installation's session cost with that key. The user-scope helper
  # (codex-status.sh in ~/.weave) has no adjacent config and owns HOME.
  if [ -f "$helper_dir/config.toml" ]; then
    config="$helper_dir/config.toml"
  elif [ "$(basename "$0")" != "weave-status.sh" ] && [ -f "$HOME/.codex/config.toml" ]; then
    config="$HOME/.codex/config.toml"
  fi
  [ -n "$config" ] || return 0
  awk -v begin="$codex_begin_marker" -v end="$codex_end_marker" '
    $0 == begin { inblk = 1; next }
    $0 == end   { inblk = 0; next }
    !inblk { next }
    match($0, /base_url[[:space:]]*=[[:space:]]*"[^"]*"/) {
      v = substr($0, RSTART, RLENGTH)
      sub(/^.*=[[:space:]]*"/, "", v); sub(/"$/, "", v)
      url = v
    }
    match($0, /"X-Weave-Router-Key"[[:space:]]*=[[:space:]]*"[^"]*"/) {
      v = substr($0, RSTART, RLENGTH)
      sub(/^.*=[[:space:]]*"/, "", v); sub(/"$/, "", v)
      key = v
    }
    END { if (url != "" && key != "") printf "%s\n%s\n", url, key }
  ' "$config" 2>/dev/null || true
}

# Kicks off a detached fetch of this session's committed cost. The result lands
# in a cache the next turn reads; this turn renders whatever is already there.
# Fire-and-forget on purpose — a slow or unreachable router must never stall a
# Codex turn, and every failure simply leaves the previous cache in place.
refresh_session_cost() {
  local id="$1" file="$2"
  [ "${WEAVE_CODEX_STATUS_SAVINGS:-1}" = "0" ] && return 0
  command -v curl >/dev/null 2>&1 || return 0

  local endpoint base_url key
  endpoint="$(read_codex_endpoint)" || return 0
  base_url="$(printf '%s' "$endpoint" | sed -n 1p)"
  key="$(printf '%s' "$endpoint" | sed -n 2p)"
  [ -n "$base_url" ] && [ -n "$key" ] || return 0

  mkdir -p "$state_root" 2>/dev/null || return 0
  chmod 700 "$state_root" 2>/dev/null || true

  (
    exec </dev/null
    # mkdir is the portable atomic test-and-set. A crashed holder would block
    # refreshes forever, so a lock older than the fetch timeout is reclaimed.
    lock="$file.lock"
    if ! mkdir "$lock" 2>/dev/null; then
      lock_mtime="$(stat -c %Y "$lock" 2>/dev/null || stat -f %m "$lock" 2>/dev/null)" || lock_mtime=0
      lock_now="$(date +%s 2>/dev/null)" || lock_now=0
      if [ "${lock_mtime:-0}" -le 0 ] || [ $(( lock_now - lock_mtime )) -le 30 ]; then
        exit 0
      fi
      rm -rf "$lock" 2>/dev/null
      mkdir "$lock" 2>/dev/null || exit 0
    fi
    trap 'rmdir "$lock" 2>/dev/null' EXIT

    # A file:// base is the offline/test seam: curl reads it as the response
    # body directly, so the endpoint path is meaningless for it.
    url="${base_url%/}"
    case "$url" in
      file://*) ;;
      *) url="${url%/v1}/v1/sessions/$id/cost" ;;
    esac
    body="$(curl -fsS --max-time 5 -H "X-Weave-Router-Key: $key" "$url" 2>/dev/null)" || exit 0
    # savings_usd is the router's own (requested - actual). A body without it
    # (404, error envelope, older router) writes nothing and leaves the cache.
    savings="$(printf '%s' "$body" | jq -r '.savings_usd // empty' 2>/dev/null)" || exit 0
    case "$savings" in
      ''|*[!0-9.eE+-]*) exit 0 ;;
    esac
    tmp="$file.tmp.$$"
    mkdir -p "$(dirname "$file")" 2>/dev/null
    if printf '%s' "$savings" >"$tmp" 2>/dev/null; then
      chmod 600 "$tmp" 2>/dev/null
      mv "$tmp" "$file" 2>/dev/null
    fi
    rm -f "$tmp" 2>/dev/null
  ) >/dev/null 2>&1 &
  disown 2>/dev/null || true
}

# Renders the cached savings as a display clause, or nothing. Values below a
# cent read as "<$0.01" rather than "$0.00", which would be indistinguishable
# from "the router ran and did not beat your selection". Negative totals are
# omitted entirely: the router picked a pricier model for quality on this
# session and "saved -$0.02" is a worse answer than staying quiet.
savings_clause() {
  local file="$1" raw
  [ "${WEAVE_CODEX_STATUS_SAVINGS:-1}" = "0" ] && return 0
  [ -f "$file" ] || return 0
  raw="$(cat "$file" 2>/dev/null)" || return 0
  case "$raw" in
    ''|*[!0-9.eE+-]*) return 0 ;;
  esac
  awk -v v="$raw" 'BEGIN{
    v = v + 0
    if (v < 0.005) { exit }
    if (v < 0.01)  { printf " · saved <$0.01"; exit }
    printf " · saved $%.2f", v
  }' 2>/dev/null || true
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

# The cache holds the previous turn's fetch; the refresh below serves the next
# one. Router telemetry is written asynchronously anyway, so a just-finished
# turn would not be included even in a blocking read — reading first and
# refreshing after costs a turn of freshness and buys never blocking Codex.
savings=""
cost_file=""
if cost_file="$(cost_file_for "$session_id" 2>/dev/null)"; then
  savings="$(savings_clause "$cost_file")"
  refresh_session_cost "$(safe_session_id "$session_id")" "$cost_file"
fi

if [ -n "$routed_model" ] && [ -n "$requested_model" ] && [ "$routed_model" != "$requested_model" ]; then
  title="Weave Router · $routed_model ← $requested_model$savings"
elif [ -n "$routed_model" ]; then
  title="Weave Router · $routed_model$savings"
elif [ -n "$requested_model" ]; then
  title="Weave Router · active ← $requested_model$savings"
else
  title="Weave Router · active$savings"
fi
emit_title "$title"
if [ -n "$marker_model" ] || [ -n "$force_model" ]; then
  printf '%s' "$title" | jq -Rc '{systemMessage: .}'
fi
