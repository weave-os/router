#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
helper="$script_dir/../codex-status.sh"
[ -x "$helper" ] || { echo "missing executable Codex status helper" >&2; exit 1; }

work="$(mktemp -d)"
cost_mock_pid=""
cleanup() {
  # Capture first: reaping the fixture would otherwise leave the shell exiting
  # 143 and turn a green run red.
  status=$?
  if [ -n "$cost_mock_pid" ]; then
    kill "$cost_mock_pid" 2>/dev/null
    wait "$cost_mock_pid" 2>/dev/null || true
  fi
  rm -rf "$work"
  exit "$status"
}
trap cleanup EXIT

# One case below needs a real HTTP fixture (curl sends no headers to file://).
# Say so up front rather than letting it surface as a readiness timeout.
command -v python3 >/dev/null 2>&1 || {
  echo "python3 is required for the Codex status helper tests" >&2
  exit 1
}

# The hook self-updates from GitHub. Left on, every case below would race a
# download that replaces the helper under test with the published copy, so the
# suite would be testing main rather than the working tree. Cases that exercise
# the updater re-enable it with an explicit file:// source.
export WEAVE_CODEX_STATUS_UPDATE=0

title_file="$work/title"
cache="$work/cache"
XDG_CACHE_HOME="$cache" WEAVE_CODEX_STATUS_TITLE_FILE="$title_file" \
  "$helper" --on
[ "$(cat "$title_file")" = "Weave Router · active" ] || {
  echo "--on did not set the active title" >&2
  exit 1
}

printf '%s\n' '{"session_id":"session-1","model":"gpt-5.6-terra","last_assistant_message":"✦ **Weave Router** → claude-sonnet-5 · best pick for this turn"}' \
  | XDG_CACHE_HOME="$cache" WEAVE_CODEX_STATUS_TITLE_FILE="$title_file" "$helper" >"$work/marker.out"
[ ! -s "$work/marker.out" ] || {
  echo "Stop hook emitted a duplicate status message" >&2
  exit 1
}
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
[ ! -s "$work/session-start.out" ] || {
  echo "disabled SessionStart hook emitted a status message" >&2
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

# ---------- credentials survive a config Codex has rewritten ----------
#
# Codex round-trips config.toml through a TOML serializer whenever it persists
# its own state, which drops the managed comment markers while keeping the
# provider table, and re-emits the inline http_headers as a subtable with the
# header name unquoted. A marker-scoped, quoted-only read resolved nothing on
# such a config, so the savings figure silently vanished from the title with no
# error anywhere -- the failure looked like "the router just stopped showing
# savings".
normalized_home="$work/normalized-home"
mkdir -p "$normalized_home/.codex"
normalized_cost="$work/cost-normalized.json"
printf '%s\n' '{"session_id":"session-3","savings_usd":1.25}' >"$normalized_cost"
cat >"$normalized_home/.codex/config.toml" <<TOML
model_provider = "weave"

[model_providers.weave]
base_url = "file://$normalized_cost"
name = "Weave Router"
requires_openai_auth = true
wire_api = "responses"

[model_providers.weave.http_headers]
X-App = "codex"
X-Weave-Router-Key = "rk_test"

[projects."/some/repo"]
trust_level = "trusted"
TOML

normalized_cache="$work/cache-normalized"
run_normalized_turn() {
  printf '%s\n' '{"session_id":"session-3","model":"gpt-5.6-terra","last_assistant_message":"✦ **Weave Router** → claude-sonnet-5 · best pick for this turn"}' \
    | HOME="$normalized_home" XDG_CACHE_HOME="$normalized_cache" \
      WEAVE_CODEX_STATUS_TITLE_FILE="$title_file" "$helper" >/dev/null
}

run_normalized_turn
normalized_cost_cache="$normalized_cache/weave-router/codex/session-3.cost"
for _ in 1 2 3 4 5 6 7 8 9 10; do
  [ -f "$normalized_cost_cache" ] && break
  sleep 0.2
done
[ -f "$normalized_cost_cache" ] || {
  echo "credentials did not resolve from a Codex-rewritten config (no cost fetch ran)" >&2
  exit 1
}

run_normalized_turn
[ "$(cat "$title_file")" = "Weave Router · claude-sonnet-5 ← gpt-5.6-terra · saved \$1.25" ] || {
  echo "savings did not reach the title from a Codex-rewritten config: $(cat "$title_file")" >&2
  exit 1
}

# A commented-out endpoint or key left above the live one must not win. First
# match wins, so treating a comment as config would point the fetch at a stale
# endpoint and send the router key there.
#
# This one needs a real HTTP fixture rather than the file:// seam used above:
# curl sends no headers to a file:// URL, so a file-based check would prove the
# base_url comment is skipped while saying nothing about the key.
commented_port="$(python3 -c 'import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()')"
commented_seen="$work/commented-key.txt"
cat >"$work/cost-mock.py" <<'MOCK'
import pathlib, sys
from http.server import BaseHTTPRequestHandler, HTTPServer

SEEN = pathlib.Path(sys.argv[2])


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        SEEN.write_text(self.headers.get("X-Weave-Router-Key") or "")
        body = b'{"savings_usd":2.50}'
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *a):
        pass


HTTPServer(("127.0.0.1", int(sys.argv[1])), Handler).serve_forever()
MOCK
python3 "$work/cost-mock.py" "$commented_port" "$commented_seen" &
cost_mock_pid=$!  # reaped by the EXIT trap if anything below fails
cost_mock_ready=""
for _ in $(seq 1 60); do
  if curl -fsS -o /dev/null --max-time 1 "http://127.0.0.1:$commented_port/v1/sessions/x/cost" 2>/dev/null; then
    cost_mock_ready=1
    break
  fi
  sleep 0.25
done
[ -n "$cost_mock_ready" ] || {
  echo "cost mock never came up on port $commented_port" >&2
  exit 1
}
rm -f "$commented_seen"

commented_home="$work/commented-home"
mkdir -p "$commented_home/.codex"
cat >"$commented_home/.codex/config.toml" <<TOML
[model_providers.weave]
# base_url = "http://127.0.0.1:9/v1"
base_url = "http://127.0.0.1:$commented_port/v1"

[model_providers.weave.http_headers]
# X-Weave-Router-Key = "rk_stale"
X-Weave-Router-Key = "rk_test"
TOML
commented_cache="$work/cache-commented"
printf '%s\n' '{"session_id":"session-5","model":"gpt-5.6-terra","last_assistant_message":"✦ **Weave Router** → claude-sonnet-5 · best pick"}' \
  | HOME="$commented_home" XDG_CACHE_HOME="$commented_cache" \
    WEAVE_CODEX_STATUS_TITLE_FILE="$title_file" "$helper" >/dev/null
commented_cost_cache="$commented_cache/weave-router/codex/session-5.cost"
for _ in 1 2 3 4 5 6 7 8 9 10; do
  [ -f "$commented_cost_cache" ] && break
  sleep 0.2
done
[ -f "$commented_cost_cache" ] || {
  echo "a commented-out example blocked the live endpoint" >&2
  exit 1
}
[ "$(cat "$commented_seen" 2>/dev/null)" = "rk_test" ] || {
  echo "the commented-out key was forwarded instead of the live one: $(cat "$commented_seen" 2>/dev/null)" >&2
  exit 1
}

# A comment trailing another assignment is the same hazard one line over: the
# matches are unanchored, so a commented base_url riding on an earlier line
# wins under first-match unless comments are stripped rather than line-skipped.
inline_home="$work/inline-home"
mkdir -p "$inline_home/.codex"
cat >"$inline_home/.codex/config.toml" <<TOML
[model_providers.weave]
name = "Weave Router" # base_url = "http://127.0.0.1:9/v1"
base_url = "http://127.0.0.1:$commented_port/v1"
wire_api = "responses" # X-Weave-Router-Key = "rk_inline_stale"
# A literal string is a string: a # inside one must not end the line, or the
# key after it in this inline table is lost and the fetch never runs.
http_headers = { "X-App" = 'codex#1', "X-Weave-Router-Key" = "rk_test" }
TOML
inline_seen="$work/inline-key.txt"
rm -f "$inline_seen"
cp "$commented_seen" "$work/commented-key.keep" 2>/dev/null || true
inline_cache="$work/cache-inline"
kill "$cost_mock_pid" 2>/dev/null
wait "$cost_mock_pid" 2>/dev/null || true
python3 "$work/cost-mock.py" "$commented_port" "$inline_seen" &
cost_mock_pid=$!
for _ in $(seq 1 60); do
  curl -fsS -o /dev/null --max-time 1 "http://127.0.0.1:$commented_port/v1/sessions/x/cost" 2>/dev/null && break
  sleep 0.25
done
rm -f "$inline_seen"

printf '%s\n' '{"session_id":"session-6","model":"gpt-5.6-terra","last_assistant_message":"✦ **Weave Router** → claude-sonnet-5 · best pick"}' \
  | HOME="$inline_home" XDG_CACHE_HOME="$inline_cache" \
    WEAVE_CODEX_STATUS_TITLE_FILE="$title_file" "$helper" >/dev/null
inline_cost_cache="$inline_cache/weave-router/codex/session-6.cost"
for _ in 1 2 3 4 5 6 7 8 9 10; do
  [ -f "$inline_cost_cache" ] && break
  sleep 0.2
done
[ -f "$inline_cost_cache" ] || {
  echo "an inline comment on an earlier line shadowed the live base_url" >&2
  exit 1
}
[ "$(cat "$inline_seen" 2>/dev/null)" = "rk_test" ] || {
  echo "the inline-commented key was forwarded instead of the live one: $(cat "$inline_seen" 2>/dev/null)" >&2
  exit 1
}
kill "$cost_mock_pid" 2>/dev/null
wait "$cost_mock_pid" 2>/dev/null || true
cost_mock_pid=""

# A provider whose name merely starts with "weave" is a different provider: its
# key must never be adopted, and a config holding only that one resolves nothing.
neighbour_home="$work/neighbour-home"
mkdir -p "$neighbour_home/.codex"
cat >"$neighbour_home/.codex/config.toml" <<TOML
[model_providers.weaver]
base_url = "file://$normalized_cost"
X-Weave-Router-Key = "rk_not_ours"
TOML
neighbour_cache="$work/cache-neighbour"
printf '%s\n' '{"session_id":"session-4","model":"gpt-5.6-terra","last_assistant_message":"✦ **Weave Router** → claude-sonnet-5 · best pick"}' \
  | HOME="$neighbour_home" XDG_CACHE_HOME="$neighbour_cache" \
    WEAVE_CODEX_STATUS_TITLE_FILE="$title_file" "$helper" >/dev/null
# Poll the full window the positive fetches get rather than sleeping once: a
# fixed wait can expire before a wrongly-adopted fetch lands, which would pass
# this assertion under CI load precisely when it ought to fail.
neighbour_cost_cache="$neighbour_cache/weave-router/codex/session-4.cost"
for _ in 1 2 3 4 5 6 7 8 9 10; do
  [ -f "$neighbour_cost_cache" ] && break
  sleep 0.2
done
[ ! -f "$neighbour_cost_cache" ] || {
  echo "adopted credentials from an unrelated provider whose name starts with weave" >&2
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

# ---------- background self-refresh ----------
#
# Codex only reruns the installer when a user remembers to, so the lifecycle
# hook doubles as the update check. A `file://` source keeps this offline.

refresh_home="$work/refresh-home"
mkdir -p "$refresh_home"
refresh_helper="$work/refresh/weave-status.sh"
mkdir -p "$(dirname "$refresh_helper")"
cp "$helper" "$refresh_helper"
chmod 700 "$refresh_helper"

newer_helper="$work/newer-codex-status.sh"
{
  printf '%s\n' '#!/usr/bin/env bash'
  printf '%s\n' '# <!-- weave-router managed codex status -->'
  printf '%s\n' '# newer upstream copy'
  # The updater rejects anything under 1 KiB, so pad past that floor.
  awk 'BEGIN { while (i++ < 40) print "# padding padding padding padding padding padding" }'
  printf '%s\n' 'exit 0'
} >"$newer_helper"

run_refresh_turn() {
  printf '%s\n' '{"session_id":"session-refresh","model":"gpt-5.6-terra","last_assistant_message":"✦ **Weave Router** → claude-sonnet-5 · best pick"}' \
    | HOME="$refresh_home" XDG_CACHE_HOME="$work/cache-refresh" \
      WEAVE_CODEX_STATUS_UPDATE=1 WEAVE_CODEX_STATUS_URL="file://$newer_helper" \
      WEAVE_CODEX_STATUS_TITLE_FILE="$title_file" "$refresh_helper" >/dev/null
}

run_refresh_turn
for _ in 1 2 3 4 5 6 7 8 9 10; do
  cmp -s "$refresh_helper" "$newer_helper" && break
  sleep 0.2
done
cmp -s "$refresh_helper" "$newer_helper" || {
  echo "the hook did not pick up a newer helper" >&2
  exit 1
}

# A second turn must not re-download inside the interval, so a helper edited
# after the stamp was written stays put. Recopy the real helper first — the
# first turn replaced it with the stub from $newer_helper, which never
# enters weave_self_refresh.
cp "$helper" "$refresh_helper"
chmod 700 "$refresh_helper"
printf '\n# mutated after stamp\n' >>"$refresh_helper"
before_rate_limit="$(cat "$refresh_helper")"
run_refresh_turn
sleep 0.5
[ "$(cat "$refresh_helper")" = "$before_rate_limit" ] || {
  echo "self-refresh ignored its own rate limit" >&2
  exit 1
}

# Opting out must suppress the download even when the interval has elapsed.
optout_helper="$work/refresh-optout/weave-status.sh"
mkdir -p "$(dirname "$optout_helper")"
cp "$helper" "$optout_helper"
chmod 700 "$optout_helper"
optout_before="$(cat "$optout_helper")"
printf '%s\n' '{"session_id":"session-refresh-off","model":"gpt-5.6-terra","last_assistant_message":"✦ **Weave Router** → claude-sonnet-5 · best pick"}' \
  | HOME="$refresh_home" XDG_CACHE_HOME="$work/cache-refresh-optout" \
    WEAVE_CODEX_STATUS_URL="file://$newer_helper" WEAVE_CODEX_STATUS_UPDATE=0 \
    WEAVE_CODEX_STATUS_TITLE_FILE="$title_file" "$optout_helper" >/dev/null
sleep 0.5
[ "$(cat "$optout_helper")" = "$optout_before" ] || {
  echo "WEAVE_CODEX_STATUS_UPDATE=0 still replaced the helper" >&2
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
