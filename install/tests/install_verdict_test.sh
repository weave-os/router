#!/usr/bin/env bash
# Offline tests for the install's closing verdict, first-launch guidance, and
# the key/identity handoff between the clients a multi-client `setup` installs.
set -uo pipefail
unset WEAVE_ROUTER_KEY WEAVE_USER_EMAIL

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
installer="${INSTALLER:-$script_dir/../install.sh}"
[ -f "$installer" ] || { echo "cannot find installer at $installer" >&2; exit 1; }

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

fake_bin="$work/bin"
mkdir -p "$fake_bin" "$work/tmp"
# /validate answers FAKE_VALIDATE_STATUS (000 = unreachable) the way curl -f -w does.
cat >"$fake_bin/curl" <<'FAKE_CURL'
#!/usr/bin/env bash
url="${!#}"
case "$url" in
  */health) [ "${FAKE_VALIDATE_STATUS:-200}" != 000 ] ;;
  */validate)
    status="${FAKE_VALIDATE_STATUS:-200}"
    while [ $# -gt 0 ]; do
      case "$1" in -w) printf '%s' "$status"; shift 2 ;; *) shift ;; esac
    done
    case "$status" in 2*) exit 0 ;; 000) exit 7 ;; *) echo "curl: (22) The requested URL returned error: $status" >&2; exit 22 ;; esac
    ;;
  *) exit 0 ;;
esac
FAKE_CURL
cat >"$fake_bin/claude" <<'FAKE_CLAUDE'
#!/usr/bin/env bash
if [ "$*" = "auth status --json" ]; then
  if [ "${FAKE_CLIENT_LOGGED_IN:-0}" = 1 ]; then echo '{"loggedIn":true}'; exit 0; fi
  echo '{"loggedIn":false}'; exit 1
fi
FAKE_CLAUDE
cat >"$fake_bin/codex" <<'FAKE_CODEX'
#!/usr/bin/env bash
case "$*" in
  "login status")
    if [ "${FAKE_CLIENT_LOGGED_IN:-0}" = 1 ]; then echo "Logged in using an API key"; exit 0; fi
    [ "${FAKE_CLIENT_HOLDS_STDOUT:-0}" = 1 ] && sleep 30 &
    echo "Not logged in" >&2; exit 1 ;;
  --version) echo "codex-cli 0.164.0" ;;
esac
FAKE_CODEX
chmod +x "$fake_bin/curl" "$fake_bin/claude" "$fake_bin/codex"
test_path="$fake_bin:$PATH"

pass=0
fail=0
check() { # check <name> <command...>
  local name="$1"; shift
  if "$@"; then echo "  ok   $name"; pass=$((pass + 1)); else echo "  FAIL $name"; fail=$((fail + 1)); fi
}
handoff_key_empty() { [ -s "$1" ] && [ -z "$(sed -n 1p "$1")" ]; }
no_setup_handoff_left() { ! compgen -G "$work/tmp/weave-setup.*" >/dev/null; }
no_temp_files_left() { ! compgen -G "$work/tmp/weave-validate.*" >/dev/null && ! compgen -G "$work/tmp/weave-capture.*" >/dev/null; }
has() { grep -Fq -- "$2" <<<"$1"; }
lacks() { ! grep -Fq -- "$2" <<<"$1"; }

# install <home> <validate-status> [env KEY=VAL ...] -- <installer args...>
install() {
  local home="$1" status="$2"; shift 2
  local extra_env=()
  while [ "${1:-}" != "--" ]; do extra_env+=("$1"); shift; done
  shift
  mkdir -p "$home"
  env HOME="$home" XDG_CACHE_HOME="$home/.cache" TMPDIR="$work/tmp" PATH="$test_path" NO_COLOR=1 \
    FAKE_VALIDATE_STATUS="$status" "${extra_env[@]}" \
    bash "$installer" "$@" --scope user --base-url http://127.0.0.1:9 </dev/null 2>&1
}

codex_key() {
  sed -n 's/.*"X-Weave-Router-Key"[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' "$1" 2>/dev/null | head -n 1
}
codex_email() {
  sed -n 's/.*"X-Weave-User-Email"[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' "$1" 2>/dev/null | head -n 1
}

echo "install.sh closing verdict"

out="$(install "$work/wrong-fleet" 403 WEAVE_ROUTER_KEY=rk_secret_403 -- --claude)"
check "a key the router refuses (403) is not announced as installed" lacks "$out" "Weave Router installed"
check "a 403 explains the deployment mismatch" has "$out" "✗ Weave Router is configured for Claude Code, but not working yet: http://127.0.0.1:9 does not serve this key"
check "a 403 points at --base-url" has "$out" "Fix: re-run with the router URL shown next to your key: npx @weave-os/router --claude --base-url <url>"
check "raw curl errors are not shown" lacks "$out" "curl: (22)"
check "the key never appears in output" lacks "$out" "rk_secret_403"
check "a failed check skips first-launch guidance" lacks "$out" "Next:"

out="$(install "$work/bad-key" 401 WEAVE_ROUTER_KEY=rk_secret_401 -- --claude)"
check "a 401 says the key was not recognized" has "$out" "not working yet: the router did not recognize this key"
check "a 401 suggests --rotate-key" has "$out" "npx @weave-os/router --claude --rotate-key"

out="$(install "$work/offline" 000 WEAVE_ROUTER_KEY=rk_secret_offline -- --codex)"
check "an unreachable router is not announced as installed" lacks "$out" "Weave Router installed"
check "an unreachable router says so" has "$out" "not working yet: could not reach http://127.0.0.1:9"
check "an unreachable router suggests update" has "$out" "npx @weave-os/router update --codex"

install "$work/outage" 200 WEAVE_ROUTER_KEY=rk_saved -- --claude >/dev/null
out="$(install "$work/outage" 503 -- --claude)"
check "a router outage is not announced as installed" lacks "$out" "Weave Router installed"
check "a router outage reports the HTTP status" has "$out" "not working yet: http://127.0.0.1:9 answered HTTP 503"
check "a router outage does not call the saved key rejected" lacks "$out" "was rejected"

out="$(install "$work/claude-new" 200 WEAVE_ROUTER_KEY=rk_ok -- --claude)"
check "a valid key is announced as installed" has "$out" "✓ Weave Router installed for Claude Code."
check "a signed-out Claude Code user is told to /login" has "$out" "Next: Claude Code isn't signed in yet. Run claude and type /login"

out="$(install "$work/claude-ready" 200 WEAVE_ROUTER_KEY=rk_ok FAKE_CLIENT_LOGGED_IN=1 -- --claude)"
check "a signed-in Claude Code user is told to just run claude" has "$out" "Next: run claude; your next prompt goes through Weave Router."

out="$(install "$work/codex-new" 200 WEAVE_ROUTER_KEY=rk_ok -- --codex)"
check "a signed-out Codex user is told to codex login" has "$out" "Next: Codex isn't signed in yet. Run codex login first"
check "Codex users are warned about the hook review" has "$out" "Choose Trust all"

started=$SECONDS
out="$(install "$work/codex-stuck" 200 WEAVE_ROUTER_KEY=rk_ok FAKE_CLIENT_HOLDS_STDOUT=1 -- --codex)"
check "a client CLI that leaves a child holding stdout does not stall the install" test $((SECONDS - started)) -lt 20
check "the login hint still shows when the client leaves a child behind" has "$out" "Next: Codex isn't signed in yet."

out="$(install "$work/codex-ready" 200 WEAVE_ROUTER_KEY=rk_ok FAKE_CLIENT_LOGGED_IN=1 -- --codex)"
check "a signed-in Codex user is told to just run codex" has "$out" "Next: run codex; your next prompt goes through Weave Router."

echo "setup key and identity handoff"

handoff="$work/handoff-ok"; : >"$handoff"
install "$work/handoff-ok-home" 200 WEAVE_ROUTER_KEY=rk_shared WEAVE_USER_EMAIL=dev@example.com \
  WEAVE_SETUP_HANDOFF_FILE="$handoff" -- --claude >/dev/null
check "the first client hands its validated key to the next" test "$(sed -n 1p "$handoff")" = rk_shared
check "the first client hands its identity choice to the next" test "$(sed -n 2p "$handoff")" = dev@example.com

handoff="$work/handoff-bad"; : >"$handoff"
install "$work/handoff-bad-home" 401 WEAVE_ROUTER_KEY=rk_rejected WEAVE_SETUP_HANDOFF_FILE="$handoff" -- --claude >/dev/null
check "a rejected key is not handed on" handoff_key_empty "$handoff"

home="$work/shared"
out="$(install "$home" 200 WEAVE_SETUP_SHARED_KEY=rk_from_claude WEAVE_SETUP_IDENTITY_RESOLVED=1 \
  WEAVE_SETUP_SHARED_EMAIL=dev@example.com -- --codex)"
check "the next client installs with the shared key and no prompt" test "$(codex_key "$home/.codex/config.toml")" = rk_from_claude
check "the next client reuses the identity choice" test "$(codex_email "$home/.codex/config.toml")" = dev@example.com
check "the next client says where the key came from" has "$out" "Using the router key from the Claude Code setup above."

home="$work/shared-optout"
install "$home" 200 WEAVE_SETUP_SHARED_KEY=rk_from_claude WEAVE_SETUP_IDENTITY_RESOLVED=1 \
  WEAVE_USER_EMAIL=env@example.com -- --codex >/dev/null
check "an identity opt-out carries over instead of falling back to the environment" \
  test -z "$(codex_email "$home/.codex/config.toml")"

home="$work/setup"
out="$(install "$home" 200 WEAVE_ROUTER_KEY=rk_setup -- setup --non-interactive)"
check "setup installs Claude Code" has "$out" "✓ Weave Router installed for Claude Code."
check "setup installs Codex" has "$out" "✓ Weave Router installed for Codex."
check "setup removes its handoff file" no_setup_handoff_left
check "installs leave no validation or capture temp files behind" no_temp_files_left

echo
echo "$pass passed, $fail failed"
[ "$fail" -eq 0 ]
