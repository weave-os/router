#!/usr/bin/env bash

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
installer="${INSTALLER:-$script_dir/../install.sh}"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/bin" "$work/home"
export FAKE_CURL_LOG="$work/curl.log"

payload="$(printf '%s' '{"chatgpt_account_id":"chatgpt-test"}' | openssl base64 -A | tr '+/' '-_' | tr -d '=')"
export FAKE_JWT="header.$payload.signature"

cat >"$work/bin/curl" <<'FAKE_CURL'
#!/usr/bin/env bash
set -euo pipefail
out=""
data_file=""
url=""
want_status="false"
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    -w) want_status="true"; shift 2 ;;
    --data-binary)
      data_file="${2#@}"
      case "$2" in *refresh-new*|*authorization-code*|*pkce-verifier*) exit 91 ;; esac
      shift 2
      ;;
    -H|--header|-X|--max-time) shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
printf '%s\t%s\n' "$url" "$data_file" >>"$FAKE_CURL_LOG"
case "$url" in
  */api/accounts/deviceauth/usercode)
    printf '%s' '{"device_auth_id":"device-1","user_code":"ABCD-EFGH","interval":"1"}'
    ;;
  */api/accounts/deviceauth/token)
    printf '%s' '{"authorization_code":"authorization-code","code_verifier":"pkce-verifier"}'
    ;;
  */oauth/token)
    printf '{"id_token":"%s","access_token":"access-new","refresh_token":"refresh-new","expires_in":3600}' "$FAKE_JWT"
    ;;
  */validate)
    printf '%s' '{}' >"$out"
    [ "$want_status" = "true" ] && printf '200'
    ;;
  */v1/subscriptions/accounts)
    if [ -n "$data_file" ]; then
      grep -Fq '"refresh_token":"refresh-new"' "$data_file"
      printf '%s' '{"id":"opaque-1","provider":"codex","external_account_id":"chatgpt-test","enabled":true}' >"$out"
      [ "$want_status" = "true" ] && printf '201'
    else
      printf '%s' '[{"id":"opaque-1","provider":"codex","external_account_id":"chatgpt-test","enabled":true}]' >"$out"
      [ "$want_status" = "true" ] && printf '200'
    fi
    ;;
  *) exit 22 ;;
esac
FAKE_CURL
chmod +x "$work/bin/curl"

common_env=(HOME="$work/home" PATH="$work/bin:$PATH" WEAVE_ROUTER_KEY="rk_test_secret" NO_COLOR=1)
env "${common_env[@]}" bash "$installer" login codex --base-url https://router.example.test --non-interactive --quiet \
  | grep -Fq 'Codex subscription enrolled.'

status_output="$(env "${common_env[@]}" bash "$installer" status --base-url https://router.example.test --quiet)"
grep -Fq 'Identity: rk_…cret' <<<"$status_output"
grep -Fq 'Connectivity: connected' <<<"$status_output"
grep -Fq 'codex  chatgpt-test  enabled  ready' <<<"$status_output"
if grep -Fq 'refresh-new' "$FAKE_CURL_LOG"; then
  echo 'refresh token leaked into curl argv log' >&2
  exit 1
fi

echo "Subscription CLI regression tests passed"
