#!/usr/bin/env bash

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
installer="${INSTALLER:-$script_dir/../install.sh}"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/bin" "$work/home"
export FAKE_CURL_LOG="$work/curl.log"
export FAKE_ENROLLMENTS="$work/enrollments.jsonl"

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
    printf '%s' "$FAKE_CLAUDE_RESPONSE"
    ;;
  */oauth/token)
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
printf '#!/usr/bin/env bash\nexit 0\n' >"$work/bin/open"
chmod +x "$work/bin/open"

common_env=(HOME="$work/home" PATH="$work/bin:$PATH" WEAVE_ROUTER_KEY="rk_test_secret" NO_COLOR=1)

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
    bash "$installer" login codex --base-url https://router.example.test --non-interactive --quiet 2>&1)"; then
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

env "${common_env[@]}" bash "$installer" login codex --base-url https://router.example.test --non-interactive --quiet \
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
    os.execvpe("bash", ["bash", installer, "login", "codex", "--base-url", "https://router.example.test", "--quiet"], os.environ)

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
grep -Fq 'Identity: rk_…cret' <<<"$status_output"
grep -Fq 'Connectivity: connected' <<<"$status_output"
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

echo "Subscription CLI regression tests passed"
