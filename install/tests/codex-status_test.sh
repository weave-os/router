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

# ---------- server-sourced savings ----------
#
# Savings come from the router's own (requested - actual), never from local
# pricing: Codex records only its requested model, so client-side arithmetic
# would price both sides identically and report zero.

savings_home="$work/home"
mkdir -p "$savings_home/.codex"
cost_body="$work/cost.json"
printf '%s\n' '{"session_id":"session-2","savings_usd":0.32}' >"$cost_body"
cat >"$savings_home/.codex/config.toml" <<TOML
# >>> weave-router managed (do not edit between markers) >>>
model_provider = "weave"

[model_providers.weave]
base_url = "file://$cost_body"
http_headers = { "X-Weave-Router-Key" = "rk_test", "X-App" = "codex" }
# <<< weave-router managed <<<
TOML

savings_cache="$work/cache-savings"
run_savings_turn() {
  printf '%s\n' '{"session_id":"session-2","model":"gpt-5.6-terra","last_assistant_message":"✦ **Weave Router** → claude-sonnet-5 · best pick for this turn"}' \
    | HOME="$savings_home" XDG_CACHE_HOME="$savings_cache" \
      WEAVE_CODEX_STATUS_TITLE_FILE="$title_file" "$helper" >/dev/null
}

# The first turn has no cache yet, so it renders model-only and kicks off the
# fetch that serves the next turn — the hook must never block on the network.
run_savings_turn
[ "$(cat "$title_file")" = "Weave Router · claude-sonnet-5 ← gpt-5.6-terra" ] || {
  echo "first turn rendered savings before any fetch had completed" >&2
  exit 1
}

cost_cache="$savings_cache/weave-router/codex/session-2.cost"
for _ in 1 2 3 4 5 6 7 8 9 10; do
  [ -f "$cost_cache" ] && break
  sleep 0.2
done
[ -f "$cost_cache" ] || {
  echo "background fetch never wrote the session cost cache" >&2
  exit 1
}

run_savings_turn
[ "$(cat "$title_file")" = "Weave Router · claude-sonnet-5 ← gpt-5.6-terra · saved \$0.32" ] || {
  echo "server-sourced savings did not reach the title: $(cat "$title_file")" >&2
  exit 1
}

# The remaining rendering cases run with no reachable config ($HOME has no
# config.toml), so the fetch is a no-op and the seeded cache is what the turn
# renders. That also proves an unreachable router leaves the last good value in
# place rather than wiping it.
render_cached_savings() {
  printf '%s' "$1" >"$cost_cache"
  printf '%s\n' '{"session_id":"session-2","model":"gpt-5.6-terra","last_assistant_message":"✦ **Weave Router** → claude-sonnet-5 · best pick"}' \
    | HOME="$work/empty-home" XDG_CACHE_HOME="$savings_cache" \
      WEAVE_CODEX_STATUS_TITLE_FILE="$title_file" "$helper" >/dev/null
}

render_cached_savings '0.32'
[ "$(cat "$title_file")" = "Weave Router · claude-sonnet-5 ← gpt-5.6-terra · saved \$0.32" ] || {
  echo "an unreachable router discarded the cached savings: $(cat "$title_file")" >&2
  exit 1
}

# A router that spent more than the requested model would have is reported by
# staying silent, never as a negative saving.
render_cached_savings '-0.5'
[ "$(cat "$title_file")" = "Weave Router · claude-sonnet-5 ← gpt-5.6-terra" ] || {
  echo "negative savings leaked into the title: $(cat "$title_file")" >&2
  exit 1
}

# Sub-cent totals must not read as "$0.00", which is indistinguishable from
# "the router ran and did not beat your selection".
render_cached_savings '0.004'
[ "$(cat "$title_file")" = "Weave Router · claude-sonnet-5 ← gpt-5.6-terra" ] || {
  echo "a total below half a cent should render no savings clause" >&2
  exit 1
}
render_cached_savings '0.006'
[ "$(cat "$title_file")" = "Weave Router · claude-sonnet-5 ← gpt-5.6-terra · saved <\$0.01" ] || {
  echo "sub-cent savings did not render as <\$0.01: $(cat "$title_file")" >&2
  exit 1
}

# A garbage cache must degrade to model-only rather than rendering junk.
render_cached_savings 'not-a-number'
[ "$(cat "$title_file")" = "Weave Router · claude-sonnet-5 ← gpt-5.6-terra" ] || {
  echo "a malformed cost cache leaked into the title: $(cat "$title_file")" >&2
  exit 1
}

# A project-scoped helper (weave-status.sh) whose adjacent config.toml is
# gone must not fall through to ~/.codex — that would send the user-scope
# key for a project session.
project_helper="$work/project/.codex/weave-status.sh"
mkdir -p "$(dirname "$project_helper")" "$work/project-home/.codex"
cp "$helper" "$project_helper"
chmod +x "$project_helper"
cat >"$work/project-home/.codex/config.toml" <<TOML
# >>> weave-router managed (do not edit between markers) >>>
model_provider = "weave"

[model_providers.weave]
base_url = "file://$cost_body"
http_headers = { "X-Weave-Router-Key" = "rk_user_scope", "X-App" = "codex" }
# <<< weave-router managed <<<
TOML
project_cache="$work/cache-project-scope"
printf '%s\n' '{"session_id":"session-project","model":"gpt-5.6-terra","last_assistant_message":"✦ **Weave Router** → claude-sonnet-5 · best pick"}' \
  | HOME="$work/project-home" XDG_CACHE_HOME="$project_cache" \
    WEAVE_CODEX_STATUS_TITLE_FILE="$title_file" "$project_helper" >/dev/null
sleep 0.5
[ ! -f "$project_cache/weave-router/codex/session-project.cost" ] || {
  echo "project helper without adjacent config used the user-scope endpoint" >&2
  exit 1
}

# Opting out must suppress the fetch entirely, not just the rendering.
optout_cache="$work/cache-optout"
printf '%s\n' '{"session_id":"session-3","model":"gpt-5.6-terra","last_assistant_message":"✦ **Weave Router** → claude-sonnet-5 · best pick"}' \
  | HOME="$savings_home" XDG_CACHE_HOME="$optout_cache" WEAVE_CODEX_STATUS_SAVINGS=0 \
    WEAVE_CODEX_STATUS_TITLE_FILE="$title_file" "$helper" >/dev/null
sleep 0.5
[ ! -f "$optout_cache/weave-router/codex/session-3.cost" ] || {
  echo "WEAVE_CODEX_STATUS_SAVINGS=0 still fetched the session cost" >&2
  exit 1
}

# install.sh embeds this helper as a heredoc so the standalone `curl | sh`
# install has no sibling asset to copy. Nothing keeps the two copies in sync,
# so an edit to one silently ships the other stale — which is exactly how the
# savings lookup missed every curl installer once already.
installer="$script_dir/../install.sh"
if [ -f "$installer" ]; then
  start="$(grep -n 'CODEX_STATUS_EOF' "$installer" | head -1 | cut -d: -f1)"
  end="$(grep -n 'CODEX_STATUS_EOF' "$installer" | tail -1 | cut -d: -f1)"
  if [ -n "$start" ] && [ -n "$end" ] && [ "$end" -gt "$start" ]; then
    awk -v s="$start" -v e="$end" 'NR>s && NR<e' "$installer" >"$work/codex-heredoc.sh"
    diff -q "$work/codex-heredoc.sh" "$helper" >/dev/null 2>&1 || {
      echo "install.sh heredoc has drifted from codex-status.sh:" >&2
      diff "$work/codex-heredoc.sh" "$helper" | head -10 >&2
      exit 1
    }
  else
    echo "could not locate the CODEX_STATUS_EOF markers in install.sh" >&2
    exit 1
  fi
fi

echo "Codex status helper regression tests passed"
