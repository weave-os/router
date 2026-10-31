#!/usr/bin/env bash
# Regression tests for the Claude Desktop install, re-install, and uninstall
# lifecycle: a local proxy service, an api.anthropic.com certificate trusted by
# the OS, and the OS auto-proxy pointed at the proxy's PAC file. Claude Code's
# settings.json is never touched.

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
installer="$script_dir/../install.sh"
uninstaller="$script_dir/../uninstall.sh"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
home="$work/home"
fake_bin="$work/bin"
state="$work/state"
calls="$work/calls.log"
mkdir -p "$home" "$fake_bin" "$state"
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
# The OS tools record their calls; networksetup and gsettings also keep state
# so the installer and uninstaller see what was set before.
cat >"$fake_bin/fake-os-tool" <<FAKE_OS
#!/usr/bin/env bash
tool="\$(basename "\$0")"
printf '%s %s\n' "\$tool" "\$*" >>"$calls"
case "\$tool \$1" in
  "sudo "*) exit 0 ;;
  "security verify-cert") exit 1 ;;
  "security remove-trusted-cert") [ -z "\${FAKE_TRUST_REMOVAL_FAILS:-}" ] || exit 1 ;;
  "networksetup -listallnetworkservices") printf 'An asterisk (*) denotes that a network service is disabled.\nWi-Fi\n*Old VPN\n' ;;
  "networksetup -getautoproxyurl") printf 'URL: %s\nEnabled: No\n' "\$(cat "$state/pac" 2>/dev/null || echo '(null)')" ;;
  "networksetup -setautoproxyurl") printf '%s' "\$3" >"$state/pac" ;;
  "gsettings get") if [ "\$3" = mode ]; then cat "$state/mode" 2>/dev/null || echo "'none'"; else echo "'\$(cat "$state/pac" 2>/dev/null)'"; fi ;;
  "gsettings set") if [ "\$3" = mode ]; then echo "'\$4'" >"$state/mode"; else printf '%s' "\$4" >"$state/pac"; fi ;;
esac
exit 0
FAKE_OS
for tool in systemctl launchctl security networksetup gsettings sudo; do
  ln -s fake-os-tool "$fake_bin/$tool"
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
  env -u WEAVE_ROUTER_KEY -u XDG_CONFIG_HOME -u WEAVE_DESKTOP_CERT_DIR HOME="$home" PATH="$test_path" NO_COLOR=1 "$@"
}

install_desktop() {
  run "$@" bash "$installer" --claude-desktop --non-interactive --base-url http://127.0.0.1:9 --email dev@example.com
}

reset_os() {
  : >"$calls"
  rm -f "$state/pac" "$state/mode"
}

shim_dir="$home/.weave-router/desktop-shim"
config="$shim_dir/config.json"
unit="$home/.config/systemd/user/weave-router-desktop-shim.service"
plist="$home/Library/LaunchAgents/ai.workweave.router-desktop-shim.plist"
pac_url="http://127.0.0.1:47821/proxy.pac"

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
grep -Fq "systemctl --user restart weave-router-desktop-shim.service" "$calls" || fail "service not (re)started"
grep -Fq "sudo install -m 644 $shim_dir/ca.pem /usr/local/share/ca-certificates/weave-router-claude-desktop.crt" "$calls" || fail "CA not added to the system trust store: $(cat "$calls")"
grep -Fq "sudo update-ca-certificates" "$calls" || fail "system trust store not refreshed"
[ "$(cat "$state/pac")" = "$pac_url" ] || fail "GNOME auto-proxy not pointed at the PAC"
[ "$(cat "$state/mode")" = "'auto'" ] || fail "GNOME proxy mode not set to auto"
[ ! -e "$home/.claude/settings.json" ] || fail "Claude Code settings.json was touched"
grep -Fq "quit and reopen Claude Desktop" <<<"$out" || fail "next step not printed: $out"

# A re-install reuses the stored key and keeps the CA the OS already trusts.
ca_before="$(cat "$shim_dir/ca.pem")"
install_desktop >/dev/null 2>&1 || fail "re-install without a key in env failed"
[ "$(jq -r '.router_key' "$config")" = "rk_desktop_test" ] || fail "re-install lost the stored key"
[ "$(cat "$shim_dir/ca.pem")" = "$ca_before" ] || fail "re-install replaced the trusted CA"

# An auto-proxy someone else set is left alone and the rule to add is printed.
reset_os
echo "'auto'" >"$state/mode"
printf 'http://corp.example/proxy.pac' >"$state/pac"
out="$(install_desktop 2>&1)" || fail "install next to a corporate PAC failed: $out"
[ "$(cat "$state/pac")" = "http://corp.example/proxy.pac" ] || fail "corporate PAC was replaced"
grep -Fq 'if (host === "api.anthropic.com") return "PROXY 127.0.0.1:47821";' <<<"$out" || fail "PAC rule not printed: $out"

# macOS: launchd agent, login-keychain trust, and the auto-proxy on enabled services.
reset_os
out="$(install_desktop env FAKE_UNAME=Darwin 2>&1)" || fail "macOS install failed: $out"
grep -Fq "password dialog" <<<"$out" || fail "--non-interactive macOS install did not warn about the trust dialog: $out"
grep -Fq "<string>ai.workweave.router-desktop-shim</string>" "$plist" || fail "launchd plist missing its label"
grep -Eq "^launchctl bootstrap gui/[0-9]+ $plist$" "$calls" || fail "launchd agent not bootstrapped: $(cat "$calls")"
grep -Fq "security add-trusted-cert -r trustRoot -k $home/Library/Keychains/login.keychain-db $shim_dir/ca.pem" "$calls" || fail "CA not trusted in the login keychain"
grep -Fq "sudo networksetup -setautoproxyurl Wi-Fi $pac_url" "$calls" || fail "auto-proxy not set on Wi-Fi: $(cat "$calls")"
! grep -Fq "Old VPN" "$calls" || fail "auto-proxy set on a disabled service"

# launchd rejects a malformed plist, so a HOME containing & or < must still parse.
odd_home="$work/odd&<home"
mkdir -p "$odd_home"
run env HOME="$odd_home" FAKE_UNAME=Darwin WEAVE_ROUTER_KEY=rk_desktop_test bash "$installer" --claude-desktop --non-interactive \
  --base-url http://127.0.0.1:9 --email dev@example.com >/dev/null 2>&1 || fail "install under an odd HOME failed"
python3 -c 'import plistlib, sys; plistlib.load(open(sys.argv[1], "rb"))' \
  "$odd_home/Library/LaunchAgents/ai.workweave.router-desktop-shim.plist" || fail "plist with & and < in paths does not parse"

# An organization can supply its own certificate pair.
org_certs="$work/org-certs"
mkdir -p "$org_certs"
cp "$shim_dir/ca.pem" "$shim_dir/leaf.pem" "$shim_dir/leaf-key.pem" "$org_certs/"
printf '# org marker\n' >>"$org_certs/ca.pem"
install_desktop env WEAVE_DESKTOP_CERT_DIR="$org_certs" >/dev/null 2>&1 || fail "install with WEAVE_DESKTOP_CERT_DIR failed"
cmp -s "$org_certs/ca.pem" "$shim_dir/ca.pem" || fail "organization CA not used"

# Uninstall must leave another install's Desktop proxy and service files intact.
mkdir -p "$work/project"
run bash "$uninstaller" --scope project --dir "$work/project" >/dev/null 2>&1 || true
[ -f "$config" ] || fail "project-scope uninstall removed the Desktop proxy"
run bash "$uninstaller" --claude-desktop --dir "$work/project" >/dev/null 2>&1 || true
[ -f "$unit" ] || fail "--dir uninstall removed the systemd unit of another install"
[ -f "$plist" ] || fail "--dir uninstall removed the launchd plist of another install"

if run bash "$installer" off --claude-desktop --non-interactive >/dev/null 2>&1; then
  fail "off --claude-desktop should be rejected"
fi

# A cancelled keychain prompt keeps ca.pem and the config so a rerun can remove the CA.
out="$(run env FAKE_UNAME=Darwin FAKE_TRUST_REMOVAL_FAILS=1 bash "$uninstaller" --claude-desktop 2>&1)" || fail "macOS uninstall with a cancelled prompt failed: $out"
[ -f "$shim_dir/ca.pem" ] || fail "uninstall deleted the CA it could not untrust"
[ -f "$config" ] || fail "uninstall deleted the config a rerun needs"
grep -Fq "Rerun: npx @weave-os/router --uninstall --claude-desktop" <<<"$out" || fail "no rerun hint: $out"
install_desktop env FAKE_UNAME=Darwin >/dev/null 2>&1 || fail "macOS reinstall failed"

: >"$calls"
run env FAKE_UNAME=Darwin bash "$uninstaller" --claude-desktop >/dev/null 2>&1 || fail "macOS uninstall failed"
grep -Fq "security remove-trusted-cert $shim_dir/ca.pem" "$calls" || fail "keychain trust not removed"
grep -Fq "sudo networksetup -setautoproxystate Wi-Fi off" "$calls" || fail "auto-proxy not turned off"
grep -Eq "^launchctl bootout gui/[0-9]+/ai.workweave.router-desktop-shim$" "$calls" || fail "launchd agent not stopped"
[ ! -e "$shim_dir" ] || fail "proxy dir left behind"
[ ! -e "$plist" ] || fail "launchd plist left behind"

# Linux uninstall reverts the GNOME auto-proxy, and a bare --uninstall finds the install.
reset_os
install_desktop env WEAVE_ROUTER_KEY=rk_desktop_test >/dev/null 2>&1 || fail "second install failed"
out="$(run bash "$uninstaller" 2>&1)" || fail "bare uninstall failed: $out"
grep -Fq "Claude Desktop" <<<"$out" || fail "bare uninstall did not report Claude Desktop: $out"
[ "$(cat "$state/mode")" = "'none'" ] || fail "GNOME proxy mode not reset"
grep -Fq "systemctl --user disable --now weave-router-desktop-shim.service" "$calls" || fail "systemd service not stopped"
[ ! -e "$shim_dir" ] || fail "bare uninstall left the proxy dir"
[ ! -e "$unit" ] || fail "systemd unit left behind"

echo "claude_desktop_install_test: ok"
