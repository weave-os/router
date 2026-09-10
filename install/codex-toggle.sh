#!/usr/bin/env bash
# <!-- weave-router managed codex directive -->
#
# Codex UserPromptSubmit hook for the Weave Router's LOCAL config toggles.
# Codex passes a JSON object on stdin carrying the RAW prompt text, before
# `$skill` expansion and before any inference.
#
# Why this exists. The other directives ($fm, $ufm, $rf, $router-session,
# a bare $router-models) are router state, and the router answers them itself
# with no model turn. These four are not: they rewrite config.toml on this
# machine, which a remote HTTP service cannot do. Left as skills they cost two
# inference turns each -- one for the model to read the skill and decide to
# exec, one to report the tool result.
#
# The second reason matters more than the cost. $router-off and
# $disable-routing are what you reach for when the router is slow, broken, or
# out of credits -- and as skills they need a model turn, which is served
# THROUGH the router. The off switch depended on the thing it turns off. This
# hook runs locally before any network call, so it works when the router does
# not.
#
# Scope: only the four local toggles. Everything the router can answer is
# deliberately absent -- a hook that intercepted those would re-open the
# duplicate-path problem #1257 closed.
#
# FAIL OPEN. Every unexpected condition -- no jq, no npx, a malformed payload,
# a command that errors -- must let the prompt through untouched. A directive
# that silently swallows the user's prompt is far worse than one that does not
# fire.

set -uo pipefail

# How long the toggle may take before the hook gives up and passes the prompt
# through. npx resolves from cache after first use; the budget covers a cold
# fetch without letting a dead network hang the turn indefinitely.
#
# Validated, not trusted. `[ "$waited" -ge "$seconds" ]` does not compare a
# bound it cannot parse -- it errors -- so the cap would never fire and the
# hook would hang until npx exited on its own. Two ways to get there: a
# non-numeric value, and a digit-only value too large for the shell's integer
# type ("[: 99999999999999999999: integer expression expected"). The length
# bound covers the second; 4 digits is already ~2.7h, far past anything
# meaningful for a prompt hook.
#
# The diagnostic goes to stderr, never stdout: stdout is the hook protocol
# channel and any stray byte there corrupts the JSON. Codex discards stderr,
# so this costs a user nothing and tells whoever set the variable why it was
# ignored.
WEAVE_TOGGLE_TIMEOUT_DEFAULT=45
# Resolved into a local first: under `set -u`, ${#WEAVE_TOGGLE_TIMEOUT} on an
# unset variable aborts the hook outright -- which would swallow every prompt,
# the one failure this script must never have.
weave_timeout="${WEAVE_TOGGLE_TIMEOUT:-$WEAVE_TOGGLE_TIMEOUT_DEFAULT}"
weave_timeout_invalid=""
case "$weave_timeout" in
  ''|*[!0-9]*|0) weave_timeout_invalid=1 ;;
  *) [ "${#weave_timeout}" -gt 4 ] && weave_timeout_invalid=1 ;;
esac
if [ -n "$weave_timeout_invalid" ]; then
  printf 'weave-router: ignoring invalid WEAVE_TOGGLE_TIMEOUT=%s; using %ss\n' \
    "$weave_timeout" "$WEAVE_TOGGLE_TIMEOUT_DEFAULT" >&2
  weave_timeout="$WEAVE_TOGGLE_TIMEOUT_DEFAULT"
fi
WEAVE_TOGGLE_TIMEOUT="$weave_timeout"

# ---------- responses ----------

# pass_through hands the prompt to Codex unchanged. Every failure path ends here.
pass_through() {
  printf '{"continue":true}\n'
  exit 0
}

# block stops the turn before any inference and shows the text to the user.
# Codex renders `reason`; nothing reaches the model.
#
# Setting only `decision` is deliberate: `continue:false` alongside it wins and
# renders a bare "Hook stopped" with the reason discarded.
block() {
  local reason="$1" payload
  payload="$(jq -cn --arg r "$reason" '{decision:"block", reason:$r}' 2>/dev/null)" \
    || pass_through
  printf '%s\n' "$payload"
  exit 0
}

command -v jq >/dev/null 2>&1 || pass_through

payload="$(cat)" || pass_through
[ -n "$payload" ] || pass_through

prompt="$(jq -r '.prompt // ""' <<<"$payload" 2>/dev/null)" || pass_through

# Only a directive at the very start of the prompt is ours. Prose that merely
# mentions `$router-off` in a sentence is the user's text, not a command.
case "$prompt" in
  '$'*) ;;
  *) pass_through ;;
esac

verb="${prompt#$}"
# These four take no arguments. A prompt with a tail is not one of ours --
# pass it through so the skill can explain itself rather than the hook eating
# a message it cannot honour.
case "$verb" in
  *[[:space:]]*) pass_through ;;
esac

# ---------- directive table ----------
#
# Local-config toggles only. router-on/off/status flip this install's
# config.toml; disable-routing is `off` plus the reversal hint below.
case "$verb" in
  router-on)       toggle="on" ;;
  router-off)      toggle="off" ;;
  router-status)   toggle="status" ;;
  disable-routing) toggle="off" ;;
  *) pass_through ;;
esac

command -v npx >/dev/null 2>&1 || pass_through

# ---------- run ----------

# WEAVE_TOGGLE_TIMED_OUT is set by run_bounded instead of encoding the timeout
# in the exit status. 124 is a status the child can legitimately return, and
# reading that as "we timed out" would swallow the toggle's own error output.
WEAVE_TOGGLE_TIMED_OUT=0

# run_bounded runs a command with a wall-clock cap. GNU `timeout` is not on
# macOS, so this polls the child rather than depending on coreutils.
#
# The child runs in its own process group (`set -m` + `kill -- -PID`) because
# npx execs node: signalling only the direct child leaves that node process
# running after the hook has already passed the prompt through.
run_bounded() {
  local seconds="$1" out="$2"; shift 2
  WEAVE_TOGGLE_TIMED_OUT=0

  local had_monitor=0
  case "$-" in *m*) had_monitor=1 ;; esac
  set -m
  "$@" >"$out" 2>&1 &
  local pid=$!
  [ "$had_monitor" -eq 1 ] || set +m

  local waited=0
  while kill -0 "$pid" 2>/dev/null; do
    if [ "$waited" -ge "$seconds" ]; then
      WEAVE_TOGGLE_TIMED_OUT=1
      # Negative pid = the whole group. Fall back to the bare pid if the
      # group is already gone, so a raced exit still gets reaped.
      kill -TERM -- "-$pid" 2>/dev/null || kill -TERM "$pid" 2>/dev/null
      sleep 1
      kill -KILL -- "-$pid" 2>/dev/null || kill -KILL "$pid" 2>/dev/null
      wait "$pid" 2>/dev/null
      return 0
    fi
    sleep 1
    waited=$((waited + 1))
  done
  wait "$pid"
}

out_file="$(mktemp -t weave-codex-toggle.XXXXXX)" || pass_through
# shellcheck disable=SC2064  # expand out_file now; it never changes after this
trap "rm -f '$out_file'" EXIT

# {{SCOPE}} is substituted at install time by the only code that knows whether
# this install is user, project, or --dir scoped. It becomes literal argument
# text (the installer printf %q-quotes any path), so there is nothing here to
# word-split at runtime -- the braces are a template token, not an expansion.
# shellcheck disable=SC1083
run_bounded "$WEAVE_TOGGLE_TIMEOUT" "$out_file" \
  npx --package @weave-os/router -y -- weave-router "$toggle" --codex{{SCOPE}}
status=$?

output="$(cat "$out_file" 2>/dev/null || true)"
# Strip ANSI colour the installer emits when it thinks it has a terminal;
# Codex renders `reason` as plain text.
output="$(printf '%s' "$output" | sed -e 's/\x1b\[[0-9;]*m//g')"

# A failure is reported, not swallowed: passing through here would hand the
# model a prompt the user meant as a command, and it would likely try the same
# command again. Only a real timeout passes through -- the toggle never got to
# finish, so the skill path is still the honest fallback.
if [ "$WEAVE_TOGGLE_TIMED_OUT" -eq 1 ]; then
  pass_through
fi

[ -n "$output" ] || output="weave-router $toggle --codex exited with status $status."

# Both trailers describe a change that actually happened, so they are gated on
# a zero exit. A failed toggle rewrote no config; telling the user it takes
# effect next launch would send them to restart Codex for nothing.
if [ "$status" -eq 0 ] && [ "$verb" != "router-status" ]; then
  output="$output"$'\n\n'"Takes effect on your next \`codex\` launch: Codex reads its provider config at startup, so this session keeps routing as it already was."
fi
if [ "$status" -eq 0 ] && [ "$verb" = "disable-routing" ]; then
  output="$output"$'\n'"Reverse it with \$router-on."
fi

block "$output"
