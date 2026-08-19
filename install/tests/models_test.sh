#!/usr/bin/env bash
#
# Regression tests for `npx @workweave/router models` — the model-selection CLI
# behind the /models slash command.
#
# Fully offline: an isolated $HOME keeps real config untouched, and a fake curl
# on PATH answers the router's /admin/v1 endpoints from canned JSON while
# logging every request so the assertions can check method, path, and body.
# Run directly or via `make test-install`.

set -uo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
installer="${INSTALLER:-$script_dir/../install.sh}"
[ -f "$installer" ] || { echo "cannot find installer at $installer" >&2; exit 1; }

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

fake_bin="$work/bin"
mkdir -p "$fake_bin"

# Fake curl. Understands the flags install.sh actually passes (-X, --data-binary,
# -o, -w, --header @-) and routes on the request path. $ROUTER_MODE decides which
# router it impersonates:
#   full    — serves the model-selection API
#   managed — 404s /admin/v1/* (the Weave-hosted router) but serves the catalog
#   down    — connection failure
# Requests are appended to $REQUEST_LOG as "METHOD PATH BODY".
cat >"$fake_bin/curl" <<'FAKE_CURL'
#!/usr/bin/env bash
method="GET"; out=""; url=""; body=""
while [ $# -gt 0 ]; do
  case "$1" in
    -X) method="$2"; shift 2 ;;
    -o) out="$2"; shift 2 ;;
    --data-binary|-d) body="$2"; shift 2 ;;
    -w|-H|--max-time) shift 2 ;;
    --header) shift 2 ;;
    -sS|-fsS|-s|-S|-f) shift ;;
    http*) url="$1"; shift ;;
    *) shift ;;
  esac
done
cat >/dev/null 2>&1 || true   # drain the key header on stdin
path="${url#http://}"; path="${path#https://}"; path="/${path#*/}"
[ -n "${REQUEST_LOG:-}" ] && printf '%s %s %s\n' "$method" "$path" "$body" >>"$REQUEST_LOG"

emit() { # emit <status> <json>
  [ -n "$out" ] && printf '%s' "$2" >"$out"
  printf '%s' "$1"
  exit 0
}

if [ "${ROUTER_MODE:-full}" = "down" ]; then
  printf '000'
  exit 7
fi

catalog='{"models":[{"model":"claude-opus-5","provider":"anthropic"},{"model":"gpt-5.6","provider":"openai"}]}'
models='[{"model":"claude-opus-5","provider":"anthropic","enabled":true},{"model":"claude-haiku-4-5","provider":"anthropic","enabled":false},{"model":"gpt-5.6","provider":"openai","enabled":true}]'
providers='[{"provider":"anthropic","enabled":true},{"provider":"openai","enabled":false}]'

case "$path" in
  /v1/router/models) emit 200 "$catalog" ;;
esac

if [ "${ROUTER_MODE:-full}" = "managed" ]; then
  emit 404 '{"error":"404 page not found"}'
fi

case "$path" in
  /admin/v1/models)                  emit 200 "$models" ;;
  /admin/v1/providers)               emit 200 "$providers" ;;
  /admin/v1/preferred-models)        emit 200 '{"preferred":["claude-opus-5"]}' ;;
esac

# The router validates ids against the deployed set, so a typo comes back 400.
case "$body" in
  *no-such-model*) emit 400 '{"error":"unknown model: \"no-such-model\""}' ;;
esac

case "$path" in
  /admin/v1/excluded-models*)        emit 200 '{"available":[],"excluded":["gpt-5.6"],"env_override_active":false}' ;;
  /admin/v1/excluded-providers*)     emit 200 '{"available":[],"excluded":["openai"],"env_override_active":false}' ;;
  *)                                 emit 404 '{"error":"404 page not found"}' ;;
esac
FAKE_CURL
chmod +x "$fake_bin/curl"
test_path="$fake_bin:$PATH"

pass=0
fail=0
ok() { echo "  ok   $1"; pass=$((pass + 1)); }
no() { echo "  FAIL $1"; echo "         expected: $2"; echo "         actual:   $3"; fail=$((fail + 1)); }

check() { # check <name> <actual> <expected>
  if [ "$2" = "$3" ]; then ok "$1"; else no "$1" "$3" "$2"; fi
}

contains() { # contains <name> <haystack> <needle>
  case "$2" in
    *"$3"*) ok "$1" ;;
    *)      no "$1" "output containing: $3" "$2" ;;
  esac
}

# seed_install writes the settings.json an install would leave behind, without
# running the installer (these tests are about `models`, not about install).
seed_install() { # seed_install <home> <base-url> <key>
  mkdir -p "$1/.claude"
  cat >"$1/.claude/settings.json" <<EOF
{"env":{"ANTHROPIC_BASE_URL":"$2","ANTHROPIC_CUSTOM_HEADERS":"X-Weave-Router-Key: $3"}}
EOF
}

# run_models <home> -- <installer args...>. Sets $out (stdout+stderr) and $rc.
# Deliberately not a command substitution at the call site: that would run the
# function in a subshell and throw the exit status away.
out=""
rc=0
run_models() {
  local home="$1"; shift
  [ "${1:-}" = "--" ] && shift
  HOME="$home" XDG_CACHE_HOME="$home/.cache" PATH="$test_path" NO_COLOR=1 \
    ROUTER_MODE="${ROUTER_MODE:-full}" REQUEST_LOG="${REQUEST_LOG:-}" \
    bash "$installer" models "$@" </dev/null >"$work/out" 2>&1
  rc=$?
  out="$(cat "$work/out")"
}

# run_models_stdout is the same, but keeps stderr out of $out — the shape a
# `--json` consumer sees when it pipes the command into jq.
run_models_stdout() {
  local home="$1"; shift
  [ "${1:-}" = "--" ] && shift
  HOME="$home" XDG_CACHE_HOME="$home/.cache" PATH="$test_path" NO_COLOR=1 \
    ROUTER_MODE="${ROUTER_MODE:-full}" REQUEST_LOG="${REQUEST_LOG:-}" \
    bash "$installer" models "$@" </dev/null >"$work/out" 2>"$work/err"
  rc=$?
  out="$(cat "$work/out")"
  errout="$(cat "$work/err")"
}

echo "install.sh models"

# ---------- listing ----------

home="$work/list"; mkdir -p "$home"
seed_install "$home" "http://127.0.0.1:8080" "rk_list"
export REQUEST_LOG="$work/list.log"
: >"$REQUEST_LOG"

run_models "$home" -- --claude
check "list exits 0" "$rc" "0"
contains "list marks an enabled model" "$out" "[x] claude-opus-5"
contains "list marks a disabled model" "$out" "[ ] claude-haiku-4-5"
contains "list groups by provider" "$out" "openai"
contains "list reports the enabled count" "$out" "2 of 3 enabled"
contains "list shows the preferred ranking" "$out" "Preferred order: claude-opus-5"
check "list reads the installed endpoint, not the hosted default" \
  "$(grep -c '^GET /admin/v1/models ' "$REQUEST_LOG")" "1"

run_models_stdout "$home" -- --claude --json
check "--json emits the raw payload" \
  "$(printf '%s' "$out" | jq -r '.[0].model')" "claude-opus-5"
# models writes no config, so the install-time "client not on PATH, we'll write
# it anyway" warnings must not fire — they would also corrupt a piped --json.
check "--json writes nothing to stderr" "$errout" ""

run_models "$home" -- providers --claude
contains "providers list marks an enabled provider" "$out" "[x] anthropic"
contains "providers list marks a disabled provider" "$out" "[ ] openai"

# ---------- editing ----------

: >"$REQUEST_LOG"
run_models "$home" -- disable gpt-5.6 --claude
check "disable exits 0" "$rc" "0"
contains "disable reports the model" "$out" "model gpt-5.6 is disabled"
check "disable adds to the exclusion list" \
  "$(grep -c '^POST /admin/v1/excluded-models {"model":"gpt-5.6"}$' "$REQUEST_LOG")" "1"

: >"$REQUEST_LOG"
run_models "$home" -- enable gpt-5.6 --claude
contains "enable reports the model" "$out" "model gpt-5.6 is enabled"
check "enable removes from the exclusion list" \
  "$(grep -c '^POST /admin/v1/excluded-models/remove {"model":"gpt-5.6"}$' "$REQUEST_LOG")" "1"

: >"$REQUEST_LOG"
run_models "$home" -- disable claude-haiku-4-5 gpt-5.6 --claude
check "disable accepts several models in one call" \
  "$(grep -c '^POST /admin/v1/excluded-models ' "$REQUEST_LOG")" "2"

: >"$REQUEST_LOG"
run_models "$home" -- providers disable openai --claude
check "providers disable hits the provider endpoint" \
  "$(grep -c '^POST /admin/v1/excluded-providers {"provider":"openai"}$' "$REQUEST_LOG")" "1"

: >"$REQUEST_LOG"
run_models "$home" -- prefer claude-opus-5 gpt-5.6 --claude
check "prefer replaces the whole ranking" \
  "$(grep -c '^PUT /admin/v1/preferred-models {"preferred":\["claude-opus-5","gpt-5.6"\]}$' "$REQUEST_LOG")" "1"

: >"$REQUEST_LOG"
run_models "$home" -- prefer clear --claude
check "prefer clear empties the ranking" \
  "$(grep -c '^PUT /admin/v1/preferred-models {"preferred":\[\]}$' "$REQUEST_LOG")" "1"

# A model id the router doesn't know comes back 400; the CLI must surface the
# router's own message rather than a bare status code, and stop.
: >"$REQUEST_LOG"
run_models "$home" -- disable no-such-model gpt-5.6 --claude
check "an unknown model id fails" "$rc" "1"
contains "an unknown model id surfaces the router's message" "$out" "unknown model"
check "an unknown model id stops before the remaining ids" \
  "$(grep -c '^POST ' "$REQUEST_LOG")" "1"

# ---------- a router without the model-selection API ----------

ROUTER_MODE="managed"
: >"$REQUEST_LOG"
run_models "$home" -- --claude
check "list against a router without the API still exits 0" "$rc" "0"
contains "list falls back to the public catalog" "$out" "claude-opus-5"
contains "list says where model selection lives" "$out" "router.workweave.ai/dashboard/settings"
# The catalog endpoint reports what is deployed, not what this installation
# enabled, so the fallback must not render on/off state it cannot know.
case "$out" in
  *"[x]"*|*"[ ]"*) no "the fallback claims no on/off state" "no checkbox markers" "$out" ;;
  *)               ok "the fallback claims no on/off state" ;;
esac

run_models "$home" -- disable gpt-5.6 --claude
check "editing against a router without the API fails" "$rc" "1"
contains "the failure names the dashboard" "$out" "router.workweave.ai/dashboard/settings"
ROUTER_MODE="full"

# ---------- unreachable router ----------

ROUTER_MODE="down"
run_models "$home" -- --claude
check "an unreachable router fails" "$rc" "1"
contains "an unreachable router says so" "$out" "Could not reach the router"
ROUTER_MODE="full"

# ---------- endpoint + key resolution ----------

selfhosted="$work/selfhosted"; mkdir -p "$selfhosted"
seed_install "$selfhosted" "https://router.internal.example" "rk_selfhosted"
export REQUEST_LOG="$work/selfhosted.log"
: >"$REQUEST_LOG"
run_models "$selfhosted" -- --claude
check "a self-hosted install talks to its own router" \
  "$(grep -c '^GET /admin/v1/models ' "$REQUEST_LOG")" "1"

# `off` parks the router URL + key in the sidecar and points settings.json at
# Anthropic. models must read the sidecar, or it would treat api.anthropic.com
# as the router.
parked="$work/parked"; mkdir -p "$parked/.claude"
cat >"$parked/.claude/settings.json" <<'EOF'
{"env":{"ANTHROPIC_BASE_URL":"https://api.anthropic.com"}}
EOF
cat >"$parked/.claude/.weave-parked.json" <<'EOF'
{"env":{"ANTHROPIC_BASE_URL":"http://127.0.0.1:8080","ANTHROPIC_CUSTOM_HEADERS":"X-Weave-Router-Key: rk_parked"}}
EOF
export REQUEST_LOG="$work/parked.log"
: >"$REQUEST_LOG"
run_models "$parked" -- --claude
check "models works while the router is toggled off" "$rc" "0"
check "models reads the parked endpoint, not api.anthropic.com" \
  "$(grep -c '^GET /admin/v1/models ' "$REQUEST_LOG")" "1"

empty="$work/empty"; mkdir -p "$empty"
run_models "$empty" -- --claude
check "no install found fails" "$rc" "1"
contains "no install found explains how to fix it" "$out" "npx @workweave/router --claude"

nokey="$work/nokey"; mkdir -p "$nokey/.claude"
cat >"$nokey/.claude/settings.json" <<'EOF'
{"env":{"ANTHROPIC_BASE_URL":"http://127.0.0.1:8080"}}
EOF
run_models "$nokey" -- --claude
check "an install with no key fails" "$rc" "1"
contains "an install with no key says so" "$out" "No router key found"

# ---------- argument handling ----------

run_models "$home" -- --codex
check "models rejects a non-Claude client" "$rc" "2"
contains "models says which client it supports" "$out" "supports --claude only"

HOME="$home" PATH="$test_path" NO_COLOR=1 bash "$installer" models </dev/null >"$work/out" 2>&1
check "models without a client flag is refused" "$?" "2"
contains "models without a client flag names --claude" "$(cat "$work/out")" "requires an explicit client"

run_models "$home" -- enable --claude
check "enable with no model id is refused" "$rc" "2"
contains "enable with no model id shows usage" "$out" "needs at least one model id"

run_models "$home" -- frobnicate --claude
check "an unknown sub-command is refused" "$rc" "2"
contains "an unknown sub-command shows usage" "$out" "Unknown models sub-command"

# ---------- the shipped slash commands ----------

# `models` refuses to guess a client, so every command line the wrapper tells
# Claude Code to run has to name one. {{SCOPE}} renders empty in user scope,
# which is exactly where a missing --claude would surface as a hard failure.
commands_dir="$script_dir/../commands"
for wrapper in router-models models; do
  file="$commands_dir/$wrapper.md"
  if [ ! -f "$file" ]; then
    no "$wrapper.md is shipped" "a file at $file" "missing"
    continue
  fi
  bad="$(grep -o 'npx @workweave/router models[^`]*' "$file" | grep -cv -- '--claude' || true)"
  check "every $wrapper.md command line names a client" "$bad" "0"
done

# The alias must stay a copy of the primary wrapper, or the two drift.
check "models.md and router-models.md share one body" \
  "$(diff <(sed -n '6,$p' "$commands_dir/router-models.md") <(sed -n '6,$p' "$commands_dir/models.md") >/dev/null 2>&1 && echo same || echo differs)" \
  "same"

echo
echo "$pass passed, $fail failed"
[ "$fail" -eq 0 ]
