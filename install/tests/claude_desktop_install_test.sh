#!/usr/bin/env bash
# Regression tests for the Claude Desktop install, re-install, and uninstall
# lifecycle: a local proxy service with its own api.anthropic.com certificate
# and the router key, and no change to Claude Code's settings.json.

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
installer="$script_dir/../install.sh"
uninstaller="$script_dir/../uninstall.sh"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
home="$work/home"
fake_bin="$work/bin"
calls="$work/calls.log"
mkdir -p "$home" "$fake_bin"
: >"$calls"

cat >"$fake_bin/curl" <<'FAKE_CURL'
#!/usr/bin/env bash
case "${!#}" in
  http://127.0.0.1:9/validate|http://127.0.0.1:9/health|http://127.0.0.1:47821/health) ;;
  *) exit 22 ;;
esac
while [ $# -gt 0 ]; do
  case "$1" in
    -o) printf '%s' '{}' >"$2"; shift 2 ;;
    -w) printf '200'; shift 2 ;;
    *) shift ;;
  esac
done
FAKE_CURL
for tool in systemctl launchctl; do
  printf '#!/usr/bin/env bash\nprintf "%s %%s\\n" "$*" >>"%s"\n' "$tool" "$calls" >"$fake_bin/$tool"
done
# shellcheck disable=SC2016 # expands inside the fake, not here
printf '#!/usr/bin/env bash\nprintf "%%s\\n" "${FAKE_UNAME:-Linux}"\n' >"$fake_bin/uname"
chmod +x "$fake_bin"/*
test_path="$fake_bin:$PATH"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

run() {
  env -u WEAVE_ROUTER_KEY -u XDG_CONFIG_HOME HOME="$home" PATH="$test_path" NO_COLOR=1 "$@"
}

install_desktop() {
  run "$@" bash "$installer" --claude-desktop --non-interactive --base-url http://127.0.0.1:9 --email dev@example.com
}

shim_dir="$home/.weave-router/desktop-shim"
config="$shim_dir/config.json"
unit="$home/.config/systemd/user/weave-router-desktop-shim.service"
plist="$home/Library/LaunchAgents/ai.workweave.router-desktop-shim.plist"

out="$(install_desktop env WEAVE_ROUTER_KEY=rk_desktop_test 2>&1)" || fail "install failed: $out"

[ "$(jq -r '.router_key' "$config")" = "rk_desktop_test" ] || fail "router key not stored"
[ "$(jq -r '.router_base_url' "$config")" = "http://127.0.0.1:9" ] || fail "router base URL not stored"
[ "$(jq -r '.listen_port' "$config")" = "47821" ] || fail "default port not stored"
[ "$(stat -c %a "$config" 2>/dev/null || stat -f %Lp "$config")" = "600" ] || fail "config holding the key is not 0600"
[ "$(stat -c %a "$shim_dir/leaf-key.pem" 2>/dev/null || stat -f %Lp "$shim_dir/leaf-key.pem")" = "600" ] || fail "leaf key is not 0600"
openssl verify -CAfile "$shim_dir/ca.pem" "$shim_dir/leaf.pem" >/dev/null || fail "leaf is not signed by the trusted CA"
openssl x509 -in "$shim_dir/leaf.pem" -noout -ext subjectAltName | grep -q 'DNS:api.anthropic.com' || fail "leaf is not for api.anthropic.com"
openssl x509 -in "$shim_dir/ca.pem" -noout -ext nameConstraints | grep -q 'DNS:api.anthropic.com' || fail "CA is not limited to api.anthropic.com"
[ ! -e "$shim_dir/ca-key.pem" ] || fail "CA private key was left on disk"
cmp -s "$script_dir/../desktop-shim.js" "$shim_dir/desktop-shim.js" || fail "proxy script not copied next to its config"
grep -Fq "ExecStart=\"$(command -v node)\" \"$shim_dir/desktop-shim.js\" \"$config\"" "$unit" || fail "systemd unit does not run the proxy: $(cat "$unit")"
grep -Fq "systemctl --user enable weave-router-desktop-shim.service" "$calls" || fail "service not enabled"
grep -Fq "systemctl --user restart weave-router-desktop-shim.service" "$calls" || fail "service not (re)started"
[ ! -e "$home/.claude/settings.json" ] || fail "Claude Code settings.json was touched"
token="$(jq -r '.proxy_token' "$config")"
[[ "$token" =~ ^[0-9a-f]{48}$ ]] || fail "proxy token missing or malformed: $token"
grep -Fq "HTTPS_PROXY=http://weave:$token@127.0.0.1:47821" <<<"$out" || fail "Desktop env lines not printed with the proxy token: $out"
grep -Fq "NODE_EXTRA_CA_CERTS=$shim_dir/ca.pem" <<<"$out" || fail "CA path not printed: $out"

# A re-install reuses the stored key and keeps the CA Desktop already trusts.
ca_before="$(cat "$shim_dir/ca.pem")"
install_desktop >/dev/null 2>&1 || fail "re-install without a key in env failed"
[ "$(jq -r '.router_key' "$config")" = "rk_desktop_test" ] || fail "re-install lost the stored key"
[ "$(cat "$shim_dir/ca.pem")" = "$ca_before" ] || fail "re-install replaced the trusted CA"
[ "$(jq -r '.proxy_token' "$config")" = "$token" ] || fail "re-install changed the proxy token Desktop was given"

# macOS runs the proxy as a launchd agent instead.
: >"$calls"
install_desktop env FAKE_UNAME=Darwin >/dev/null 2>&1 || fail "macOS install failed"
grep -Fq "<string>ai.workweave.router-desktop-shim</string>" "$plist" || fail "launchd plist missing its label"
grep -Fq "<string>$config</string>" "$plist" || fail "launchd plist does not pass the config"
grep -Eq "^launchctl bootstrap gui/[0-9]+ $plist$" "$calls" || fail "launchd agent not bootstrapped: $(cat "$calls")"

# Paths are XML-escaped in the plist.
odd_home="$work/odd&<home"
mkdir -p "$odd_home"
run env HOME="$odd_home" FAKE_UNAME=Darwin WEAVE_ROUTER_KEY=rk_desktop_test bash "$installer" --claude-desktop --non-interactive \
  --base-url http://127.0.0.1:9 --email dev@example.com >/dev/null 2>&1 || fail "install under an odd HOME failed"
python3 -c 'import plistlib, sys; plistlib.load(open(sys.argv[1], "rb"))' \
  "$odd_home/Library/LaunchAgents/ai.workweave.router-desktop-shim.plist" || fail "plist with & and < in paths does not parse"

# A project-scope or unrelated --dir uninstall leaves the user's Desktop proxy alone.
mkdir -p "$work/project"
run bash "$uninstaller" --scope project --dir "$work/project" >/dev/null 2>&1 || true
[ -f "$config" ] || fail "project-scope uninstall removed the Desktop proxy"
run bash "$uninstaller" --claude-desktop --dir "$work/project" >/dev/null 2>&1 || true
[ -f "$unit" ] || fail "--dir uninstall removed the systemd unit of another install"
[ -f "$plist" ] || fail "--dir uninstall removed the launchd plist of another install"

if run bash "$installer" off --claude-desktop --non-interactive >/dev/null 2>&1; then
  fail "off --claude-desktop should be rejected"
fi

: >"$calls"
run bash "$uninstaller" --claude-desktop >/dev/null 2>&1 || fail "uninstall failed"
[ ! -e "$shim_dir" ] || fail "proxy dir left behind"
[ ! -e "$unit" ] || fail "systemd unit left behind"
[ ! -e "$plist" ] || fail "launchd plist left behind"
grep -Fq "systemctl --user disable --now weave-router-desktop-shim.service" "$calls" || fail "systemd service not stopped"
grep -Eq "^launchctl bootout gui/[0-9]+/ai.workweave.router-desktop-shim$" "$calls" || fail "launchd agent not stopped"

# A bare --uninstall finds and removes the Desktop install too.
install_desktop env WEAVE_ROUTER_KEY=rk_desktop_test >/dev/null 2>&1 || fail "second install failed"
out="$(run bash "$uninstaller" 2>&1)" || fail "bare uninstall failed: $out"
grep -Fq "Claude Desktop" <<<"$out" || fail "bare uninstall did not report Claude Desktop: $out"
[ ! -e "$shim_dir" ] || fail "bare uninstall left the proxy dir"

echo "claude_desktop_install_test: ok"
