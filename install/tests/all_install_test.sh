#!/usr/bin/env bash
#
# Regression tests for `--all`: harness detection, the install plan, the
# enrollment exchange request and what gets installed from its response.
#
# Fully offline: each case gets an isolated $HOME and a PATH holding only the
# system tools plus fakes. A fake curl records the exchange request and returns
# a canned response; a fake install.sh records the router targets it is asked
# to install. Run directly or via `make test-install`.

# shellcheck disable=SC2016 # Canned responses and hostile inputs must stay literal.

set -uo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
all_installer="$script_dir/../all.sh"
[ -f "$all_installer" ] || { echo "cannot find all.sh at $all_installer" >&2; exit 1; }

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

pass=0
fail=0
ok() { echo "  ok   $1"; pass=$((pass + 1)); }
no() { echo "  FAIL $1"; echo "         expected: $2"; echo "         actual:   $3"; fail=$((fail + 1)); }
check() {
  if [ "$2" = "$3" ]; then ok "$1"; else no "$1" "$3" "$2"; fi
}
check_contains() {
  case "$2" in
    *"$3"*) ok "$1" ;;
    *) no "$1" "contains: $3" "$2" ;;
  esac
}
check_lacks() {
  case "$2" in
    *"$3"*) no "$1" "does not contain: $3" "$2" ;;
    *) ok "$1" ;;
  esac
}

# The installer under test is a copy placed beside a fake install.sh, so all.sh
# resolves its sibling instead of the real installer.
stage="$work/stage"
mkdir -p "$stage"
cp "$all_installer" "$stage/all.sh"
cat >"$stage/install.sh" <<'FAKE'
#!/usr/bin/env bash
printf '%s key=%s\n' "$*" "$WEAVE_ROUTER_KEY" >>"$RECORD_DIR/router_installs"
printf '%s\n' "${WEAVE_ROUTER_URL:-unset}" >"$RECORD_DIR/router_url"
case " $* " in *" --$FAIL_ROUTER_TARGET "*) exit 1 ;; esac
FAKE

fake_bin="$work/bin"
mkdir -p "$fake_bin"
for tool in bash cat tr cut od mkdir chmod head mktemp rm dirname jq printf; do
  tool_path="$(command -v "$tool")" && ln -s "$tool_path" "$fake_bin/$tool"
done
cat >"$fake_bin/curl" <<'FAKE'
#!/usr/bin/env bash
case " $* " in *" -o "*) exit 22 ;; esac
cat >"$RECORD_DIR/curl_stdin"
printf '%s\n' "$@" >"$RECORD_DIR/curl_args"
previous=""
for arg in "$@"; do
  [ "$previous" = "-d" ] && printf '%s' "$arg" >"$RECORD_DIR/request"
  previous="$arg"
done
[ "${EXCHANGE_UNREACHABLE:-}" = "true" ] && exit 7
printf '%s\n%s' "$EXCHANGE_RESPONSE" "${EXCHANGE_STATUS:-200}"
FAKE
chmod +x "$fake_bin/curl"

new_home() {
  local home="$work/$1" dir
  shift
  mkdir -p "$home/record"
  for dir in "$@"; do mkdir -p "$home/$dir"; done
  printf '%s' "$home"
}

# run_all <home> <args...>: prints combined output, returns all.sh's status.
run_all() {
  local home="$1"
  shift
  HOME="$home" PATH="$fake_bin" RECORD_DIR="$home/record" \
    EXCHANGE_RESPONSE="${EXCHANGE_RESPONSE:-}" EXCHANGE_STATUS="${EXCHANGE_STATUS:-200}" \
    EXCHANGE_UNREACHABLE="${EXCHANGE_UNREACHABLE:-}" FAIL_ROUTER_TARGET="${FAIL_ROUTER_TARGET:-none}" \
    WEAVE_INSTALL_TOKEN="" WEAVE_APP_URL="" \
    "$fake_bin/bash" "$stage/all.sh" "$@" </dev/null 2>&1
}

echo "detection and plan"
home="$(new_home detect .claude .cursor .config/opencode)"
output="$(run_all "$home" --all --dry-run)"; status=$?
check "dry run succeeds" "$status" "0"
check_contains "lists what was found" "$output" "Found: Claude Code, opencode, Cursor"
check_contains "lists what was not found" "$output" "Not found: Codex, Copilot CLI, pi, Grok Build"
check_contains "plans router targets" "$output" "Router: claude, opencode"
check_contains "plans plugins" "$output" "Plugins: claude_code, cursor"
check "dry run calls nothing" "$(ls "$home/record")" ""
check "dry run writes no machine id" "$([ -e "$home/.weave/machine-id" ] && echo present || echo absent)" "absent"

home="$(new_home detect_bin)"
printf '#!/usr/bin/env bash\n' >"$fake_bin/codex"; chmod +x "$fake_bin/codex"
output="$(run_all "$home" --all --dry-run)"
rm -f "$fake_bin/codex"
check_contains "a binary on PATH counts as present" "$output" "Found: Codex"

home="$(new_home detect_cursor_cli)"
printf '#!/usr/bin/env bash\n' >"$fake_bin/cursor-agent"; chmod +x "$fake_bin/cursor-agent"
output="$(run_all "$home" --all --dry-run)"
rm -f "$fake_bin/cursor-agent"
check_contains "the Cursor CLI binary counts as Cursor" "$output" "Found: Cursor"

home="$(new_home empty)"
output="$(run_all "$home" --all --token wenroll_t)"; status=$?
check "no harness is an error" "$status" "1"
check_contains "no harness says what to do" "$output" "No supported coding agents found"
check "no harness spends no token" "$(ls "$home/record")" ""

echo "exchange and install"
home="$(new_home install .claude .codex .cursor .copilot)"
EXCHANGE_RESPONSE='{"router_key":"rk_test_key","plugins":[{"kind":"claude_code","install_command":"echo plugin-claude >>\"$RECORD_DIR/plugin_installs\""},{"kind":"cursor","install_command":"echo plugin-cursor >>\"$RECORD_DIR/plugin_installs\""}],"unsupported":["copilot"],"failed":["codex"]}'
output="$(run_all "$home" --all --token wenroll_secret_token --app-url http://localhost:8001)"; status=$?
machine="$(cat "$home/.weave/machine-id")"
check "request names the machine and only detected plugins" "$(cat "$home/record/request")" \
  "{\"machine_id\":\"$machine\",\"need_router_key\":true,\"plugins\":[\"claude_code\",\"codex\",\"copilot\",\"cursor\"]}"
check "token travels on stdin" "$(cat "$home/record/curl_stdin")" 'header = "Authorization: Bearer wenroll_secret_token"'
check_lacks "token is not in curl argv" "$(cat "$home/record/curl_args")" "wenroll_secret_token"
check_contains "posts to the chosen app" "$(cat "$home/record/curl_args")" "http://localhost:8001/api/install/exchange"
check "router installed for each router target" "$(cat "$home/record/router_installs")" \
  "--claude --non-interactive key=rk_test_key
--codex --non-interactive key=rk_test_key
--copilot --non-interactive key=rk_test_key"
check "granted plugins installed" "$(cat "$home/record/plugin_installs")" "plugin-claude
plugin-cursor"
check "a failed plugin fails the run" "$status" "1"
check_contains "reports the failed plugin" "$output" "Failed: codex plugin"
check_contains "points at the installer's own error for the fix" "$output" "Fix the errors shown above, then copy a new command"
check_contains "reports the skipped plugin" "$output" "No one-command plugin install yet: copilot"
check_lacks "never prints the router key" "$output" "rk_test_key"
check_lacks "never prints the token" "$output" "wenroll_secret_token"

output="$(run_all "$home" --all --token wenroll_second --app-url http://localhost:8001)"
check "a re-run keeps the machine id" "$(cat "$home/.weave/machine-id")" "$machine"

home="$(new_home plugin_only .cursor)"
EXCHANGE_RESPONSE='{"plugins":[{"kind":"cursor","install_command":"true"}],"unsupported":[],"failed":[]}'
output="$(run_all "$home" --all --token wenroll_t)"; status=$?
check "plugin-only machine succeeds" "$status" "0"
check_contains "plugin-only machine asks for no router key" "$(cat "$home/record/request")" '"need_router_key":false'
check "plugin-only machine runs no router install" "$([ -e "$home/record/router_installs" ] && echo ran || echo none)" "none"
check_contains "defaults to the hosted app" "$(cat "$home/record/curl_args")" "https://app.weaveos.com/api/install/exchange"

home="$(new_home router_fails .claude .codex)"
EXCHANGE_RESPONSE='{"router_key":"rk_test_key","plugins":[],"unsupported":[],"failed":[]}'
output="$(FAIL_ROUTER_TARGET=codex run_all "$home" --all --token wenroll_t)"; status=$?
check "a failed router target fails the run" "$status" "1"
check_contains "the other router target still installs" "$output" "Installed: router for claude"
check_contains "reports the failed router target" "$output" "Failed: router for codex"

home="$(new_home other_router .claude)"
output="$(WEAVE_ROUTER_URL=https://router.other.example run_all "$home" --all --token wenroll_t)"
check "a hosted key is not sent to another router" "$(cat "$home/record/router_url")" "unset"
output="$(WEAVE_ROUTER_URL=http://localhost:8080 run_all "$home" --all --token wenroll_t --app-url http://localhost:8001)"
check "a non-hosted app keeps the chosen router" "$(cat "$home/record/router_url")" "http://localhost:8080"

for hosted_spelling in https://app.weaveos.com:443 https://App.WeaveOS.com/ https://app.weaveos.com.; do
  output="$(WEAVE_ROUTER_URL=https://router.other.example run_all "$home" --all --token wenroll_t --app-url "$hosted_spelling")"
  check "hosted app spelled $hosted_spelling still keeps its key off another router" "$(cat "$home/record/router_url")" "unset"
done
output="$(run_all "$home" --all --token wenroll_t --app-url https://weave.example/Team.A/)"
check_contains "a custom app path is posted as given" "$(cat "$home/record/curl_args")" "https://weave.example/Team.A/api/install/exchange"

home="$(new_home dropped_plugin .cursor .grok)"
EXCHANGE_RESPONSE='{"plugins":[{"kind":"cursor","install_command":""}],"unsupported":[],"failed":[]}'
output="$(run_all "$home" --all --token wenroll_t)"; status=$?
check "a plugin the response drops fails the run" "$status" "1"
check_contains "reports a dropped plugin and an empty command" "$output" "Failed: cursor plugin, grok_build plugin"

home="$(new_home no_installer .claude .cursor)"
EXCHANGE_RESPONSE='{"router_key":"rk_test_key","plugins":[{"kind":"claude_code","install_command":"true"},{"kind":"cursor","install_command":"true"}],"unsupported":[],"failed":[]}'
mv "$stage/install.sh" "$stage/install.sh.away"
output="$(run_all "$home" --all --token wenroll_t)"; status=$?
mv "$stage/install.sh.away" "$stage/install.sh"
check "a missing installer fails the run" "$status" "1"
check_contains "plugins still install without the router installer" "$output" "Installed: claude_code plugin, cursor plugin"
check_contains "reports the router target as failed" "$output" "Failed: router for claude"

echo "refusals"
home="$(new_home refused .claude)"
EXCHANGE_RESPONSE='{"code":"invalid_token","message":"This install command has expired."}'
output="$(EXCHANGE_STATUS=401 run_all "$home" --all --token wenroll_t)"; status=$?
check "a refused token is an error" "$status" "1"
check_contains "shows the server's reason" "$output" "This install command has expired."
check "a refused token installs nothing" "$([ -e "$home/record/router_installs" ] && echo ran || echo none)" "none"

home="$(new_home unreachable .claude)"
output="$(EXCHANGE_UNREACHABLE=true run_all "$home" --all --token wenroll_t)"; status=$?
check "an unreachable app is an error" "$status" "1"
check_contains "an unreachable app says so" "$output" "Could not reach"

home="$(new_home hostile .claude)"
for hostile_token in 'a"b' 'a b' "a'; rm -rf /" 'a$(id)'; do
  output="$(run_all "$home" --all --token "$hostile_token")"; status=$?
  check "rejects hostile token" "$status" "2"
done
for hostile_url in 'http://evil.example' 'https://evil.example/$(id)' 'file:///etc/passwd' 'https://a b'; do
  output="$(run_all "$home" --all --token wenroll_t --app-url "$hostile_url")"; status=$?
  check "rejects app url $hostile_url" "$status" "2"
done
output="$(run_all "$home" --all)"; status=$?
check "a missing token is an error" "$status" "2"
output="$(run_all "$home" --all --claude --token wenroll_t)"; status=$?
check "an unknown flag is an error" "$status" "2"
check "refusals call nothing" "$(ls "$home/record")" ""

home="$(new_home hostile_machine_id .claude)"
mkdir -p "$home/.weave"
printf 'abc"; rm -rf /\n' >"$home/.weave/machine-id"
EXCHANGE_RESPONSE='{"router_key":"rk_test_key","plugins":[],"unsupported":[],"failed":[]}'
output="$(run_all "$home" --all --token wenroll_t)"
check_contains "a tampered machine id is reduced to safe characters" "$(cat "$home/record/request")" '"machine_id":"abcrm-rf"'

echo
echo "passed: $pass  failed: $fail"
[ "$fail" -eq 0 ]
