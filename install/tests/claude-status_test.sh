#!/usr/bin/env bash
# Offline status regressions exercise the full CLI with synthetic config only.
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
installer="${INSTALLER:-$script_dir/../install.sh}"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/bin"

cat >"$work/bin/curl" <<'CURL'
#!/usr/bin/env bash
set -euo pipefail
out="" url="" method=GET
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    -X) method="$2"; shift 2 ;;
    -H|--header|-w|--max-time) shift 2 ;;
    http*) url="$1"; shift ;;
    *) shift ;;
  esac
done
printf '%s %s\n' "$method" "$url" >>"$REQUEST_LOG"
case "$method $url" in
  'GET https://router.example.test/validate')
    printf '{}' >"$out"
    printf '%s' "${VALIDATE_STATUS:-200}"
    ;;
  'GET https://proxy.example.test/validate')
    printf '{}' >"$out"
    printf '401'
    ;;
  'GET https://router.example.test/v1/subscriptions/accounts'|'GET https://proxy.example.test/v1/subscriptions/accounts')
    printf '[]' >"$out"
    printf '200'
    ;;
  *) exit 22 ;;
esac
CURL
chmod +x "$work/bin/curl"
export PATH="$work/bin:$PATH" NO_COLOR=1
export REQUEST_LOG="$work/requests.log"
unset WEAVE_ROUTER_KEY

failures=0
assert_contains() {
  if [[ "$2" == *"$3"* ]]; then
    printf 'PASS: %s\n' "$1"
  else
    printf 'FAIL: %s\nExpected: %s\nOutput: %s\n' "$1" "$3" "$2" >&2
    failures=$((failures + 1))
  fi
}

assert_not_contains() {
  if [[ "$2" == *"$3"* ]]; then
    printf 'FAIL: %s\nUnexpected: %s\nOutput: %s\n' "$1" "$3" "$2" >&2
    failures=$((failures + 1))
  else
    printf 'PASS: %s\n' "$1"
  fi
}

run_status() {
  local config_root="$1" scope="$2"
  shift 2
  if [ "$scope" = project ]; then
    (cd "$config_root" && bash "$installer" status --claude --scope project --quiet --non-interactive "$@") 2>&1
  else
    bash "$installer" status --claude --dir "$config_root" --quiet --non-interactive "$@" 2>&1
  fi
}

for scope in user project; do
  config_root="$work/$scope"
  mkdir -p "$config_root/.claude"
  if [ "$scope" = project ]; then git -C "$config_root" init -q; fi
  cat >"$config_root/.claude/.weave-parked.json" <<'JSON'
{"env":{"ANTHROPIC_BASE_URL":"https://router.example.test","ANTHROPIC_CUSTOM_HEADERS":"X-Weave-Router-Key: rk_synthetic_status_1234"}}
JSON
  if [ "$scope" = project ]; then
    printf '%s\n' '{"env":{"ANTHROPIC_BASE_URL":"https://router.example.test"}}' >"$config_root/.claude/settings.json"
    printf '%s\n' '{"env":{"ANTHROPIC_BASE_URL":"https://api.anthropic.com"}}' >"$config_root/.claude/settings.local.json"
  else
    printf '%s\n' '{"env":{}}' >"$config_root/.claude/settings.json"
  fi

  before="$(find "$config_root/.claude" -type f -exec cksum {} \; | sort)"
  status_output="$(run_status "$config_root" "$scope")"
  assert_contains "$scope parked install reports off" "$status_output" 'Claude Code  off'
  assert_not_contains "$scope parked install does not report active routing" "$status_output" 'Claude Code  on'
  assert_contains "$scope status can authenticate while off" "$status_output" 'accepted'
  assert_contains "$scope status separates authentication from inference" "$status_output" 'inference not checked'
  assert_contains "$scope status identifies settings being inspected" "$status_output" "$config_root/.claude"
  assert_contains "$scope status explains running-session limitation" "$status_output" 'Restart Claude Code'
  after="$(find "$config_root/.claude" -type f -exec cksum {} \; | sort)"
  if [ "$before" != "$after" ]; then
    printf 'FAIL: status modified %s config\n' "$scope" >&2
    failures=$((failures + 1))
  fi
done

config_root="$work/user"
mv "$config_root/.claude/.weave-parked.json" "$config_root/.claude/settings.json"
status_output="$(run_status "$config_root" user)"
assert_contains 'active user config reports on' "$status_output" 'Claude Code  on'
cp "$config_root/.claude/settings.json" "$config_root/.claude/.weave-parked.json"
status_output="$(run_status "$config_root" user)"
assert_contains 'stale parked file cannot override active user settings' "$status_output" 'Claude Code  on'
mv "$config_root/.claude/.weave-parked.json" "$config_root/saved-router.json"

status_output="$(VALIDATE_STATUS=401 run_status "$config_root" user)"
assert_contains 'invalid key is reported separately from config state' "$status_output" 'rejected'
assert_contains 'unavailable router does not hide active config' "$status_output" 'Claude Code  on'

printf '%s\n' '{"env":{"ANTHROPIC_BASE_URL":"https://router.example.test"}}' >"$config_root/.claude/settings.json"
status_output="$(WEAVE_ROUTER_KEY=rk_synthetic_status_1234 run_status "$config_root" user)"
assert_contains 'CLI-only key does not imply Claude is authenticated' "$status_output" 'router key header is missing'
assert_not_contains 'missing live key is not on' "$status_output" 'Claude Code  on'

config_root="$work/project"
mv "$config_root/.claude/.weave-parked.json" "$config_root/saved-router.json"
printf '%s\n' '{"env":{"ANTHROPIC_BASE_URL":"https://api.anthropic.com","ANTHROPIC_CUSTOM_HEADERS":"X-Weave-Router-Key: rk_synthetic_status_1234"}}' >"$config_root/.claude/settings.local.json"
status_output="$(run_status "$config_root" project)"
assert_contains 'local direct override without parked file reports off' "$status_output" 'Claude Code  off'
assert_not_contains 'committed URL cannot override local direct setting' "$status_output" 'Claude Code  on'
status_output="$(run_status "$config_root" project --dir "$config_root")"
assert_contains 'explicit project directory respects local direct override' "$status_output" 'Claude Code  off'

printf '%s\n' '{"env":{"ANTHROPIC_CUSTOM_HEADERS":"X-Weave-Router-Key: rk_synthetic_status_1234"}}' >"$config_root/.claude/settings.local.json"
status_output="$(run_status "$config_root" project)"
assert_contains 'split project URL and local key reports on' "$status_output" 'Claude Code  on'
status_output="$(run_status "$config_root" project --dir "$config_root")"
assert_contains 'explicit project directory combines committed URL and local key' "$status_output" 'Claude Code  on'

printf '%s\n' '{}' >"$config_root/.claude/settings.json"
cp "$config_root/saved-router.json" "$config_root/.claude/settings.local.json"
status_output="$(run_status "$config_root" project)"
assert_contains 'local-only project config reports on' "$status_output" 'Claude Code  on'
status_output="$(run_status "$config_root" project --dir "$config_root")"
assert_contains 'explicit project directory reads local-only config' "$status_output" 'Claude Code  on'
cp "$config_root/saved-router.json" "$config_root/.claude/.weave-parked.json"
status_output="$(run_status "$config_root" project)"
assert_contains 'stale parked file cannot override active project settings' "$status_output" 'Claude Code  on'
status_output="$(run_status "$config_root" project --dir "$config_root")"
assert_contains 'explicit project directory ignores stale parked credentials' "$status_output" 'Claude Code  on'
mv "$config_root/.claude/.weave-parked.json" "$config_root/saved-router.json"

cp "$config_root/saved-router.json" "$config_root/.claude/settings.json"
printf '%s\n' '{"env":{"ANTHROPIC_CUSTOM_HEADERS":"X-App: claude-code"}}' >"$config_root/.claude/settings.local.json"
status_output="$(run_status "$config_root" project)"
assert_contains 'local headers override a committed router key' "$status_output" 'router key header is missing'
assert_not_contains 'overridden key is not active routing' "$status_output" 'Claude Code  on'
status_output="$(run_status "$config_root" project --dir "$config_root")"
assert_contains 'explicit project directory respects local header override' "$status_output" 'router key header is missing'
assert_not_contains 'explicit project directory does not use overridden key' "$status_output" 'on in saved settings'

assert_not_contains 'status output redacts credentials' "$status_output" 'rk_synthetic_status_1234'

config_root="$work/user"
printf '%s\n' '{"env":{"ANTHROPIC_BASE_URL":"https://proxy.example.test","ANTHROPIC_CUSTOM_HEADERS":"Authorization: Bearer proxy_synthetic_status_1234"}}' >"$config_root/.claude/settings.json"
status_output="$(WEAVE_ROUTER_KEY=rk_synthetic_status_1234 run_status "$config_root" user)"
assert_contains 'proxy without Weave header identifies missing Weave authentication' "$status_output" 'Weave-key authentication is not configured'
assert_contains 'proxy authentication is not assessed by the status check' "$status_output" 'other authentication methods are not checked'
assert_not_contains 'proxy credentials are not declared invalid' "$status_output" "requests won't authenticate"
assert_not_contains 'proxy credentials do not prompt a reinstall' "$status_output" 'Run the installer to add your key'
assert_not_contains 'status output redacts proxy credentials' "$status_output" 'proxy_synthetic_status_1234'
assert_not_contains 'status never performs inference' "$(cat "$REQUEST_LOG")" '/v1/messages'
assert_not_contains 'status never mutates router state' "$(cat "$REQUEST_LOG")" 'POST '

mixed_root="$work/mixed"
mkdir -p "$mixed_root/.claude" "$mixed_root/.codex"
printf '%s\n' '{"env":{"ANTHROPIC_BASE_URL":"https://router.example.test"}}' >"$mixed_root/.claude/settings.json"
cat >"$mixed_root/.codex/config.toml" <<'TOML'
model_provider = "weave"

[model_providers.weave]
base_url = "https://router.example.test"
name = "Weave Router"
wire_api = "responses"

[model_providers.weave.http_headers]
X-Weave-Router-Key = "rk_synthetic_status_1234"
TOML
status_rc=0
status_output="$(bash "$installer" status --dir "$mixed_root" --quiet --non-interactive 2>&1)" || status_rc=$?
assert_contains 'Claude without a key still gets its own row' "$status_output" 'Claude Code  router key missing'
assert_contains 'a keyless Claude does not hide a working Codex' "$status_output" 'Codex        on'
assert_contains 'status checks the Codex key when Claude has none' "$status_output" 'accepted'
assert_contains 'keyless Claude status exits 0' "$status_rc" '0'

empty_root="$work/empty"
mkdir -p "$empty_root"
status_rc=0
status_output="$(bash "$installer" status --dir "$empty_root" --quiet --non-interactive 2>&1)" || status_rc=$?
assert_contains 'empty install reports no router' "$status_output" 'Router   not set up'
assert_contains 'empty install lists Claude with its install command' "$status_output" 'Claude Code  not set up'
assert_contains 'empty install lists Codex with its install command' "$status_output" 'Codex        not set up'
assert_contains 'empty install status exits 0' "$status_rc" '0'
printf '\nFailures: %s\n' "$failures"
[ "$failures" -eq 0 ]
