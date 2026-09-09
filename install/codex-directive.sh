#!/usr/bin/env bash
# <!-- weave-router managed codex directive -->
#
# Codex UserPromptSubmit hook for the Weave Router. Codex passes a JSON object
# on stdin carrying the RAW prompt text, before `$skill` expansion and before
# any inference. That is what makes this hook the deterministic equivalent of
# Claude Code's slash commands.
#
# Why this exists. A Codex skill is prompt text: the model reads it, decides to
# exec a script, and invents the arguments. Pinning a model that way costs two
# inference turns (one to decide to run the script, one because the script's
# output arrives as a tool result) and loses argument fidelity — a `$rf - note`
# verdict is routinely paraphrased away by the model before it reaches the
# router. Claude Code pays neither cost, because `/fm x` is expanded textually
# by the client and the router short-circuits a user-typed directive into a
# synthetic response with no upstream call at all.
#
# This hook closes that gap. It parses the directive itself, sends it to the
# router as a user-typed message, and blocks the Codex turn — so the directive
# costs zero inference and the arguments arrive byte-for-byte.
#
# The pin lands on the caller's real session because the force-model pin key
# omits the first-message discriminator (see deriveForceModelSessionKeyForRequest):
# it is derived from the API key and the client session id alone, and this hook
# sends the same Session-Id that Codex sends on its own turns.
#
# FAIL OPEN. Every unexpected condition — no jq, no curl, no credentials, an
# unreachable router, a malformed payload — must let the prompt through
# untouched. A directive that silently swallows the user's prompt is far worse
# than one that does not fire.

set -uo pipefail

# ---------- responses ----------

# pass_through hands the prompt to Codex unchanged. Every failure path ends here.
pass_through() {
  printf '{"continue":true}\n'
  exit 0
}

# block stops the turn before any inference and shows the text to the user.
# Codex renders `reason`; nothing reaches the model.
block() {
  local reason="$1" payload
  payload="$(jq -cn --arg r "$reason" '{decision:"block", reason:$r, continue:false}' 2>/dev/null)" \
    || pass_through
  printf '%s\n' "$payload"
  exit 0
}

command -v jq >/dev/null 2>&1 || pass_through

payload="$(cat)" || pass_through
[ -n "$payload" ] || pass_through

prompt="$(jq -r '.prompt // ""' <<<"$payload" 2>/dev/null)" || pass_through
session_id="$(jq -r '.session_id // ""' <<<"$payload" 2>/dev/null)" || pass_through
requested_model="$(jq -r '.model // ""' <<<"$payload" 2>/dev/null)" || pass_through

# Only a directive at the very start of the prompt is ours. Prose that merely
# mentions `$fm` somewhere in a sentence is the user's text, not a command.
case "$prompt" in
  '$'*) ;;
  *) pass_through ;;
esac

verb="${prompt#$}"
args=""
case "$verb" in
  *' '*)
    args="${verb#* }"
    verb="${verb%% *}"
    ;;
esac
# Trim surrounding whitespace from the argument tail without touching its
# interior: a leading `-`/`+` is a router-feedback verdict and must survive.
args="${args#"${args%%[![:space:]]*}"}"
args="${args%"${args##*[![:space:]]}"}"

# ---------- directive table ----------
#
# Only prompt-capability directives are handled here. The local-toggle skills
# (router-on/off/status/models, disable-routing) mutate local config rather than
# router state, so a skill remains the right adapter for them.
directive=""
case "$verb" in
  fm|force-model)          directive="/force-model" ;;
  ufm|unforce-model)       directive="/unforce-model" ;;
  rf|router-feedback)      directive="/router-feedback" ;;
  router-session)
    # Purely local: the session id is already in the hook payload, so this
    # answers without a network call and without a model turn.
    [ -n "$session_id" ] || pass_through
    block "✦ Weave Router · session id: $session_id"
    ;;
  *) pass_through ;;
esac

# A directive that needs an argument and has none is more useful as a prompt:
# the skill can still explain itself rather than the hook eating the message.
case "$directive" in
  /force-model|/router-feedback)
    [ -n "$args" ] || pass_through
    ;;
esac

# Without a client session id the router would derive a different pin key than
# Codex's own turns, so the pin would apply to a session the user is not in.
# Passing through is the honest failure: the skill path still works.
[ -n "$session_id" ] || pass_through
command -v curl >/dev/null 2>&1 || pass_through

# ---------- credentials ----------
#
# Read base_url and the router key from the [model_providers.weave] table.
# Deliberately NOT scoped to the managed comment markers: Codex rewrites
# config.toml through a TOML serializer when it persists its own state, which
# drops comments while keeping the table, and re-emits the inline http_headers
# as a subtable with the header name unquoted. A marker-scoped or quoted-only
# reader misses both on any config Codex has touched.
resolve_router_endpoint() {
  local config=""
  local helper_dir
  helper_dir="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd)" || helper_dir=""
  if [ -n "$helper_dir" ] && [ -f "$helper_dir/config.toml" ]; then
    config="$helper_dir/config.toml"
  elif [ -f "${CODEX_HOME:-$HOME/.codex}/config.toml" ]; then
    config="${CODEX_HOME:-$HOME/.codex}/config.toml"
  fi
  [ -n "$config" ] || return 1
  awk '
    # Cut a TOML comment, honouring quoted strings so a # inside a value stays.
    # A full-line comment reduces to empty and matches nothing below, so this
    # replaces a separate skip rule -- and unlike one, it also catches a comment
    # trailing another assignment, which the unanchored matches would otherwise
    # read as live config on any line preceding the real value.
    function weave_strip_comment(line,   i, c, out, indq, insq, esc) {
      out = ""
      indq = 0
      insq = 0
      esc = 0
      for (i = 1; i <= length(line); i++) {
        c = substr(line, i, 1)
        if (esc) { out = out c; esc = 0; continue }
        # Only basic strings have escapes; a backslash in a literal string is
        # data, so consuming the next character there would mis-track the quote.
        if (indq && c == "\\") { out = out c; esc = 1; continue }
        if (!insq && c == "\"") { indq = !indq; out = out c; continue }
        if (!indq && c == "'"'"'") { insq = !insq; out = out c; continue }
        if (c == "#" && !indq && !insq) break
        out = out c
      }
      return out
    }
    { $0 = weave_strip_comment($0) }
    # TOML basic string -> raw value: drop the `<header> = "` prefix and the
    # closing quote, then undo the escaping write_codex_config applied.
    function weave_unescape(s) {
      sub(/^"?[A-Za-z][A-Za-z0-9-]*"?[[:space:]]*=[[:space:]]*"/, "", s)
      sub(/"$/, "", s)
      gsub(/\\"/, "\"", s)
      gsub(/\\\\/, "\\", s)
      return s
    }
    /^[[:space:]]*\[/ {
      in_provider = ($0 ~ /^[[:space:]]*\[[[:space:]]*model_providers[[:space:]]*\.[[:space:]]*weave[[:space:]]*(\.[^]]*)?\][[:space:]]*(#.*)?$/)
      next
    }
    !in_provider { next }
    match($0, /base_url[[:space:]]*=[[:space:]]*"[^"]*"/) {
      v = substr($0, RSTART, RLENGTH)
      sub(/^.*=[[:space:]]*"/, "", v); sub(/"$/, "", v)
      if (url == "") url = v
    }
    match($0, /"?X-Weave-Router-Key"?[[:space:]]*=[[:space:]]*"([^"\\]|\\.)*"/) {
      if (key == "") key = weave_unescape(substr($0, RSTART, RLENGTH))
    }
    match($0, /"?X-Weave-User-Email"?[[:space:]]*=[[:space:]]*"([^"\\]|\\.)*"/) {
      if (email == "") email = weave_unescape(substr($0, RSTART, RLENGTH))
    }
    match($0, /"?X-Weave-User-Name"?[[:space:]]*=[[:space:]]*"([^"\\]|\\.)*"/) {
      if (name == "") name = weave_unescape(substr($0, RSTART, RLENGTH))
    }
    END { if (url != "" && key != "") printf "%s\n%s\n%s\n%s\n", url, key, email, name }
  ' "$config" 2>/dev/null
}

endpoint="$(resolve_router_endpoint)" || pass_through
base_url="$(printf '%s' "$endpoint" | sed -n 1p)"
router_key="$(printf '%s' "$endpoint" | sed -n 2p)"
user_email="$(printf '%s' "$endpoint" | sed -n 3p)"
user_name="$(printf '%s' "$endpoint" | sed -n 4p)"
if [ -z "$base_url" ] || [ -z "$router_key" ]; then
  pass_through
fi

url="${base_url%/}"
url="${url%/v1}/v1/chat/completions"

# ---------- send ----------
#
# The leading space is what stops Codex's own slash-command handling from
# claiming the line; the router strips it. Sent as a user message (not a tool
# result) so the router takes its synthetic short-circuit and never calls a
# model — the same path Claude Code's slash commands already take.
line=" $directive"
[ -n "$args" ] && line="$line $args"

body="$(jq -cn --arg m "${requested_model:-gpt-5.6-sol}" --arg c "$line" \
  '{model:$m, messages:[{role:"user", content:$c}]}' 2>/dev/null)" || pass_through

# Identity headers only when the install has them: the router must never see a
# header with an empty value, and omitting them is what an unattributed install
# already does on every ordinary turn.
identity_args=()
[ -n "$user_email" ] && identity_args+=(-H "X-Weave-User-Email: $user_email")
[ -n "$user_name" ] && identity_args+=(-H "X-Weave-User-Name: $user_name")

response="$(curl -fsS --max-time 10 \
  -H "Content-Type: application/json" \
  -H "X-Weave-Router-Key: $router_key" \
  -H "Session-Id: $session_id" \
  -H "X-App: codex" \
  ${identity_args[@]+"${identity_args[@]}"} \
  -d "$body" \
  "$url" 2>/dev/null)" || pass_through
[ -n "$response" ] || pass_through

# `strings` filters to the string type: jq -r would otherwise render an object
# or array as JSON, which is non-empty and would block the turn with garbage.
marker="$(jq -r '[.choices[0].message.content | strings] | first // ""' <<<"$response" 2>/dev/null)" || pass_through
marker="$(printf '%s' "$marker" | sed -e 's/\*\*//g' -e '/^[[:space:]]*$/d')"
[ -n "$marker" ] || pass_through

block "$marker"
