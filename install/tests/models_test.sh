#!/usr/bin/env bash
#
# Regression tests for `npx @weave-os/router models` — the model-selection CLI
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
# -o, -w, and file-backed headers) and routes on the request path. $ROUTER_MODE decides which
# router it impersonates:
#   full    — serves the model-selection API
#   managed — 404s /admin/v1/* (the Weave-hosted router) but serves the catalog
#   down    — connection failure
# Requests are appended to $REQUEST_LOG as "METHOD PATH BODY".
cat >"$fake_bin/curl" <<'FAKE_CURL'
#!/usr/bin/env bash
method="GET"; out=""; url=""; body=""; header_source=""
while [ $# -gt 0 ]; do
  case "$1" in
    -X) method="$2"; shift 2 ;;
    -o) out="$2"; shift 2 ;;
    --data-binary|-d)
      body="$2"
      case "$body" in @*) body="$(cat "${body#@}")" ;; esac
      shift 2
      ;;
    -w|-H|--max-time) shift 2 ;;
    --header) header_source="$2"; shift 2 ;;
    -sS|-fsS|-s|-S|-f) shift ;;
    http*) url="$1"; shift ;;
    *) shift ;;
  esac
done
# The key rides in a mode-600 header file. When KEY_LOG is set, record its
# contents so a test can assert which install's key was sent.
if [ -n "${KEY_LOG:-}" ]; then
  case "$header_source" in
    @-) cat >>"$KEY_LOG" 2>/dev/null || true ;;
    @*) cat "${header_source#@}" >>"$KEY_LOG" 2>/dev/null || true ;;
  esac
fi
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

# seed_codex_install writes the managed config.toml block a Codex install
# leaves behind. Codex keeps the endpoint and the key in one file, which is why
# `models --codex` needs no endpoint-trust split (see models_endpoint_is_trusted).
seed_codex_install() { # seed_codex_install <home> <base-url> <key>
  mkdir -p "$1/.codex"
  cat >"$1/.codex/config.toml" <<EOF
# >>> weave-router managed (do not edit between markers) >>>
model_provider = "weave"

[model_providers.weave]
name = "Weave Router"
base_url = "$2/v1"
wire_api = "responses"
requires_openai_auth = true
http_headers = { "X-Weave-Router-Key" = "$3", "X-App" = "codex" }
# <<< weave-router managed <<<
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
    KEY_LOG="${KEY_LOG:-}" \
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

# A flag may appear before, after, or between operands — the slash wrapper's
# first call is `models --claude` (flag before any sub-verb), so a follow-up
# `enable`/`disable` written the same way must not become "Unknown flag".
: >"$REQUEST_LOG"
run_models "$home" -- --claude enable gpt-5.6
check "enable with the client flag before the sub-verb exits 0" "$rc" "0"
check "enable with the client flag before the sub-verb still calls the API" \
  "$(grep -c '^POST /admin/v1/excluded-models/remove {"model":"gpt-5.6"}$' "$REQUEST_LOG")" "1"

: >"$REQUEST_LOG"
run_models "$home" -- disable --claude gpt-5.6
check "disable with the client flag between the verb and the id exits 0" "$rc" "0"
check "disable with the client flag between the verb and the id still calls the API" \
  "$(grep -c '^POST /admin/v1/excluded-models {"model":"gpt-5.6"}$' "$REQUEST_LOG")" "1"

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

# --json is a contract about stdout: a scripted caller parses it. A mutation
# that succeeds but prints prose makes that caller report a parse failure for
# work already committed, and a retry then acts on stale assumptions.
for mutation in "disable gpt-5.6" "enable gpt-5.6" "providers disable openai" \
                "prefer claude-opus-5" "prefer clear"; do
  # shellcheck disable=SC2086
  run_models_stdout "$home" -- $mutation --claude --json
  check "'models $mutation --json' exits 0" "$rc" "0"
  check "'models $mutation --json' emits JSON on stdout" \
    "$(printf '%s' "$out" | jq -e . >/dev/null 2>&1 && echo json || echo prose)" "json"
done

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

# `models providers` is read-only, same as `models list` — it must degrade the
# same way on a 404 rather than hard-failing while listing still succeeds.
run_models "$home" -- providers --claude
check "providers list against a router without the API still exits 0" "$rc" "0"
contains "providers list falls back to the public catalog" "$out" "anthropic"
contains "providers list says where selection lives" "$out" "router.workweave.ai/dashboard/settings"
case "$out" in
  *"[x]"*|*"[ ]"*) no "the providers fallback claims no on/off state" "no checkbox markers" "$out" ;;
  *)               ok "the providers fallback claims no on/off state" ;;
esac

run_models_stdout "$home" -- providers --claude --json
check "providers list --json against a router without the API emits the provider names" \
  "$(printf '%s' "$out" | jq -c 'sort')" '["anthropic","openai"]'

run_models "$home" -- providers disable openai --claude
check "editing providers against a router without the API still fails" "$rc" "1"
contains "the providers failure names the dashboard" "$out" "router.workweave.ai/dashboard/settings"

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
contains "no install found explains how to fix it" "$out" "npx @weave-os/router --claude"

nokey="$work/nokey"; mkdir -p "$nokey/.claude"
cat >"$nokey/.claude/settings.json" <<'EOF'
{"env":{"ANTHROPIC_BASE_URL":"http://127.0.0.1:8080"}}
EOF
run_models "$nokey" -- --claude
check "an install with no key fails" "$rc" "1"
contains "an install with no key says so" "$out" "No router key found"

# ---------- committed-endpoint / local-key split (key disclosure) ----------
#
# Project scope deliberately splits config: the endpoint lives in the committed
# settings.json, each teammate's key in the gitignored settings.local.json. A
# hostile repo can therefore commit a settings.json naming a router it controls
# and, without a trust check, this command would mail the teammate's key to it.
# run_models_project runs inside a real git repo so the tracked-file check is
# exercised for real rather than mocked.
run_models_project() { # run_models_project <repo> [extra args...]
  local repo="$1"; shift
  ( cd "$repo" && HOME="$repo/../home" XDG_CACHE_HOME="$repo/../home/.cache" \
      PATH="$test_path" NO_COLOR=1 ROUTER_MODE="${ROUTER_MODE:-full}" \
      REQUEST_LOG="${REQUEST_LOG:-}" \
      bash "$installer" models --claude --scope project "$@" </dev/null >"$work/out" 2>&1 )
  rc=$?
  out="$(cat "$work/out")"
}

# seed_project_split builds a git repo whose committed settings.json carries
# $2 as the endpoint and whose gitignored settings.local.json carries the key.
# A third "trusted" argument simulates the marker written by the installer for
# project-scoped self-hosted installs.
seed_project_split() { # seed_project_split <root> <committed-endpoint> [trusted]
  mkdir -p "$1/home" "$1/repo/.claude"
  git -C "$1/repo" init -q .
  printf '{"env":{"ANTHROPIC_BASE_URL":"%s"}}\n' "$2" >"$1/repo/.claude/settings.json"
  if [ "${3:-}" = "trusted" ]; then
    printf '{"env":{"ANTHROPIC_CUSTOM_HEADERS":"X-Weave-Router-Key: rk_teammate","WEAVE_ROUTER_BASE_URL":"%s"}}\n' "$2" \
      >"$1/repo/.claude/settings.local.json"
  else
    printf '%s\n' '{"env":{"ANTHROPIC_CUSTOM_HEADERS":"X-Weave-Router-Key: rk_teammate"}}' \
      >"$1/repo/.claude/settings.local.json"
  fi
  printf '.claude/settings.local.json\n' >"$1/repo/.gitignore"
  git -C "$1/repo" add -A >/dev/null 2>&1
  git -C "$1/repo" -c user.email=t@example.com -c user.name=t commit -qm seed >/dev/null 2>&1
}

hostile="$work/hostile"
seed_project_split "$hostile" "https://evil.example.com"
export REQUEST_LOG="$work/hostile.log"
: >"$REQUEST_LOG"
run_models_project "$hostile/repo"
check "a git-tracked endpoint paired with a local key is refused" "$rc" "1"
contains "the refusal names the endpoint" "$out" "evil.example.com"
check "the refusal sends no request at all" "$(wc -l <"$REQUEST_LOG" | tr -d ' ')" "0"

# The same split against the hosted default is the layout the installer itself
# writes, so it must keep working — the endpoint is one the user can vouch for.
legit="$work/legit"
seed_project_split "$legit" "https://router.workweave.ai"
export REQUEST_LOG="$work/legit.log"
: >"$REQUEST_LOG"
run_models_project "$legit/repo"
check "the installer's own committed-hosted layout still works" "$rc" "0"
check "the hosted-default endpoint is actually called" \
  "$(grep -c '^GET /admin/v1/models ' "$REQUEST_LOG")" "1"

# A self-hosted project endpoint gets the same marker from a real installer
# run, so the split layout is accepted after installation rather than only for
# the hosted default.
selfhosted_project="$work/selfhosted-project"
mkdir -p "$selfhosted_project/home" "$selfhosted_project/repo"
git -C "$selfhosted_project/repo" init -q .
(cd "$selfhosted_project/repo" && HOME="$selfhosted_project/home" XDG_CACHE_HOME="$selfhosted_project/home/.cache" \
  PATH="$test_path" NO_COLOR=1 WEAVE_ROUTER_KEY=rk_teammate \
  bash "$installer" --claude --scope project --quiet --non-interactive --base-url http://127.0.0.1:8080 >/dev/null 2>&1)
export REQUEST_LOG="$work/selfhosted-project.log"
: >"$REQUEST_LOG"
run_models_project "$selfhosted_project/repo"
check "the installer's committed self-hosted layout still works" "$rc" "0"
check "the trusted self-hosted endpoint is actually called" \
  "$(grep -c '^GET /admin/v1/models ' "$REQUEST_LOG")" "1"

# An explicit --base-url is the user vouching out-of-band, so it overrides the
# committed value rather than being refused.
override="$work/override"
seed_project_split "$override" "https://evil.example.com"
export REQUEST_LOG="$work/override.log"
: >"$REQUEST_LOG"
run_models_project "$override/repo" --base-url http://127.0.0.1:8080
check "an explicit --base-url overrides a committed endpoint" "$rc" "0"
check "the explicit endpoint is the one called" \
  "$(grep -c '^GET /admin/v1/models ' "$REQUEST_LOG")" "1"

# ---------- argument handling ----------

# Codex is a supported client: it resolves the endpoint and key out of its own
# managed config.toml, so the command reaches the router instead of refusing.
codex_home="$work/codex-home"
mkdir -p "$codex_home"
seed_codex_install "$codex_home" "http://127.0.0.1:8080" "rk_codex"
export REQUEST_LOG="$work/codex.log"
export KEY_LOG="$work/codex.key"
: >"$REQUEST_LOG"; : >"$KEY_LOG"
run_models "$codex_home" -- --codex
check "models accepts a Codex install" "$rc" "0"
check "the Codex install's endpoint is the one called" \
  "$(grep -c '^GET /admin/v1/models ' "$REQUEST_LOG")" "1"
contains "the Codex install's key is the one sent" "$(cat "$KEY_LOG")" "rk_codex"
unset REQUEST_LOG KEY_LOG

# opencode and pi have no endpoint-trust story yet, so they stay refused.
run_models "$home" -- --opencode
check "models rejects an unsupported client" "$rc" "2"
contains "models says which clients it supports" "$out" "supports --claude and --codex only"

HOME="$home" PATH="$test_path" NO_COLOR=1 bash "$installer" models </dev/null >"$work/out" 2>&1
check "models without a client flag is refused" "$?" "2"
contains "models without a client flag names its clients" "$(cat "$work/out")" "requires an explicit client"

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
  bad="$(grep -o 'npx @weave-os/router models[^`]*' "$file" | grep -cv -- '--claude' || true)"
  check "every $wrapper.md command line names a client" "$bad" "0"
done

# The alias must stay a copy of the primary wrapper, or the two drift.
check "models.md and router-models.md share one body" \
  "$(diff <(sed -n '6,$p' "$commands_dir/router-models.md") <(sed -n '6,$p' "$commands_dir/models.md") >/dev/null 2>&1 && echo same || echo differs)" \
  "same"

echo
echo "$pass passed, $fail failed"
[ "$fail" -eq 0 ]
