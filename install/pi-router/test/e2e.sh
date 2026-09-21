#!/usr/bin/env bash
#
# End-to-end test for @workweave/router + the `install.sh --pi` target.
#
# Drives a REAL `pi` process against a mock Weave Router (mock_router.py) and
# asserts the routed-request shape the router actually receives: routing knobs,
# identity headers, and the metadata.user_id session/subagent signal -- for the
# main loop, for dispatch subagents, and for on-disk (key-file + models.json)
# resolution. No real model spend; no network beyond localhost.
#
# Requires: pi 0.83+, jq, python3, curl. Run from anywhere:
#   install/pi-router/test/e2e.sh
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PKG_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
INSTALL_SH="$(cd "$PKG_DIR/.." && pwd)/install.sh"
EXT="$PKG_DIR/src/index.ts"
UNIT_SUITE="$SCRIPT_DIR/unit-suite.ts"
MOCK="$SCRIPT_DIR/mock_router.py"

PORT="${MOCK_PORT:-8899}"
BASE_URL="http://127.0.0.1:$PORT"

for tool in pi jq python3 curl; do
  command -v "$tool" >/dev/null 2>&1 || { echo "FATAL: '$tool' not on PATH"; exit 2; }
done
[ -f "$EXT" ]        || { echo "FATAL: extension not found: $EXT"; exit 2; }
[ -f "$UNIT_SUITE" ]  || { echo "FATAL: unit suite not found: $UNIT_SUITE"; exit 2; }
[ -f "$INSTALL_SH" ] || { echo "FATAL: installer not found: $INSTALL_SH"; exit 2; }
[ -f "$MOCK" ]       || { echo "FATAL: mock not found: $MOCK"; exit 2; }

WORK="$(mktemp -d)"
LOG="$WORK/requests.jsonl"
PI_DIR="$WORK/.pi"
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

# Deterministic identity + key. The mock accepts any key value.
export WEAVE_ROUTER_KEY="rk_e2e_testkey_abcd"
export WEAVE_USER_EMAIL="e2e@workweave.ai"
export WEAVE_USER_NAME="E2E Tester"

PASS=0
FAIL=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$1"; PASS=$((PASS + 1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$1"; FAIL=$((FAIL + 1)); }
phase() { printf '\n\033[1m== %s ==\033[0m\n' "$1"; }

# jqcount <filter> -> number of JSONL records matching the boolean jq filter
jqcount() { jq -s "[.[] | select($1)] | length" "$LOG"; }

# with_timeout <secs> <cmd...>  (macOS has no coreutils `timeout`)
with_timeout() {
  local secs="$1"; shift
  "$@" &
  local pid=$!
  ( sleep "$secs"; kill "$pid" 2>/dev/null ) &
  local wd=$!
  disown "$wd" 2>/dev/null || true  # suppress the job-control "Terminated" line
  local rc=0
  wait "$pid" 2>/dev/null || rc=$?
  kill "$wd" 2>/dev/null || true
  return "$rc"
}

# -------------------------------------------------------------------------
# Start the mock router and wait for it to accept connections.
# -------------------------------------------------------------------------
MOCK_LOG="$LOG" MOCK_PORT="$PORT" python3 "$MOCK" &
MOCK_PID=$!
for _ in $(seq 1 50); do
  curl -fsS "$BASE_URL/health" >/dev/null 2>&1 && break
  sleep 0.1
done
curl -fsS "$BASE_URL/health" >/dev/null 2>&1 || { echo "FATAL: mock did not come up on $BASE_URL"; exit 2; }

# -------------------------------------------------------------------------
phase "Phase 1 — installer (install.sh --pi)"
# -------------------------------------------------------------------------
bash "$INSTALL_SH" --pi --base-url "$BASE_URL" --dir "$WORK" >"$WORK/install.out" 2>&1 </dev/null || true

if [ -f "$PI_DIR/models.json" ]; then
  ok "models.json written"
else
  bad "models.json missing (see $WORK/install.out)"
fi
if [ -f "$PI_DIR/settings.json" ]; then
  ok "settings.json written"
else
  bad "settings.json missing"
fi
if [ -f "$PI_DIR/.weave_router_key" ]; then
  ok "router key file written"
else
  bad "key file missing"
fi

if jq -e --arg u "$BASE_URL" '.providers.weave.baseUrl == $u' "$PI_DIR/models.json" >/dev/null 2>&1; then
  ok "models.json baseUrl = $BASE_URL (root, no /v1)"
else
  bad "models.json baseUrl wrong (want root, no /v1)"
fi
if jq -e '.providers.weave.api == "anthropic-messages" and .providers.weave.authHeader == false' "$PI_DIR/models.json" >/dev/null 2>&1; then
  ok "provider api=anthropic-messages, authHeader=false"
else
  bad "provider api/authHeader wrong"
fi
if jq -e '.providers.weave.headers["x-weave-routing-alpha"] == "0.8" and .providers.weave.headers["x-weave-routing-speed-weight"] == "0.05"' "$PI_DIR/models.json" >/dev/null 2>&1; then
  ok "models.json carries main-loop knobs (0.8 / 0.05)"
else
  bad "models.json knobs wrong"
fi
if jq -e '.providers.weave.headers["X-Weave-User-Email"] == "e2e@workweave.ai"' "$PI_DIR/models.json" >/dev/null 2>&1; then
  ok "identity baked into models.json headers"
else
  bad "identity header missing in models.json"
fi

PERM="$(stat -f '%Lp' "$PI_DIR/.weave_router_key" 2>/dev/null || stat -c '%a' "$PI_DIR/.weave_router_key" 2>/dev/null || echo '?')"
if [ "$PERM" = "600" ]; then
  ok "key file mode 600"
else
  bad "key file mode $PERM (want 600)"
fi

if grep -q '"path":"/health"'   "$LOG"; then
  ok "installer pinged /health"
else
  bad "no /health probe reached mock"
fi
if grep -q '"path":"/validate"' "$LOG"; then
  ok "installer validated key (/validate)"
else
  bad "no /validate probe reached mock"
fi

# Idempotency + legacy migration: seed an old @workweave/pi-router entry (the
# pre-fold id), re-install, and confirm the new id stays single and the legacy
# id is dropped.
tmp="$(jq '.packages += ["npm:@workweave/pi-router"]' "$PI_DIR/settings.json")"; printf '%s\n' "$tmp" >"$PI_DIR/settings.json"
bash "$INSTALL_SH" --pi --base-url "$BASE_URL" --dir "$WORK" >>"$WORK/install.out" 2>&1 </dev/null || true
PACKAGE_SOURCE="npm:${WEAVE_ROUTER_NPM_PACKAGE:-@weave-os/router}"
PKGCOUNT="$(jq --arg source "$PACKAGE_SOURCE" '[.packages[]? | select(. == $source)] | length' "$PI_DIR/settings.json")"
if [ "$PKGCOUNT" = "1" ]; then
  ok "idempotent re-install: single $PACKAGE_SOURCE package entry"
else
  bad "package entry count = $PKGCOUNT (want 1)"
fi
if [ "$(jq '[.packages[]? | select(. == "npm:@workweave/pi-router")] | length' "$PI_DIR/settings.json")" = "0" ]; then
  ok "legacy npm:@workweave/pi-router entry migrated away"
else
  bad "legacy pi-router entry not removed"
fi

# The npm package is not published pre-merge; drop it so `-e` is the sole loader.
STRIPPED="$(jq 'del(.packages)' "$PI_DIR/settings.json")"
printf '%s\n' "$STRIPPED" >"$PI_DIR/settings.json"

# -------------------------------------------------------------------------
phase "Phase 2 — generated pricing + savings contract"
# Loading the focused node:test suite through pi exercises the same TypeScript
# loader and module resolution that the published extension uses. The wrapper
# deliberately is not named *.test.ts because Pi 0.74 excludes those paths.
if with_timeout 30 env PI_CODING_AGENT_DIR="$PI_DIR" \
  pi -e "$UNIT_SUITE" --no-session --offline --model weave/claude-sonnet-4-6 \
  -p "Run the unit suite." >"$WORK/unit.out" 2>&1 </dev/null; then
  if [ "$(grep -Ec '^(✔ |ok [0-9]+ - )' "$WORK/unit.out" || true)" = "116" ]; then
    ok "pricing, beta, force-model, UI, compaction, served-window, and LSP unit suite passed"
  else
    bad "unit suite did not report all 116 passes (see $WORK/unit.out)"
  fi
else
  bad "unit suite failed to load through pi (see $WORK/unit.out)"
fi

# -------------------------------------------------------------------------
phase "Phase 3 — main-loop routing (real pi -p)"
# -------------------------------------------------------------------------
with_timeout 90 env PI_CODING_AGENT_DIR="$PI_DIR" \
  pi -e "$EXT" --no-session --offline --model weave/claude-sonnet-4-6 \
  -p "Say hello in three words." >"$WORK/main.out" 2>&1 </dev/null || true

if [ "$(jqcount '.method=="POST" and .app=="pi" and .path=="/v1/messages" and .rejected==false')" -ge 1 ]; then
  ok "pi sent a routed request to /v1/messages"
else
  bad "no valid main-loop request reached /v1/messages (see $WORK/main.out)"
fi
if [ "$(jqcount '.app=="pi" and .knobs["x-weave-routing-alpha"]=="0.8" and .knobs["x-weave-routing-speed-weight"]=="0.05" and .knobs["x-weave-routing-output-cost-ratio"]=="0.5" and .knobs["x-weave-routing-expected-output-tokens"]=="3000"')" -ge 1 ]; then
  ok "main loop: quality knobs 0.8 / 0.05 / 0.5 / 3000"
else
  bad "main-loop knobs wrong"
fi
if [ "$(jqcount '.app=="pi" and (.user_id // "" | startswith("pi:")) and (.user_id // "" | length) > 3')" -ge 1 ]; then
  ok "main loop: metadata.user_id = pi:<session>"
else
  bad "main-loop user_id wrong"
fi
if [ "$(jqcount '.app=="pi" and .key_present==true')" -ge 1 ]; then
  ok "main loop: X-Weave-Router-Key forwarded"
else
  bad "router key not forwarded"
fi
if [ "$(jqcount '.app=="pi" and .email=="e2e@workweave.ai"')" -ge 1 ]; then
  ok "main loop: identity email forwarded"
else
  bad "identity email not forwarded"
fi
if [ "$(jqcount '.app=="pi" and .marker_opt=="off"')" -ge 1 ]; then
  ok "main loop: opted out of in-band routing marker (X-Weave-Routing-Marker: off)"
else
  bad "routing-marker opt-out header not sent"
fi
if grep -q "weave-routed-model: claude-opus-4-8" "$WORK/main.out"; then
  ok "x-router-model surfaced (headless stderr marker)"
else
  bad "routed-model marker absent (see $WORK/main.out)"
fi

# -------------------------------------------------------------------------
phase "Phase 4 — dispatch fan-out (real subagent processes)"
# -------------------------------------------------------------------------
# WEAVE_ROUTING_* are set on the PARENT on purpose: dispatch must NOT leak them
# into children, so the subagent-knob assertion below (still 0.25/0.45) doubles
# as the regression test for that env-isolation fix.
with_timeout 120 env PI_CODING_AGENT_DIR="$PI_DIR" \
  WEAVE_ROUTING_ALPHA=0.99 WEAVE_ROUTING_SPEED_WEIGHT=0.02 \
  pi -e "$EXT" --no-session --offline --model weave/claude-sonnet-4-6 \
  -p "__DISPATCH__ run two quick parallel checks." >"$WORK/dispatch.out" 2>&1 </dev/null || true

if [ "$(jqcount '.app=="pi" and .served=="tool_use"')" -ge 1 ]; then
  ok "main loop invoked the dispatch tool"
else
  bad "dispatch tool_use was never served (see $WORK/dispatch.out)"
fi
SUBAGENT_REQS="$(jqcount '.app=="pi-subagent"')"
if [ "$SUBAGENT_REQS" -ge 2 ]; then
  ok "spawned $SUBAGENT_REQS subagent requests (>=2 expected)"
else
  bad "only $SUBAGENT_REQS subagent requests (want >=2)"
fi
if [ "$(jqcount '.app=="pi-subagent" and .knobs["x-weave-routing-alpha"]=="0.25" and .knobs["x-weave-routing-speed-weight"]=="0.45" and .knobs["x-weave-routing-output-cost-ratio"]=="2" and .knobs["x-weave-routing-expected-output-tokens"]=="1500"')" -ge 2 ]; then
  ok "subagents: speed/cheap knobs 0.25 / 0.45 / 2 / 1500 (parent WEAVE_ROUTING_* not inherited)"
else
  bad "subagent knobs wrong (did parent WEAVE_ROUTING_* leak into children?)"
fi
if [ "$(jqcount '.app=="pi-subagent" and (.user_id // "" | startswith("subagent:"))')" -ge 2 ]; then
  ok "subagents: metadata.user_id = subagent:<uuid>"
else
  bad "subagent user_id wrong"
fi
if [ "$(jqcount '.app=="pi-subagent" and .marker_opt=="off"')" -ge 2 ]; then
  ok "subagents: opted out of in-band routing marker (else finalText = the badge)"
else
  bad "subagent routing-marker opt-out not sent"
fi
UNIQUE_SUBAGENT_IDS="$(jq -s '[.[] | select(.app=="pi-subagent") | .user_id] | unique | length' "$LOG")"
if [ "$UNIQUE_SUBAGENT_IDS" -ge 2 ]; then
  ok "each subagent got a distinct session id ($UNIQUE_SUBAGENT_IDS unique)"
else
  bad "subagent ids not distinct ($UNIQUE_SUBAGENT_IDS)"
fi
if [ "$(jqcount '.app=="pi" and .has_tool_result==true')" -ge 1 ]; then
  ok "main loop resumed after tool_result (loop terminated cleanly)"
else
  bad "no post-dispatch main turn (loop did not complete)"
fi

# -------------------------------------------------------------------------
phase "Phase 5 — on-disk resolution (no env; key file + models.json)"
# -------------------------------------------------------------------------
# Unset every WEAVE_* override so the extension MUST resolve the key from the
# installer-written key file and the base URL from models.json (the bug fix).
with_timeout 90 env -u WEAVE_ROUTER_KEY -u WEAVE_ROUTER_URL -u WEAVE_USER_EMAIL -u WEAVE_USER_NAME \
  PI_CODING_AGENT_DIR="$PI_DIR" \
  pi -e "$EXT" --no-session --offline --model weave/claude-sonnet-4-6 \
  -p "Resolve credentials from disk." >"$WORK/resolve.out" 2>&1 </dev/null || true

# Requests carrying our test key's suffix prove key-file + models.json resolution
# worked with no env vars set (the request reached the mock at all == baseUrl ok).
if [ "$(jqcount '.app=="pi" and .key_present==true and .key_suffix=="abcd"')" -ge 1 ]; then
  ok "resolved key from key file + baseUrl from models.json (no env)"
else
  bad "on-disk resolution failed (see $WORK/resolve.out)"
fi

# -------------------------------------------------------------------------
phase "Phase 6 — explicit classifier admission and independent children (Pi 0.83+)"
MESSAGES_BEFORE="$(jqcount '.path=="/v1/messages"')"
HANDOFFS_BEFORE="$(jqcount '.path=="/v1/route/handoff"')"
with_timeout 60 env PI_CODING_AGENT_DIR="$PI_DIR" WEAVE_PI_LLM_CLASSIFIER=1 \
  pi -e "$EXT" --no-session --offline --model weave/claude-sonnet-4-6 \
  -p "__DISPATCH__ run two classifier checks." >"$WORK/classifier.out" 2>&1 </dev/null || true

if [ "$(jqcount '.path=="/v1/router/threads" and .classifier_denied==false')" = "3" ] && \
   [ "$(jq -s '[.[] | select(.classifier_thread != null) | .classifier_thread] | unique | length' "$LOG")" = "3" ]; then
  ok "classifier parent and child processes enrolled three distinct threads"
else
  bad "classifier thread enrollment/isolation failed (see $WORK/classifier.out)"
fi
if [ "$(jqcount '.classifier_thread != null')" = "4" ] && \
   [ "$(jqcount '.path=="/v1/messages"')" = "$((MESSAGES_BEFORE + 4))" ] && \
   [ "$(jqcount '.path=="/v1/route/handoff"')" = "$HANDOFFS_BEFORE" ]; then
  ok "every classifier inference carried a ticket; tool loop reused it without handoff"
else
  bad "classifier request lost its ticket or used a legacy handoff"
fi

MESSAGES_BEFORE="$(jqcount '.path=="/v1/messages"')"
with_timeout 30 env PI_CODING_AGENT_DIR="$PI_DIR" WEAVE_PI_LLM_CLASSIFIER=1 \
  WEAVE_ROUTER_KEY=rk_e2e_classifier_denied \
  pi -e "$EXT" --no-session --offline --model weave/claude-sonnet-4-6 \
  -p "Enrollment must fail closed." >"$WORK/classifier-denied.out" 2>&1 </dev/null || true
if [ "$(jqcount '.classifier_denied==true')" -ge 1 ] && \
   [ "$(jqcount '.path=="/v1/messages"')" = "$MESSAGES_BEFORE" ]; then
  ok "failed classifier enrollment aborted the real Pi provider request"
else
  bad "failed classifier enrollment sent an unticketed request"
fi

phase "Result"
# -------------------------------------------------------------------------
# Endpoint correctness across every phase: the Anthropic SDK appends
# /v1/messages to baseUrl, so a baseUrl ending in /v1 yields /v1/v1/messages and
# 404s on the real router. Any rejected POST means a wrong baseUrl shipped.
WRONGPATH="$(jqcount '.method=="POST" and .rejected==true')"
if [ "$WRONGPATH" -eq 0 ]; then
  ok "all routed POSTs hit valid message or handoff paths (no /v1 doubling)"
else
  bad "$WRONGPATH POST(s) hit an unsupported path -> would 404 on the real router"
fi

printf 'requests logged: %s\n' "$(wc -l <"$LOG" | tr -d ' ')"
printf '\033[1m%s passed, %s failed\033[0m\n' "$PASS" "$FAIL"
if [ "$FAIL" -ne 0 ]; then KEEP_WORK=1; echo "FAILED"; exit 1; fi
echo "ALL GREEN"
