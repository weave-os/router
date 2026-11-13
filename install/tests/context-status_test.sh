#!/usr/bin/env bash
set -euo pipefail
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
export HOME="$work/home" XDG_CACHE_HOME="$work/cache"
export WEAVE_STATUSLINE_UPDATE=0 WEAVE_CODEX_STATUS_UPDATE=0 WEAVE_COMMANDS_UPDATE=0
mkdir -p "$HOME/.codex" "$work/helpers"
cp "$script_dir/../codex-status.sh" "$work/helpers/codex-status.sh"
codex="$work/helpers/codex-status.sh"
claude="$script_dir/../cc-statusline.sh"
export WEAVE_CODEX_STATUS_TITLE_FILE="$work/title"
contains() { [[ "$1" == *"$2"* ]] || { printf 'missing %s in %s\n' "$2" "$1" >&2; exit 1; }; }
absent() { [[ "$1" != *"$2"* ]] || { printf 'unexpected %s in %s\n' "$2" "$1" >&2; exit 1; }; }

for percentage in 0 58 95 10; do
  payload="$(jq -nc --argjson p "$percentage" '{model:{id:"gpt-5.6-sol"},context_window:{context_window_size:128000,used_percentage:$p}}')"
  line="$(printf '%s' "$payload" | "$claude")"
  contains "$line" "Context $percentage%"
  if [ "$percentage" = 95 ]; then contains "$line" '95% !'; else absent "$line" '% !'; fi
done
for invalid in 'null' '"58"' '-1' '101' '{}'; do
  payload="$(jq -nc --argjson p "$invalid" '{context_window:{context_window_size:128000,used_percentage:$p}}')"
  absent "$(printf '%s' "$payload" | "$claude")" 'Context '
done
for window in 0 -1 '"128000"' null; do
  payload="$(jq -nc --argjson w "$window" '{context_window:{context_window_size:$w,used_percentage:58}}')"
  absent "$(printf '%s' "$payload" | "$claude")" 'Context '
done
absent "$(printf '%s' '{"context_window":{"total_input_tokens":72000,"total_output_tokens":9000,"context_window_size":128000}}' | "$claude")" 'Context '
[ -z "$(printf '%s' '{not json' | "$claude")" ]
absent "$(printf '%s' '{"context_window":{"context_window_size":128000,"used_percentage":58}}' | WEAVE_STATUSLINE_CONTEXT=0 "$claude")" 'Context '

fixture="$work/snapshot.json"
make_snapshot() {
  jq -n --argjson age "${1:-0}" '{session_id:"session-context", savings_usd:0.32,context_snapshot:{
    version:1,estimate_kind:"approximate",estimate_tokens:72000,context_window:128000,output_reserve_tokens:8000,
    served_model:"gpt-5.6-luna",requested_model:"gpt-5.6-sol",request_id:"synthetic-request",
    requested_at:((now - $age - 1)|floor|todateiso8601),recorded_at:((now - $age)|floor|todateiso8601)
  }}' >"$fixture"
}
make_snapshot
cat >"$HOME/.codex/config.toml" <<TOML
[model_providers.weave]
base_url = "file://$fixture"
http_headers = { "X-Weave-Router-Key" = "rk_synthetic" }
TOML
payload='{"hook_event_name":"Stop","session_id":"session-context","model":"gpt-5.6-sol","last_assistant_message":"✦ **Weave Router** → gpt-5.6-luna · best pick"}'
run_codex() { printf '%s' "$payload" | "$codex" >"$work/hook-output"; [ ! -s "$work/hook-output" ]; }
run_codex
for _ in $(seq 1 40); do
  [[ "$(cat "$work/title")" == *'Router ctx est.'* ]] && break
  sleep 0.1
done
contains "$(cat "$work/title")" 'gpt-5.6-sol → gpt-5.6-luna'
contains "$(cat "$work/title")" 'last Router ctx est. ~72k/128k'
absent "$(cat "$work/title")" '%'
scope="$(printf '%s' "$(cd "$work/helpers" && pwd -P)" | cksum | awk '{print $1}')"
context_file="$XDG_CACHE_HOME/weave-router/codex/$scope-session-context.context"
[ -f "$context_file" ]
[ "$(stat -c %a "$context_file" 2>/dev/null || stat -f %Lp "$context_file")" = 600 ]
absent "$(cat "$context_file")" 'rk_synthetic'
# A valid but older observation must not overwrite snapshot, savings or title.
newest_request="$(jq -r '.context_snapshot.request_id' "$context_file")"
mkdir "$work/curl-bin"
cat >"$work/curl-bin/curl" <<'CURL'
#!/usr/bin/env bash
: >"$WEAVE_TEST_FETCH_STARTED"
exec "$WEAVE_TEST_CURL" "$@"
CURL
chmod +x "$work/curl-bin/curl"
real_curl="$(command -v curl)"
for age in 10 0 equal; do
  if [ "$age" = equal ]; then
    cp "$context_file" "$fixture"
  else
    make_snapshot "$age"
  fi
  if [ "$age" = 0 ]; then
    # Equal seconds with a later fractional observation must remain ordered.
    jq '.context_snapshot.requested_at |= sub("Z$"; ".9Z") | .context_snapshot.recorded_at |= sub("Z$"; ".9Z")' "$context_file" >"$work/newest.json"
    mv "$work/newest.json" "$context_file"
    jq '.context_snapshot.requested_at |= sub("\\.[0-9]+Z$"; ".1Z") | .context_snapshot.recorded_at |= sub("\\.[0-9]+Z$"; ".1Z")' "$context_file" >"$work/fractional.json"
    mv "$work/fractional.json" "$fixture"
  fi
  jq '.savings_usd=0.44 | .context_snapshot.request_id="older-observation" | .context_snapshot.estimate_tokens=1000' "$fixture" >"$work/older.json"
  mv "$work/older.json" "$fixture"
  rm -f "$work/fetch-started"
  PATH="$work/curl-bin:$PATH" WEAVE_TEST_FETCH_STARTED="$work/fetch-started" WEAVE_TEST_CURL="$real_curl" run_codex
  for _ in $(seq 1 40); do
    [ -f "$work/fetch-started" ] && [ ! -d "$XDG_CACHE_HOME/weave-router/codex/session-context.cost.lock" ] && break
    sleep 0.1
  done
  [ -f "$work/fetch-started" ] && [ ! -d "$XDG_CACHE_HOME/weave-router/codex/session-context.cost.lock" ]
  [ "$(jq -r '.context_snapshot.request_id' "$context_file")" = "$newest_request" ] || { echo 'an older context observation overwrote the newer snapshot' >&2; exit 1; }
  [ "$(cat "$XDG_CACHE_HOME/weave-router/codex/session-context.cost")" = '0.32' ] || { echo 'an older context observation changed savings' >&2; exit 1; }
  contains "$(cat "$work/title")" 'last Router ctx est. ~72k/128k'
done
cp "$context_file" "$fixture"
jq '.savings_usd=0.55' "$fixture" >"$work/same-observation.json"
mv "$work/same-observation.json" "$fixture"
rm -f "$work/fetch-started"
PATH="$work/curl-bin:$PATH" WEAVE_TEST_FETCH_STARTED="$work/fetch-started" WEAVE_TEST_CURL="$real_curl" run_codex
for _ in $(seq 1 40); do
  [ -f "$work/fetch-started" ] && [ ! -d "$XDG_CACHE_HOME/weave-router/codex/session-context.cost.lock" ] && break
  sleep 0.1
done
[ "$(cat "$XDG_CACHE_HOME/weave-router/codex/session-context.cost")" = '0.55' ] || { echo 'an identical observation prevented updated savings' >&2; exit 1; }
contains "$(cat "$work/title")" "saved \$0.55"
# Use only cached data while testing invalidation; there must be no racing refresh.
rm "$HOME/.codex/config.toml"
make_snapshot 301
cp "$fixture" "$context_file"
run_codex
absent "$(cat "$work/title")" 'ctx est.'
# A cached snapshot for a prior automatic route must not override an explicit pin.
cp "$fixture" "$context_file"
forced_payload='{"hook_event_name":"Stop","session_id":"session-context","model":"gpt-5.6-sol","last_assistant_message":"Weave Router: force-model applied: gpt-5.6-sol (session pin)"}'
make_snapshot
cp "$fixture" "$context_file"
printf '%s' "$forced_payload" | "$codex" >/dev/null
absent "$(cat "$work/title")" 'last Router ctx est.'
contains "$(cat "$work/title")" 'gpt-5.6-sol'
parent_payload='{"hook_event_name":"Stop","session_id":"session-context","model":"gpt-5.6-sol","last_assistant_message":"Weave Router: force-parent applied: gpt-6-astra (openai). Subagents use automatic routing."}'
printf '%s' "$parent_payload" | "$codex" >/dev/null
contains "$(cat "$work/title")" 'gpt-6-astra'
absent "$(cat "$work/title")" 'last Router ctx est.'
make_snapshot
jq '.session_id="foreign-session"' "$fixture" >"$context_file"
run_codex
absent "$(cat "$work/title")" 'ctx est.'
for filter in '.context_snapshot.estimate_tokens=0' '.context_snapshot.estimate_kind="exact"' '.context_snapshot.version=2' '.context_snapshot.context_window="128000"' '.context_snapshot.served_model="unsafe\u001btitle"' '.context_snapshot.recorded_at=((now+100)|floor|todateiso8601)'; do
  jq "$filter" "$fixture" >"$context_file"
  run_codex
  absent "$(cat "$work/title")" 'ctx est.'
done
cp "$fixture" "$context_file"
printf '%s' '{"hook_event_name":"PreCompact","session_id":"session-context"}' | "$codex"
[ ! -f "$context_file" ]
absent "$(cat "$work/title")" 'ctx est.'
cp "$fixture" "$context_file"
printf '%s' '{"hook_event_name":"SessionStart","session_id":"session-context"}' | "$codex"
[ ! -f "$context_file" ]
absent "$(cat "$work/title")" 'ctx est.'
cp "$fixture" "$context_file"
WEAVE_CODEX_STATUS_CONTEXT=0 run_codex
absent "$(cat "$work/title")" 'ctx est.'
# A second helper scope must not reuse the first installation's snapshot.
mkdir "$work/other"
cp "$codex" "$work/other/weave-status.sh"
printf '%s' "$payload" | "$work/other/weave-status.sh"
absent "$(cat "$work/title")" 'ctx est.'

# Claude fallback requires both session scope and matching transcript/selection.
claude_scope="$(printf '%s' "$(cd "$script_dir/.." && pwd -P)" | cksum | awk '{print $1}')"
claude_cache="$XDG_CACHE_HOME/weave-router/claude-context"
mkdir -p "$claude_cache"
make_snapshot
cp "$fixture" "$claude_cache/$claude_scope-session-context.json"
printf '%s\n' '{"type":"assistant","message":{"model":"gpt-5.6-luna"}}' >"$work/transcript.jsonl"
claude_payload="$(jq -nc --arg path "$work/transcript.jsonl" '{session_id:"session-context",model:{id:"gpt-5.6-sol"},transcript_path:$path}')"
contains "$(printf '%s' "$claude_payload" | "$claude")" 'last Router ctx est. ~72k/128k'
native="$(jq '.context_window={context_window_size:128000,used_percentage:58}' <<<"$claude_payload")"
line="$(printf '%s' "$native" | "$claude")"
contains "$line" 'Context 58%'
absent "$line" 'ctx est.'
printf '%s\n' '{"type":"assistant","message":{"model":"different-model"}}' >"$work/transcript.jsonl"
absent "$(printf '%s' "$claude_payload" | "$claude")" 'ctx est.'
# Claude's asynchronous context fallback must also preserve newer observations.
make_snapshot
jq '.context_snapshot.request_id="newer-claude" | .context_snapshot.requested_at |= sub("Z$"; ".9Z") | .context_snapshot.recorded_at |= sub("Z$"; ".9Z")' "$fixture" >"$claude_cache/$claude_scope-session-context.json"
jq '.context_snapshot.request_id="older-claude" | .context_snapshot.requested_at |= sub("Z$"; ".1Z") | .context_snapshot.recorded_at |= sub("Z$"; ".1Z")' "$fixture" >"$work/older-claude.json"
rm -f "$work/fetch-started"
printf '%s' "$claude_payload" | PATH="$work/curl-bin:$PATH" WEAVE_TEST_FETCH_STARTED="$work/fetch-started" WEAVE_TEST_CURL="$real_curl" \
  WEAVE_ROUTER_BASE_URL="file://$work/older-claude.json" WEAVE_ROUTER_KEY=rk_synthetic "$claude" >/dev/null
for _ in $(seq 1 40); do
  [ -f "$work/fetch-started" ] && [ ! -d "$claude_cache/$claude_scope-session-context.json.lock" ] && break
  sleep 0.1
done
[ -f "$work/fetch-started" ] && [ ! -d "$claude_cache/$claude_scope-session-context.json.lock" ]
[ "$(jq -r '.context_snapshot.request_id' "$claude_cache/$claude_scope-session-context.json")" = newer-claude ] || { echo 'an older Claude observation overwrote the newer snapshot' >&2; exit 1; }
# Overlapping redraws share the in-flight refresh instead of invalidating it.
make_snapshot
jq '.context_snapshot.request_id="overlap-claude"' "$fixture" > "$work/overlap-claude.json"
rm -f "$claude_cache/$claude_scope-session-context.json" "$work/fetch-started"
mkdir "$work/delayed-curl-bin"
cat > "$work/delayed-curl-bin/curl" <<'CURL'
#!/usr/bin/env bash
: > "$WEAVE_TEST_FETCH_STARTED"
for _ in $(seq 1 100); do
  if [ -f "$WEAVE_TEST_FETCH_RELEASE" ]; then exec "$WEAVE_TEST_CURL" "$@"; fi
  sleep 0.05
done
exit 1
CURL
chmod +x "$work/delayed-curl-bin/curl"
printf '%s' "$claude_payload" | PATH="$work/delayed-curl-bin:$PATH" WEAVE_TEST_FETCH_STARTED="$work/fetch-started" WEAVE_TEST_FETCH_RELEASE="$work/fetch-release" WEAVE_TEST_CURL="$real_curl" \
  WEAVE_ROUTER_BASE_URL="file://$work/overlap-claude.json" WEAVE_ROUTER_KEY=rk_synthetic "$claude" >/dev/null
for _ in $(seq 1 40); do
  [ -f "$work/fetch-started" ] && break
  sleep 0.1
done
[ -f "$work/fetch-started" ]
printf '%s' "$claude_payload" | WEAVE_ROUTER_BASE_URL="file://$work/overlap-claude.json" WEAVE_ROUTER_KEY=rk_synthetic "$claude" >/dev/null
: > "$work/fetch-release"
for _ in $(seq 1 40); do
  [ ! -d "$claude_cache/$claude_scope-session-context.json.lock" ] && break
  sleep 0.1
done
[ ! -d "$claude_cache/$claude_scope-session-context.json.lock" ]
[ -f "$claude_cache/$claude_scope-session-context.json" ] || { echo 'an overlapping redraw discarded a successful Claude refresh' >&2; exit 1; }
[ "$(jq -r '.context_snapshot.request_id' "$claude_cache/$claude_scope-session-context.json")" = overlap-claude ]
printf '%s\n' '{"type":"assistant","message":{"model":"gpt-5.6-luna"}}' >"$work/transcript.jsonl"
contains "$(printf '%s' "$claude_payload" | "$claude")" 'last Router ctx est. ~72k/128k'
echo 'Context status tests passed'
