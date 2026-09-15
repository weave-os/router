#!/usr/bin/env bash
#
# Endpoint smoke test for the `install.sh --opencode` target.
#
# OpenCode sends Responses requests through @ai-sdk/openai, which appends
# /responses to the configured /v1 base URL. This guard catches either a stale
# Anthropic provider or a doubled/missing /v1 before release.
#
# Requires: opencode, jq, python3, curl. Run from anywhere:
#   install/pi-router/test/opencode_smoke.sh
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALL_SH="$(cd "$SCRIPT_DIR/../.." && pwd)/install.sh"
MOCK="$SCRIPT_DIR/mock_router.py"

PORT="${MOCK_PORT:-8911}"
BASE_URL="http://127.0.0.1:$PORT"

for tool in opencode jq python3 curl; do
  command -v "$tool" >/dev/null 2>&1 || { echo "FATAL: '$tool' not on PATH"; exit 2; }
done
[ -f "$INSTALL_SH" ] || { echo "FATAL: installer not found: $INSTALL_SH"; exit 2; }
[ -f "$MOCK" ]       || { echo "FATAL: mock not found: $MOCK"; exit 2; }

WORK="$(mktemp -d)"
LOG="$WORK/requests.jsonl"
MOCK_PID=""
KEEP_WORK=0
cleanup() {
  if [ -n "$MOCK_PID" ]; then
    kill "$MOCK_PID" 2>/dev/null || true
    wait "$MOCK_PID" 2>/dev/null || true
  fi
  if [ "$KEEP_WORK" = "1" ]; then
    echo "diagnostics preserved in $WORK"
  else
    rm -rf "$WORK"
  fi
}
trap cleanup EXIT

export WEAVE_ROUTER_KEY="rk_oc_smoke_key"
export WEAVE_USER_EMAIL="oc@workweave.ai"
export WEAVE_USER_NAME="OC Smoke"

PASS=0; FAIL=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$1"; PASS=$((PASS + 1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$1"; FAIL=$((FAIL + 1)); }
jqcount() { jq -s "[.[] | select($1)] | length" "$LOG"; }

MOCK_LOG="$LOG" MOCK_PORT="$PORT" python3 "$MOCK" &
MOCK_PID=$!
for _ in $(seq 1 50); do curl -fsS "$BASE_URL/health" >/dev/null 2>&1 && break; sleep 0.1; done
curl -fsS "$BASE_URL/health" >/dev/null 2>&1 || { echo "FATAL: mock did not come up"; exit 2; }

printf '\033[1m== install (install.sh --opencode --dir) ==\033[0m\n'
bash "$INSTALL_SH" --opencode --base-url "$BASE_URL" --dir "$WORK" >"$WORK/install.out" 2>&1 </dev/null || true
if jq -e --arg u "$BASE_URL/v1" '.provider.weave.options.baseURL == $u' "$WORK/opencode.json" >/dev/null 2>&1; then
  ok "opencode.json baseURL = $BASE_URL/v1 (Vercel SDK convention — keeps /v1)"
else
  bad "opencode.json baseURL wrong (see $WORK/install.out)"
fi
if jq -e '.provider.weave.npm == "@ai-sdk/openai"' "$WORK/opencode.json" >/dev/null 2>&1; then
  ok "opencode provider uses @ai-sdk/openai"
else
  bad "opencode provider npm wrong"
fi

printf '\033[1m== run (opencode run, headless, isolated XDG) ==\033[0m\n'
mkdir -p "$WORK/xdg"
(
  cd "$WORK" || exit 1
  XDG_CONFIG_HOME="$WORK/xdg" opencode run "say hi in three words" >"$WORK/oc.out" 2>&1 </dev/null
) &
RPID=$!
( sleep 150; kill "$RPID" 2>/dev/null ) & WD=$!
disown "$WD" 2>/dev/null || true
wait "$RPID" 2>/dev/null || true
kill "$WD" 2>/dev/null || true

if [ "$(jqcount '.method=="POST" and .app=="opencode" and .path=="/v1/responses" and .rejected==false')" -ge 1 ]; then
  ok "opencode hit /v1/responses (served, app=opencode)"
else
  bad "opencode did not reach /v1/responses (see $WORK/oc.out)"
fi
WRONG="$(jqcount '.method=="POST" and .rejected==true')"
if [ "$WRONG" -eq 0 ]; then
  ok "every opencode POST hit /v1/responses"
else
  bad "$WRONG opencode POST(s) hit a wrong path -> would 404 on the real router"
fi

printf '\033[1m== Result ==\033[0m\n'
printf '\033[1m%s passed, %s failed\033[0m\n' "$PASS" "$FAIL"
if [ "$FAIL" -ne 0 ]; then KEEP_WORK=1; echo "FAILED"; exit 1; fi
echo "ALL GREEN"
