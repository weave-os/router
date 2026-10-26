#!/usr/bin/env bash

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
installer="${INSTALLER:-$script_dir/../install.sh}"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/bin" "$work/home" "$work/tmp"
export FAKE_CURL_LOG="$work/curl.log"
export FAKE_ENROLLMENTS="$work/enrollments.jsonl"
export FAKE_TOKEN_BODIES="$work/token-bodies.log"
export FAKE_BROWSER_LOG="$work/browser.log"

payload="$(printf '%s' '{"chatgpt_account_id":"chatgpt-test","chatgpt_user_id":"codex-user-test","email":"codex@example.test","organizations":[{"id":"org-test","name":"Codex Org"}]}' | openssl base64 -A | tr '+/' '-_' | tr -d '=')"
export FAKE_JWT="eyJhbGciOiJSUzI1NiJ9.$payload.signature"

cat >"$work/bin/curl" <<'FAKE_CURL'
#!/usr/bin/env bash
set -euo pipefail
out=""
data_file=""
url=""
want_status="false"
user_agent=""
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    -A) user_agent="$2"; shift 2 ;;
    -w) want_status="true"; shift 2 ;;
    --data-binary)
      data_file="${2#@}"
      case "$2" in *refresh-*|*access-*|*authorization-code*|*pkce-verifier*) exit 91 ;; esac
      shift 2
      ;;
    -H|--header|-X|--max-time) shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
printf '%s\t%s\t%s\n' "$url" "$data_file" "$user_agent" >>"$FAKE_CURL_LOG"
case "$url" in
  */api/accounts/deviceauth/usercode)
    printf '%s' '{"device_auth_id":"device-1","user_code":"ABCD-EFGH","interval":"1"}'
    ;;
  */api/accounts/deviceauth/token)
    printf '%s' '{"authorization_code":"authorization-code","code_verifier":"pkce-verifier"}'
    ;;
  */v1/oauth/token)
    { cat "$data_file"; echo; } >>"$FAKE_TOKEN_BODIES"
    printf '%s' "$FAKE_CLAUDE_RESPONSE"
    ;;
  */oauth/token)
    { cat "$data_file"; echo; } >>"$FAKE_TOKEN_BODIES"
    printf '{"id_token":"%s","access_token":"access-new","refresh_token":"refresh-new","expires_in":3600}' "$FAKE_JWT"
    ;;
  */validate)
    printf '%s' "${FAKE_VALIDATION_BODY:-\{\}}" >"$out"
    [ "$want_status" = "true" ] && printf '%s' "${FAKE_VALIDATION_STATUS:-200}"
    ;;
  */v1/subscriptions/accounts)
    if [ -n "${FAKE_ENROLLMENT_STATUS:-}" ]; then
      printf '%s' '{"error":"invalid_key"}' >"$out"
      [ "$want_status" = "true" ] && printf '%s' "$FAKE_ENROLLMENT_STATUS"
      exit 0
    fi
    if [ -n "$data_file" ]; then
      jq -e '.refresh_token | startswith("refresh-")' "$data_file" >/dev/null
      jq -c . "$data_file" >>"$FAKE_ENROLLMENTS"
      jq '{id:"opaque-1",provider,external_account_id,display_name,enabled:true}' "$data_file" >"$out"
      [ "$want_status" = "true" ] && printf '201'
    else
      printf '%s' '[{"id":"opaque-1","provider":"codex","external_account_id":"chatgpt-test","display_name":"Codex Org: codex@example.test","enabled":true}]' >"$out"
      [ "$want_status" = "true" ] && printf '200'
    fi
    ;;
  *) exit 22 ;;
esac
FAKE_CURL
chmod +x "$work/bin/curl"
# The fake browser plays the issuer: for an authorize URL with a localhost
# redirect it calls the installer's callback listener the way the real issuer
# redirects the user's browser after sign-in.
cat >"$work/bin/open" <<'FAKE_BROWSER'
#!/usr/bin/env python3
import os, socket, sys, urllib.error, urllib.parse, urllib.request
url = sys.argv[-1]
log = open(os.environ["FAKE_BROWSER_LOG"], "a")
log.write(url + "\n")
query = urllib.parse.parse_qs(urllib.parse.urlsplit(url).query)
redirect = query.get("redirect_uri", [""])[0]
mode = os.environ.get("FAKE_BROWSER", "approve")
if not redirect.startswith("http://") or mode == "ignore":
    sys.exit(0)
def callback(params):
    try:
        with urllib.request.urlopen(redirect + "?" + urllib.parse.urlencode(params), timeout=10) as response:
            return response.status
    except urllib.error.HTTPError as error:
        return error.code
def malformed_request():
    parts = urllib.parse.urlsplit(redirect)
    with socket.create_connection((parts.hostname, parts.port), timeout=10) as conn:
        conn.sendall(b"GET //[/ HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")
        return int(conn.recv(64).split()[1])
statuses = []
if mode == "mismatch":
    statuses.append(malformed_request())
    statuses.append(callback({"code": "attacker-code", "state": "not-this-login"}))
if mode == "error":
    statuses.append(callback({"error": "access_denied", "state": query["state"][0]}))
else:
    statuses.append(callback({"code": "authorization-code", "state": query["state"][0]}))
log.write("callback statuses: " + " ".join(map(str, statuses)) + "\n")
FAKE_BROWSER
chmod +x "$work/bin/open"
cp "$work/bin/open" "$work/bin/xdg-open"

# Browser sign-in is opt-in per test (DISPLAY below); the rest use the manual
# flows, so a developer's SSH session or desktop doesn't change the outcome.
common_env=(HOME="$work/home" PATH="$work/bin:$PATH" WEAVE_ROUTER_KEY="rk_test_secret" NO_COLOR=1
  TMPDIR="$work/tmp" SSH_CONNECTION= SSH_TTY= SSH_CLIENT= DISPLAY= WAYLAND_DISPLAY=)

for provider in claude codex; do
  : >"$FAKE_CURL_LOG"
  if rejected_output="$(env "${common_env[@]}" FAKE_VALIDATION_STATUS=401 \
      FAKE_VALIDATION_BODY='{"error":"invalid_key"}' \
      bash "$installer" login "$provider" --base-url https://router.example.test --non-interactive --quiet 2>&1)"; then
    echo "$provider login accepted a rejected Router key" >&2
    exit 1
  fi
  grep -Fq 'Weave Router is not set up with a valid API key' <<<"$rejected_output"
  grep -Fq -- "--$provider --scope user --rotate-key" <<<"$rejected_output"
  grep -Fq 'WEAVE_ROUTER_KEY' <<<"$rejected_output"
  if grep -Eq 'oauth|deviceauth|subscriptions/accounts' "$FAKE_CURL_LOG"; then
    echo "$provider login started authorization before validating the Router key" >&2
    exit 1
  fi
  if grep -Fq 'rk_test_secret' <<<"$rejected_output"; then
    echo 'rejected Router key leaked into terminal output' >&2
    exit 1
  fi
done

for saved_dir in "$work/saved-home" "$work/custom install"; do
  mkdir -p "$saved_dir/.claude"
  printf '%s' '{"env":{"ANTHROPIC_BASE_URL":"https://selfhosted.example.test","ANTHROPIC_AUTH_TOKEN":"rk_saved_fixture"}}' >"$saved_dir/.claude/settings.json"
  saved_args=()
  if [ "$saved_dir" = "$work/custom install" ]; then
    saved_args=(--dir "$saved_dir")
  fi
  if saved_output="$(env "${common_env[@]}" HOME="$saved_dir" WEAVE_ROUTER_URL= \
      FAKE_VALIDATION_STATUS=401 FAKE_VALIDATION_BODY='{"error":"invalid_key"}' \
      bash "$installer" login claude "${saved_args[@]}" --quiet 2>&1)"; then
    echo 'login accepted a rejected key for a saved self-hosted install' >&2
    exit 1
  fi
  grep -Fq -- '--claude --scope user --rotate-key --base-url https://selfhosted.example.test' <<<"$saved_output"
  if [ "${#saved_args[@]}" -gt 0 ]; then
    grep -Fq -- "--dir $(printf '%q' "$saved_dir")" <<<"$saved_output"
  fi
done

if missing_output="$(env "${common_env[@]}" bash "$installer" login claude --quiet 2>&1)"; then
  echo 'login succeeded without a Router install' >&2
  exit 1
fi
grep -Fq 'Weave Router is not installed for claude in the user scope' <<<"$missing_output"
grep -Fq 'npx @weave-os/router --claude --scope user' <<<"$missing_output"

if enrollment_output="$(env "${common_env[@]}" FAKE_ENROLLMENT_STATUS=401 \
    bash "$installer" login codex --device-code --base-url https://router.example.test --non-interactive --quiet 2>&1)"; then
  echo 'login accepted a key rejected during enrollment' >&2
  exit 1
fi
grep -Fq 'Weave Router is not set up with a valid API key' <<<"$enrollment_output"
grep -Fq -- '--codex --scope user --rotate-key' <<<"$enrollment_output"

for validation_status in 403 503; do
  if unavailable_output="$(env "${common_env[@]}" FAKE_VALIDATION_STATUS="$validation_status" \
      FAKE_VALIDATION_BODY='{"error":"fixture_router_unavailable"}' \
      bash "$installer" login codex --base-url https://router.example.test --non-interactive --quiet 2>&1)"; then
    echo "login continued after HTTP $validation_status" >&2
    exit 1
  fi
  grep -Fq 'fixture_router_unavailable' <<<"$unavailable_output"
  if grep -Fq -- '--rotate-key' <<<"$unavailable_output"; then
    echo "HTTP $validation_status was incorrectly reported as a rejected key" >&2
    exit 1
  fi
done

env "${common_env[@]}" bash "$installer" login codex --device-code --base-url https://router.example.test --non-interactive --quiet \
  | grep -Fq 'Codex subscription enrolled.'

# Login is interactive for OAuth, but it does not install a client config and
# therefore must not ask the unrelated user-vs-project scope question. Run it
# under a real TTY so the regression catches the prompt that a pipe would hide.
env "${common_env[@]}" python3 - "$installer" <<'PY'
import os
import pty
import select
import sys
import time

installer = sys.argv[1]
pid, fd = pty.fork()
if pid == 0:
    os.execvpe("bash", ["bash", installer, "login", "codex", "--device-code", "--base-url", "https://router.example.test", "--quiet"], os.environ)

output = bytearray()
deadline = time.monotonic() + 30
while True:
    remaining = deadline - time.monotonic()
    if remaining <= 0:
        os.kill(pid, 9)
        raise SystemExit("timed out waiting for interactive login")
    ready, _, _ = select.select([fd], [], [], remaining)
    if not ready:
        continue
    try:
        chunk = os.read(fd, 4096)
    except OSError:
        break
    if not chunk:
        break
    output.extend(chunk)
    # Keep the regression test from hanging if the old scope prompt is
    # reintroduced; the assertion below still reports that it appeared.
    if b"Install scope:" in output:
        os.write(fd, b"\n")

_, status = os.waitpid(pid, 0)
if b"Install scope:" in output:
    sys.stderr.write(output.decode(errors="replace"))
    raise SystemExit("login unexpectedly prompted for install scope")
if b"Codex subscription enrolled." not in output:
    sys.stderr.write(output.decode(errors="replace"))
    raise SystemExit("interactive login did not complete")
if not os.WIFEXITED(status) or os.WEXITSTATUS(status) != 0:
    raise SystemExit("interactive login failed")
PY

env "${common_env[@]}" python3 "$script_dir/claude_login_test.py" "$installer"

status_output="$(env "${common_env[@]}" bash "$installer" status --base-url https://router.example.test --quiet)"
grep -Fq 'Key      rk_…cret accepted' <<<"$status_output"
grep -Fq 'Router   https://router.example.test' <<<"$status_output"
grep -Fq 'codex  chatgpt-test  [Codex Org: codex@example.test]  enabled  ready' <<<"$status_output"
grep -Fq 'Codex Org: codex@example.test' <<<"$status_output"
if grep -Eq 'refresh-|access-' "$FAKE_CURL_LOG"; then
  echo 'refresh token leaked into curl argv log' >&2
  exit 1
fi

# OAuth issuers rate-limit curl's default user agent, so every token-endpoint
# call must identify the installer.
while IFS=$'\t' read -r logged_url _ logged_agent; do
  case "$logged_url" in
    *deviceauth*|*/oauth/token)
      if [ -z "$logged_agent" ]; then
        echo "OAuth call to $logged_url sent no explicit user agent" >&2
        exit 1
      fi
      ;;
  esac
done <"$FAKE_CURL_LOG"

# --- Browser sign-in through a localhost callback -------------------------
# The fake browser above plays the issuer and calls the installer's listener.
loop_env=("${common_env[@]}" DISPLAY=:99)
claude_response='{"access_token":"access-loop","refresh_token":"refresh-loop","account":{"uuid":"eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee","email_address":"loop@example.test"},"organization":{"uuid":"ffffffff-ffff-4fff-8fff-ffffffffffff","name":"Loop Org"}}'
holder_pids=()
trap 'for holder in "${holder_pids[@]:-}"; do [ -n "$holder" ] && kill "$holder" 2>/dev/null; done; rm -rf "$work"' EXIT

loop_fail() {
  echo "$1" >&2
  printf '%s\n' "${2:-}" >&2
  exit 1
}

reset_loop_logs() {
  : >"$FAKE_BROWSER_LOG"
  : >"$FAKE_TOKEN_BODIES"
  : >"$FAKE_CURL_LOG"
}

port_free() {
  node -e 'const s=require("net").createServer();s.on("error",()=>process.exit(1));s.listen(Number(process.argv[1]),"127.0.0.1",()=>s.close())' "$1"
}

hold_port() {
  node -e 'require("net").createServer().listen(Number(process.argv[1]),"127.0.0.1",()=>console.log("ready"))' "$1" >"$work/hold-$1" &
  holder_pids+=("$!")
  for _ in $(seq 50); do grep -q ready "$work/hold-$1" 2>/dev/null && return 0; sleep 0.1; done
  loop_fail "could not hold port $1"
}

assert_listener_gone() {
  port_free 1455 || loop_fail "callback listener still holds port 1455" "$1"
  if find "$work/tmp" -name 'weave-oauth.*' | grep -q .; then
    loop_fail "callback listener left its temp dir behind" "$1"
  fi
}

authorize_param() {
  python3 -c 'import sys, urllib.parse; q = urllib.parse.parse_qs(urllib.parse.urlsplit(sys.argv[1]).query); print(q.get(sys.argv[2], [""])[0])' "$1" "$2"
}

pkce_challenge() {
  printf '%s' "$1" | openssl dgst -sha256 -binary | openssl base64 -A | tr '+/' '-_' | tr -d '='
}

{ port_free 1455 && port_free 1457; } || loop_fail "ports 1455 and 1457 must be free to run the browser-login tests (is a codex login running?)"

# Codex: authorization code + PKCE through 127.0.0.1:1455, like `codex login`.
reset_loop_logs
enrolled_before="$(wc -l <"$FAKE_ENROLLMENTS")"
codex_loop_output="$(env "${loop_env[@]}" bash "$installer" login codex --base-url https://router.example.test --non-interactive --quiet 2>&1)" \
  || loop_fail "browser Codex login failed" "$codex_loop_output"
grep -Fq 'Opened your browser to sign in.' <<<"$codex_loop_output" || loop_fail "Codex login did not use the browser" "$codex_loop_output"
grep -Fq 'Codex subscription enrolled.' <<<"$codex_loop_output" || loop_fail "browser Codex login did not enroll" "$codex_loop_output"
[ "$(wc -l <"$FAKE_ENROLLMENTS")" -eq $((enrolled_before + 1)) ] || loop_fail "browser Codex login must enroll exactly once"
tail -n 1 "$FAKE_ENROLLMENTS" | jq -e '.provider == "codex" and .external_account_id == "chatgpt-test" and .refresh_token == "refresh-new" and .display_name == "Codex Org: codex@example.test"' >/dev/null \
  || loop_fail "browser Codex login changed the enrollment body" "$(tail -n 1 "$FAKE_ENROLLMENTS")"
codex_authorize="$(grep -m1 '/oauth/authorize' "$FAKE_BROWSER_LOG")"
[ "${codex_authorize%%\?*}" = "https://auth.openai.com/oauth/authorize" ] || loop_fail "wrong Codex authorize endpoint" "$codex_authorize"
[ "$(authorize_param "$codex_authorize" redirect_uri)" = "http://127.0.0.1:1455/auth/callback" ] || loop_fail "wrong Codex redirect" "$codex_authorize"
[ "$(authorize_param "$codex_authorize" client_id)" = "app_EMoamEEZ73f0CkXaXp7hrann" ] || loop_fail "wrong Codex client" "$codex_authorize"
[ "$(authorize_param "$codex_authorize" scope)" = "openid profile email offline_access api.connectors.read api.connectors.invoke" ] || loop_fail "wrong Codex scope" "$codex_authorize"
for param in response_type=code code_challenge_method=S256 id_token_add_organizations=true codex_cli_simplified_flow=true originator=codex_cli_rs; do
  [ "$(authorize_param "$codex_authorize" "${param%%=*}")" = "${param#*=}" ] || loop_fail "Codex authorize URL lacks $param" "$codex_authorize"
done
codex_form="$(tail -n 1 "$FAKE_TOKEN_BODIES")"
[ "$(authorize_param "?$codex_form" code)" = "authorization-code" ] || loop_fail "Codex exchanged the wrong code" "$codex_form"
[ "$(authorize_param "?$codex_form" redirect_uri)" = "http://127.0.0.1:1455/auth/callback" ] || loop_fail "Codex token redirect must match authorize" "$codex_form"
codex_verifier="$(authorize_param "?$codex_form" code_verifier)"
[ "$(pkce_challenge "$codex_verifier")" = "$(authorize_param "$codex_authorize" code_challenge)" ] || loop_fail "Codex verifier does not match its challenge"
if grep -Fq deviceauth "$FAKE_CURL_LOG"; then loop_fail "browser Codex login also ran the device-code flow"; fi
for secret in authorization-code "$codex_verifier" refresh-new access-new; do
  if grep -Fq -- "$secret" <<<"$codex_loop_output"; then loop_fail "browser Codex login printed a credential"; fi
done
if grep -Fq -- "$codex_verifier" "$FAKE_CURL_LOG"; then loop_fail "PKCE verifier leaked into curl argv"; fi
assert_listener_gone "$codex_loop_output"

# Claude: any free port, http://localhost:<port>/callback, like `claude login`.
reset_loop_logs
claude_loop_output="$(env "${loop_env[@]}" FAKE_CLAUDE_RESPONSE="$claude_response" \
  bash "$installer" login claude --base-url https://router.example.test --non-interactive --quiet 2>&1)" \
  || loop_fail "browser Claude login failed" "$claude_loop_output"
grep -Fq 'Claude subscription enrolled.' <<<"$claude_loop_output" || loop_fail "browser Claude login did not enroll" "$claude_loop_output"
if grep -Fq 'Paste the Claude authorization code' <<<"$claude_loop_output"; then loop_fail "browser Claude login asked for a code"; fi
claude_authorize="$(grep -m1 'oauth/authorize' "$FAKE_BROWSER_LOG")"
claude_redirect="$(authorize_param "$claude_authorize" redirect_uri)"
case "$claude_redirect" in
  http://localhost:[0-9]*/callback) ;;
  *) loop_fail "wrong Claude redirect" "$claude_authorize" ;;
esac
[ "$(authorize_param "$claude_authorize" code)" = "true" ] || loop_fail "Claude authorize URL lost code=true" "$claude_authorize"
[ "$(authorize_param "$claude_authorize" scope)" = "org:create_api_key user:profile user:inference" ] || loop_fail "wrong Claude scope" "$claude_authorize"
claude_body="$(tail -n 1 "$FAKE_TOKEN_BODIES")"
jq -e --arg redirect "$claude_redirect" --arg state "$(authorize_param "$claude_authorize" state)" \
  '.code == "authorization-code" and .redirect_uri == $redirect and .state == $state and .grant_type == "authorization_code"' <<<"$claude_body" >/dev/null \
  || loop_fail "Claude token exchange does not match the browser callback" "$claude_body"
[ "$(pkce_challenge "$(jq -r .code_verifier <<<"$claude_body")")" = "$(authorize_param "$claude_authorize" code_challenge)" ] \
  || loop_fail "Claude verifier does not match its challenge"
tail -n 1 "$FAKE_ENROLLMENTS" | jq -e '.provider == "claude" and .external_account_id == "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee:ffffffff-ffff-4fff-8fff-ffffffffffff" and .refresh_token == "refresh-loop" and .display_name == "Loop Org: loop@example.test"' >/dev/null \
  || loop_fail "browser Claude login changed the enrollment body" "$(tail -n 1 "$FAKE_ENROLLMENTS")"
for secret in authorization-code refresh-loop access-loop "$(jq -r .code_verifier <<<"$claude_body")"; do
  if grep -Fq -- "$secret" <<<"$claude_loop_output"; then loop_fail "browser Claude login printed a credential"; fi
done

# A malformed request and a callback with someone else's state are refused and ignored; the real one still lands.
reset_loop_logs
mismatch_output="$(env "${loop_env[@]}" FAKE_BROWSER=mismatch bash "$installer" login codex --base-url https://router.example.test --non-interactive --quiet 2>&1)" \
  || loop_fail "login failed after a foreign callback" "$mismatch_output"
grep -Fq 'callback statuses: 404 400 200' "$FAKE_BROWSER_LOG" || loop_fail "foreign callback was not refused" "$(cat "$FAKE_BROWSER_LOG")"
grep -Fq 'Ignored a browser callback that does not belong to this login.' <<<"$mismatch_output" || loop_fail "foreign callback was not reported" "$mismatch_output"
if grep -Fq attacker-code "$FAKE_TOKEN_BODIES"; then loop_fail "exchanged a code from a foreign callback"; fi
[ "$(authorize_param "?$(tail -n 1 "$FAKE_TOKEN_BODIES")" code)" = "authorization-code" ] || loop_fail "did not exchange the real code"

# An issuer error fails the login without a token exchange.
reset_loop_logs
if denied_output="$(env "${loop_env[@]}" FAKE_BROWSER=error bash "$installer" login codex --base-url https://router.example.test --non-interactive --quiet 2>&1)"; then
  loop_fail "denied browser sign-in succeeded" "$denied_output"
fi
grep -Fq 'did not complete (access_denied)' <<<"$denied_output" || loop_fail "issuer error was not reported" "$denied_output"
[ ! -s "$FAKE_TOKEN_BODIES" ] || loop_fail "denied sign-in still exchanged a code"
assert_listener_gone "$denied_output"

# Nobody comes back: time out, free the port and point at the manual flow.
reset_loop_logs
enrolled_before="$(wc -l <"$FAKE_ENROLLMENTS")"
if timeout_output="$(env "${loop_env[@]}" FAKE_BROWSER=ignore WEAVE_OAUTH_TIMEOUT=2 bash "$installer" login codex --base-url https://router.example.test --non-interactive --quiet 2>&1)"; then
  loop_fail "browser login without a callback succeeded" "$timeout_output"
fi
grep -Fq 'Timed out waiting for the browser sign-in.' <<<"$timeout_output" || loop_fail "timeout was not reported" "$timeout_output"
grep -Fq -- '--manual to use a device code instead' <<<"$timeout_output" || loop_fail "timeout did not name the fallback" "$timeout_output"
{ [ ! -s "$FAKE_TOKEN_BODIES" ] && [ "$(wc -l <"$FAKE_ENROLLMENTS")" -eq "$enrolled_before" ]; } || loop_fail "timed-out login exchanged or enrolled"
assert_listener_gone "$timeout_output"

# Ctrl-C while waiting stops the listener and removes its temp dir.
env "${loop_env[@]}" FAKE_BROWSER=ignore python3 - "$installer" <<'CTRLC'
import os, pty, select, sys, time
pid, fd = pty.fork()
if pid == 0:
    os.execvp("bash", ["bash", sys.argv[1], "login", "codex", "--base-url", "https://router.example.test", "--quiet"])
output = bytearray()
deadline = time.monotonic() + 20
sent = False
while time.monotonic() < deadline:
    ready, _, _ = select.select([fd], [], [], 0.5)
    if ready:
        try:
            chunk = os.read(fd, 4096)
        except OSError:
            break
        if not chunk:
            break
        output.extend(chunk)
    if not sent and b"Ctrl-C to cancel" in output:
        os.write(fd, b"\x03")
        sent = True
_, status = os.waitpid(pid, 0)
os.close(fd)
assert sent, output.decode(errors="replace")
assert os.waitstatus_to_exitcode(status) != 0, "cancelled login exited 0"
assert b"cancelled" in output, output.decode(errors="replace")
CTRLC
assert_listener_gone "Ctrl-C"

# 1455 taken: use 1457, the other port the issuer accepts for this client.
reset_loop_logs
hold_port 1455
fallback_port_output="$(env "${loop_env[@]}" bash "$installer" login codex --base-url https://router.example.test --non-interactive --quiet 2>&1)" \
  || loop_fail "Codex login on the fallback port failed" "$fallback_port_output"
[ "$(authorize_param "$(grep -m1 /oauth/authorize "$FAKE_BROWSER_LOG")" redirect_uri)" = "http://127.0.0.1:1457/auth/callback" ] \
  || loop_fail "Codex did not move to port 1457" "$(cat "$FAKE_BROWSER_LOG")"

# Both registered ports taken: no other port would be accepted, so use a device code.
reset_loop_logs
hold_port 1457
ports_taken_output="$(env "${loop_env[@]}" bash "$installer" login codex --base-url https://router.example.test --non-interactive --quiet 2>&1)" \
  || loop_fail "Codex device-code fallback failed" "$ports_taken_output"
grep -Fq 'Signing in with a device code: ports 1455 and 1457' <<<"$ports_taken_output" || loop_fail "port fallback was not printed" "$ports_taken_output"
grep -Fq 'Enter code: ABCD-EFGH' <<<"$ports_taken_output" || loop_fail "port fallback did not run the device-code flow" "$ports_taken_output"
for holder in "${holder_pids[@]}"; do kill "$holder" 2>/dev/null || true; wait "$holder" 2>/dev/null || true; done
holder_pids=()

# SSH (even with a display forwarded): the browser can't reach this machine's localhost.
for ssh_var in 'SSH_CONNECTION=203.0.113.1 50000 192.0.2.1 22' 'SSH_TTY=/dev/pts/3'; do
  reset_loop_logs
  ssh_output="$(env "${loop_env[@]}" "$ssh_var" bash "$installer" login codex --base-url https://router.example.test --non-interactive --quiet 2>&1)" \
    || loop_fail "Codex login over SSH failed" "$ssh_output"
  grep -Fq 'Signing in with a device code: this is an SSH session' <<<"$ssh_output" || loop_fail "SSH fallback was not printed" "$ssh_output"
  grep -Fq deviceauth/usercode "$FAKE_CURL_LOG" || loop_fail "SSH Codex login did not use a device code"
  if grep -Fq redirect_uri "$FAKE_BROWSER_LOG"; then loop_fail "SSH Codex login opened a localhost callback"; fi
done
reset_loop_logs
if ssh_claude_output="$(env "${loop_env[@]}" SSH_TTY=/dev/pts/3 bash "$installer" login claude --base-url https://router.example.test --non-interactive --quiet 2>&1)"; then
  loop_fail "non-interactive Claude paste flow succeeded" "$ssh_claude_output"
fi
grep -Fq 'Signing in by pasting a code: this is an SSH session' <<<"$ssh_claude_output" || loop_fail "Claude SSH fallback was not printed" "$ssh_claude_output"
grep -Fq 'redirect_uri=https%3A%2F%2Fconsole.anthropic.com%2Foauth%2Fcode%2Fcallback' <<<"$ssh_claude_output" \
  || loop_fail "Claude SSH fallback lost the manual redirect" "$ssh_claude_output"
grep -Fq 'requires an interactive terminal to paste' <<<"$ssh_claude_output" || loop_fail "Claude SSH fallback did not ask for a paste" "$ssh_claude_output"

# No display on Linux, and the explicit flags, also use the manual flows.
if [ "$(uname -s)" = Linux ]; then
  reset_loop_logs
  headless_output="$(env "${common_env[@]}" bash "$installer" login codex --base-url https://router.example.test --non-interactive --quiet 2>&1)" \
    || loop_fail "headless Codex login failed" "$headless_output"
  grep -Fq 'Signing in with a device code: no graphical display is available' <<<"$headless_output" || loop_fail "headless fallback was not printed" "$headless_output"
fi
for flag in --manual --device-code; do
  reset_loop_logs
  flag_output="$(env "${loop_env[@]}" bash "$installer" login codex "$flag" --base-url https://router.example.test --non-interactive --quiet 2>&1)" \
    || loop_fail "Codex login $flag failed" "$flag_output"
  grep -Fq 'Signing in with a device code: you asked for it with --manual or --device-code' <<<"$flag_output" || loop_fail "$flag did not force the device code" "$flag_output"
  if grep -Fq redirect_uri "$FAKE_BROWSER_LOG"; then loop_fail "$flag still opened a localhost callback"; fi
done
if flag_misuse="$(env "${common_env[@]}" bash "$installer" status --manual --quiet 2>&1)"; then
  loop_fail "--manual outside login was accepted" "$flag_misuse"
fi
grep -Fq "only apply to 'login'" <<<"$flag_misuse" || loop_fail "--manual misuse was not explained" "$flag_misuse"

echo "Subscription CLI regression tests passed"
