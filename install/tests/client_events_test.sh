#!/usr/bin/env bash
#
# Regression tests for the harness lifecycle ping: a real off/on toggle and an
# uninstall POST one authenticated event to the install's router, and every
# no-op or failure leaves the local result untouched.
#
# Fully offline: an isolated HOME keeps real config untouched, and a fake curl
# on PATH logs every request (method, path, body, argv, and whether the
# install's config still existed when the POST fired). The ping is detached,
# so assertions poll briefly for the log line instead of reading it at once.

set -uo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
installer="${INSTALLER:-$script_dir/../install.sh}"
uninstaller="${UNINSTALLER:-$(dirname "$installer")/uninstall.sh}"
[ -f "$installer" ] || { echo "cannot find installer at $installer" >&2; exit 1; }
[ -f "$uninstaller" ] || { echo "cannot find uninstaller at $uninstaller" >&2; exit 1; }

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

fake_bin="$work/bin"
mkdir -p "$fake_bin"

# Fake curl. Install-time probes get a connection failure (the installer
# tolerates that). /v1/client-events is logged to $EVENT_LOG as
# "METHOD PATH BODY CONFIG_STATE" where CONFIG_STATE reports whether
# $WATCH_FILE still existed at POST time. $CURL_MODE decides the outcome:
#   ok   — 204
#   fail — connection failure
#   hang — sleeps past the toggle's timeout
# Every argv is appended to $ARGV_LOG so a test can prove the key never rode
# on the command line; header-file contents go to $KEY_LOG.
cat >"$fake_bin/curl" <<'FAKE_CURL'
#!/usr/bin/env bash
[ -n "${ARGV_LOG:-}" ] && printf '%s\n' "$@" >>"$ARGV_LOG"
method="GET"; url=""; body=""; header_source=""
while [ $# -gt 0 ]; do
  case "$1" in
    -X) method="$2"; shift 2 ;;
    --data-binary|-d)
      body="$2"
      case "$body" in @*) body="$(cat "${body#@}")" ;; esac
      shift 2
      ;;
    -o|-w|-H|--max-time) shift 2 ;;
    --header) header_source="$2"; shift 2 ;;
    http*) url="$1"; shift ;;
    *) shift ;;
  esac
done
path="${url#http://}"; path="${path#https://}"; path="/${path#*/}"
case "$path" in
  /v1/client-events)
    if [ -n "${KEY_LOG:-}" ]; then
      case "$header_source" in @*) cat "${header_source#@}" >>"$KEY_LOG" 2>/dev/null || true ;; esac
    fi
    [ -n "${HEADER_PATH_LOG:-}" ] && printf '%s\n' "${header_source#@}" >>"$HEADER_PATH_LOG"
    state="absent"
    [ -n "${WATCH_FILE:-}" ] && [ -f "$WATCH_FILE" ] && state="present"
    [ -n "${EVENT_LOG:-}" ] && printf '%s %s %s %s\n' "$method" "$path" "$body" "$state" >>"$EVENT_LOG"
    [ -n "${URL_LOG:-}" ] && printf '%s\n' "$url" >>"$URL_LOG"
    case "${CURL_MODE:-ok}" in
      hang) sleep 5; exit 28 ;;
      fail) exit 7 ;;
      *) exit 0 ;;
    esac
    ;;
esac
printf '000'
exit 7
FAKE_CURL
chmod +x "$fake_bin/curl"
test_path="$fake_bin:$PATH"

pass=0
fail=0
ok() { echo "  ok   $1"; pass=$((pass + 1)); }
no() { echo "  FAIL $1"; echo "         expected: $2"; echo "         actual:   $3"; fail=$((fail + 1)); }
check() { if [ "$2" = "$3" ]; then ok "$1"; else no "$1" "$3" "$2"; fi; }
# assert <name> <expected-description> <actual-on-failure> <command...>
assert() { local name="$1" want="$2" got="$3"; shift 3; if "$@"; then ok "$name"; else no "$name" "$want" "$got"; fi; }

event_log="$work/events.log"
argv_log="$work/argv.log"
key_log="$work/keys.log"
header_path_log="$work/header-paths.log"
url_log="$work/urls.log"
: >"$event_log"; : >"$argv_log"; : >"$key_log"; : >"$header_path_log"; : >"$url_log"

# wait_for_events N waits (briefly) for the detached ping to land N lines.
wait_for_events() {
  local want="$1"
  for _ in $(seq 1 50); do
    [ "$(wc -l <"$event_log" | tr -d ' ')" -ge "$want" ] && return 0
    sleep 0.1
  done
  return 1
}
event_count() { wc -l <"$event_log" | tr -d ' '; }
last_event() { tail -n 1 "$event_log"; }
# settle gives a ping that should NOT happen a moment to prove it didn't.
settle() { sleep 0.3; }

run() { # run <home> <env...> -- <args>
  local home="$1"; shift
  local -a env=()
  while [ "$1" != "--" ]; do env+=("$1"); shift; done
  shift
  env HOME="$home" XDG_CONFIG_HOME="$home/xdg" PATH="$test_path" NO_COLOR=1 \
    EVENT_LOG="$event_log" ARGV_LOG="$argv_log" KEY_LOG="$key_log" HEADER_PATH_LOG="$header_path_log" URL_LOG="$url_log" \
    ${env[@]+"${env[@]}"} bash "$installer" "$@" </dev/null >/dev/null 2>&1
}
run_uninstall() { # run_uninstall <home> <watch-file> -- <args>
  local home="$1" watch="$2"; shift 3
  env HOME="$home" XDG_CONFIG_HOME="$home/xdg" PATH="$test_path" NO_COLOR=1 \
    EVENT_LOG="$event_log" ARGV_LOG="$argv_log" KEY_LOG="$key_log" HEADER_PATH_LOG="$header_path_log" WATCH_FILE="$watch" \
    bash "$uninstaller" "$@" </dev/null >/dev/null 2>&1
}

# ---------- codex ----------
printf 'codex\n'
home="$work/codex"; mkdir -p "$home"
run "$home" WEAVE_ROUTER_KEY=rk_codex_secret_1234 -- --codex --scope user --quiet --base-url https://router.workweave.ai
config="$home/.codex/config.toml"
[ -f "$config" ] || { echo "codex install did not write $config" >&2; exit 1; }
settle
check "install itself reports nothing" "$(event_count)" "0"

run "$home" -- off --codex --scope user --quiet; status=$?
check "off exits 0" "$status" "0"
wait_for_events 1
check "off reports one event" "$(event_count)" "1"
check "off event body" "$(last_event)" 'POST /v1/client-events {"action":"off","harness":"codex"} absent'
assert "off toggled the config" "off comment" "$(cat "$config")" grep -q 'weave-router: off' "$config"

run "$home" -- off --codex --scope user --quiet
settle
check "already-off reports nothing" "$(event_count)" "1"

run "$home" -- on --codex --scope user --quiet
wait_for_events 2
check "on reports one event" "$(event_count)" "2"
check "on event body" "$(last_event)" 'POST /v1/client-events {"action":"on","harness":"codex"} absent'

run "$home" -- on --codex --scope user --quiet
settle
check "already-on reports nothing" "$(event_count)" "2"

run "$home" -- disable-routing --scope user --quiet
wait_for_events 3
check "disable-routing reports action=off" "$(last_event)" 'POST /v1/client-events {"action":"off","harness":"codex"} absent'
run "$home" -- on --codex --scope user --quiet
wait_for_events 4

check "key was sent in the header file" "$(grep -c 'X-Weave-Router-Key: rk_codex_secret_1234' "$key_log")" "4"
if grep -q 'rk_codex_secret_1234' "$argv_log"; then
  no "key never appears in curl argv" "no rk_ in argv" "$(grep rk_codex_secret_1234 "$argv_log")"
else
  ok "key never appears in curl argv"
fi
# The mode-600 header file carrying the key must not outlive the ping.
leftover=""
for _ in $(seq 1 50); do
  leftover=""
  while IFS= read -r hdr; do [ -n "$hdr" ] && [ -e "$hdr" ] && leftover="$hdr"; done <"$header_path_log"
  [ -z "$leftover" ] && break
  sleep 0.1
done
check "header files were written somewhere" "$(grep -c . "$header_path_log")" "4"
check "header files are removed after the ping" "$leftover" ""

# Failure modes: the toggle must still succeed and return promptly.
run "$home" CURL_MODE=fail -- off --codex --scope user --quiet; status=$?
check "off succeeds when curl fails" "$status" "0"
assert "off applied despite curl failure" "off comment" "$(cat "$config")" grep -q 'weave-router: off' "$config"
wait_for_events 5

start=$SECONDS
run "$home" CURL_MODE=hang -- on --codex --scope user --quiet; status=$?
elapsed=$((SECONDS - start))
check "on succeeds when curl hangs" "$status" "0"
assert "on does not wait for a hung ping (${elapsed}s)" "<5s" "${elapsed}s" [ "$elapsed" -lt 5 ]
assert "on applied despite hung ping" "provider line" "$(cat "$config")" grep -qx 'model_provider = "weave"' "$config"
wait_for_events 6

# An explicit --base-url is the endpoint the user vouched for, so the ping goes
# there rather than to the on-disk one the trust gate was skipped for.
run "$home" -- off --codex --scope user --quiet --base-url http://127.0.0.1:9
wait_for_events 7
check "explicit --base-url is the endpoint pinged" "$(tail -n 1 "$url_log")" "http://127.0.0.1:9/v1/client-events"
check "on-disk endpoint is pinged otherwise" "$(head -n 1 "$url_log")" "https://router.workweave.ai/v1/client-events"
run "$home" -- on --codex --scope user --quiet
wait_for_events 8

# Uninstall reports once, while the config (and its key) still exists.
run_uninstall "$home" "$config" -- --codex --scope user; status=$?
check "uninstall exits 0" "$status" "0"
wait_for_events 9
check "uninstall reports one event before removing the config" "$(last_event)" 'POST /v1/client-events {"action":"uninstall","harness":"codex"} present'
run_uninstall "$home" "$config" -- --codex --scope user
settle
check "uninstall of an absent install reports nothing" "$(event_count)" "9"

# Absent install: nothing to report.
empty="$work/empty"; mkdir -p "$empty"
run "$empty" -- off --codex --scope user --quiet; status=$?
check "off on an absent install exits 0" "$status" "0"
settle
check "off on an absent install reports nothing" "$(event_count)" "9"

# ---------- claude ----------
printf 'claude\n'
: >"$event_log"
home="$work/claude"; mkdir -p "$home"
run "$home" WEAVE_ROUTER_KEY=rk_claude_secret_1234 -- --claude --scope user --quiet --non-interactive --base-url https://router.workweave.ai
settings="$home/.claude/settings.json"
[ -f "$settings" ] || { echo "claude install did not write $settings" >&2; exit 1; }
settle
check "install itself reports nothing" "$(event_count)" "0"

run "$home" -- off --claude --scope user --quiet; status=$?
check "off exits 0" "$status" "0"
wait_for_events 1
check "off reports via the parked sidecar" "$(last_event)" 'POST /v1/client-events {"action":"off","harness":"claude_code"} absent'
assert "off parked the router config" "sidecar" "missing" [ -f "$home/.claude/.weave-parked.json" ]
check "off sent the parked key" "$(grep -c 'X-Weave-Router-Key: rk_claude_secret_1234' "$key_log")" "1"

run "$home" -- off --claude --scope user --quiet
settle
check "already-off reports nothing" "$(event_count)" "1"

# A failure while preparing the ping (here: mktemp) must not abort the toggle,
# which runs under set -e and has already applied the change.
broken_bin="$work/broken-bin"; mkdir -p "$broken_bin"
printf '%s\n' '#!/usr/bin/env bash' 'exit 1' >"$broken_bin/mktemp"; chmod +x "$broken_bin/mktemp"
test_path="$broken_bin:$test_path"
run "$home" -- on --claude --scope user --quiet; status=$?
test_path="${test_path#"$broken_bin:"}"
check "on succeeds when the ping setup fails" "$status" "0"
assert "on applied despite the failed ping setup" "no sidecar" "present" [ ! -f "$home/.claude/.weave-parked.json" ]
settle
check "failed ping setup sends nothing" "$(event_count)" "1"
run "$home" -- off --claude --scope user --quiet
wait_for_events 2

run "$home" -- on --claude --scope user --quiet
wait_for_events 3
check "on reports one event" "$(last_event)" 'POST /v1/client-events {"action":"on","harness":"claude_code"} absent'

run "$home" -- on --claude --scope user --quiet
settle
check "already-on reports nothing" "$(event_count)" "3"

run_uninstall "$home" "$settings" -- --claude --scope user; status=$?
check "uninstall exits 0" "$status" "0"
wait_for_events 4
check "uninstall reports before scrubbing settings" "$(last_event)" 'POST /v1/client-events {"action":"uninstall","harness":"claude_code"} present'
if grep -q 'ANTHROPIC_CUSTOM_HEADERS' "$settings" 2>/dev/null; then
  no "uninstall scrubbed the key" "no header" "$(cat "$settings")"
else
  ok "uninstall scrubbed the key"
fi

# Toggled off at uninstall time: the key only lives in the sidecar, which is
# what the report must read before the uninstall deletes it.
run "$home" WEAVE_ROUTER_KEY=rk_claude_secret_1234 -- --claude --scope user --quiet --non-interactive --base-url https://router.workweave.ai
run "$home" -- off --claude --scope user --quiet
wait_for_events 5
run_uninstall "$home" "$home/.claude/.weave-parked.json" -- --claude --scope user
wait_for_events 6
check "uninstall while off reports from the sidecar" "$(last_event)" 'POST /v1/client-events {"action":"uninstall","harness":"claude_code"} present'
assert "uninstall removed the sidecar" "gone" "present" [ ! -f "$home/.claude/.weave-parked.json" ]

# ---------- opencode ----------
printf 'opencode\n'
: >"$event_log"
home="$work/opencode"; dir="$home/oc"; mkdir -p "$home" "$dir"
run "$home" WEAVE_ROUTER_KEY=rk_opencode_secret_1 -- --opencode --dir "$dir" --quiet --non-interactive --base-url https://router.workweave.ai
config="$dir/opencode.json"
[ -f "$config" ] || { echo "opencode install did not write $config" >&2; exit 1; }
settle
check "install itself reports nothing" "$(event_count)" "0"

run "$home" -- off --opencode --dir "$dir" --quiet; status=$?
check "off exits 0" "$status" "0"
wait_for_events 1
check "off reports one event" "$(last_event)" 'POST /v1/client-events {"action":"off","harness":"opencode"} absent'
run "$home" -- off --opencode --dir "$dir" --quiet
settle
check "already-off reports nothing" "$(event_count)" "1"
run "$home" -- on --opencode --dir "$dir" --quiet
wait_for_events 2
check "on reports one event" "$(last_event)" 'POST /v1/client-events {"action":"on","harness":"opencode"} absent'
run_uninstall "$home" "$config" -- --opencode --dir "$dir"
wait_for_events 3
check "uninstall reports before cleaning the config" "$(last_event)" 'POST /v1/client-events {"action":"uninstall","harness":"opencode"} present'

# ---------- pi ----------
printf 'pi\n'
: >"$event_log"
home="$work/pi"; mkdir -p "$home"
run "$home" WEAVE_ROUTER_KEY=rk_pi_secret_12345 -- --pi --scope user --quiet --non-interactive --base-url https://router.workweave.ai
models="$home/.pi/agent/models.json"
[ -f "$models" ] || models="$(find "$home" -name models.json | head -n 1)"
if [ -z "$models" ] || [ ! -f "$models" ]; then echo "pi install did not write models.json" >&2; exit 1; fi
settle
check "install itself reports nothing" "$(event_count)" "0"
run_uninstall "$home" "$models" -- --pi --scope user; status=$?
check "uninstall exits 0" "$status" "0"
wait_for_events 1
check "uninstall reports before cleaning models.json" "$(last_event)" 'POST /v1/client-events {"action":"uninstall","harness":"pi"} present'
check "pi key was sent in the header file" "$(grep -c 'X-Weave-Router-Key: rk_pi_secret_12345' "$key_log")" "1"

printf '\n%d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
