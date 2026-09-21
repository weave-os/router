#!/usr/bin/env bash
#
# Regression coverage for --return-url. The browser must open only after both
# post-install probes succeed, including when --quiet is set.
# shellcheck disable=SC2016  # generated fixture scripts need their own runtime expansions

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
installer="${INSTALLER:-$script_dir/../install.sh}"
[ -f "$installer" ] || { echo "cannot find installer at $installer" >&2; exit 1; }

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

fake_bin="$work/bin"
mkdir -p "$fake_bin"
printf '%s\n' '#!/usr/bin/env bash' \
  'case "$*" in' \
  '  */health) [ "${HEALTH_OK:-1}" = 1 ] ;;' \
  '  */validate) [ "${VALIDATE_OK:-1}" = 1 ] ;;' \
  '  *) exit 0 ;;' \
  'esac' >"$fake_bin/curl"
chmod +x "$fake_bin/curl"

case "$(uname -s)" in
  Darwin*) opener="open" ;;
  MINGW*|MSYS*|CYGWIN*) opener="explorer.exe" ;;
  *) opener="xdg-open" ;;
esac
if [ "$opener" = "explorer.exe" ]; then
  printf '%s\n' '#!/usr/bin/env bash' 'printf "%s\\n" "$@" >"$OPEN_LOG"' 'exit 1' >"$fake_bin/$opener"
else
  printf '%s\n' '#!/usr/bin/env bash' 'printf "%s\\n" "$@" >"$OPEN_LOG"' >"$fake_bin/$opener"
fi
chmod +x "$fake_bin/$opener"

pass=0
fail=0
ok() { printf '  ok   %s\n' "$1"; pass=$((pass + 1)); }
no() { printf '  FAIL %s\n         expected: %s\n         actual:   %s\n' "$1" "$2" "$3"; fail=$((fail + 1)); }
check() { if [ "$2" = "$3" ]; then ok "$1"; else no "$1" "$3" "$2"; fi; }

run_install() {
  local home="$1" log="$2" health="$3" validate="$4"
  mkdir -p "$home"
  : >"$log"
  HOME="$home" OPEN_LOG="$log" PATH="$fake_bin:$PATH" NO_COLOR=1 \
    WEAVE_ROUTER_KEY="rk_return_url_test" HEALTH_OK="$health" VALIDATE_OK="$validate" \
    bash "$installer" --claude --scope user --quiet --non-interactive \
      --base-url http://127.0.0.1:9 --return-url 'https://app.example.test/continue?source=router&ok=1' \
      </dev/null >"$home/install.log" 2>&1 || true
}

echo "install.sh return URL"

success_home="$work/success"
success_log="$work/success-browser.log"
run_install "$success_home" "$success_log" 1 1
check "opens return URL after health and key validation pass" \
  "https://app.example.test/continue?source=router&ok=1" "$(cat "$success_log")"
if grep -q 'no default-browser opener was available' "$success_home/install.log"; then
  no "does not warn that the browser opener failed after launch" "no opener-failed warning" "warning present"
else
  ok "does not warn that the browser opener failed after launch"
fi

health_home="$work/health-failure"
health_log="$work/health-failure-browser.log"
run_install "$health_home" "$health_log" 0 1
check "does not open return URL when health fails" "" "$(cat "$health_log")"

validate_home="$work/validate-failure"
validate_log="$work/validate-failure-browser.log"
run_install "$validate_home" "$validate_log" 1 0
check "does not open return URL when key validation fails" "" "$(cat "$validate_log")"

invalid_home="$work/invalid-url"
invalid_log="$work/invalid-url-browser.log"
mkdir -p "$invalid_home"
: >"$invalid_log"
invalid_rc=0
HOME="$invalid_home" OPEN_LOG="$invalid_log" PATH="$fake_bin:$PATH" NO_COLOR=1 \
  WEAVE_ROUTER_KEY="rk_return_url_test" \
  bash "$installer" --claude --scope user --quiet --non-interactive \
    --base-url http://127.0.0.1:9 --return-url 'javascript:alert(1)' \
    </dev/null >"$invalid_home/install.log" 2>&1 || invalid_rc=$?
check "rejects non-http return URLs" "2" "$invalid_rc"
check "does not open a rejected return URL" "" "$(cat "$invalid_log")"

# explorer.exe commonly exits 1 after handing the URL to an already-running
# shell. Treat launch as success so the installer does not warn falsely.
win_home="$work/windows-opener"
mkdir -p "$win_home/bin"
printf '%s\n' '#!/usr/bin/env bash' 'printf "MSYS_NT-10.0\n"' >"$win_home/bin/uname"
printf '%s\n' '#!/usr/bin/env bash' 'printf "%s\n" "$@" >"$OPEN_LOG"' 'exit 1' >"$win_home/bin/explorer.exe"
chmod +x "$win_home/bin/uname" "$win_home/bin/explorer.exe"
: >"$work/windows-opener.log"
# Extract just open_url_in_browser from the installer so PATH/uname can be stubbed.
eval "$(awk '/^open_url_in_browser\(\)/,/^open_return_url_if_verified\(\)/' "$installer" | sed '$d')"
win_rc=0
OPEN_LOG="$work/windows-opener.log" PATH="$win_home/bin:$PATH" \
  open_url_in_browser 'https://app.example.test/continue?source=router&ok=1' || win_rc=$?
check "Windows opener succeeds when explorer.exe exits 1" "0" "$win_rc"
check "Windows opener still launches the URL" \
  "https://app.example.test/continue?source=router&ok=1" "$(cat "$work/windows-opener.log")"

if [ "$fail" -gt 0 ]; then
  printf '\n%d passed, %d failed\n' "$pass" "$fail" >&2
  exit 1
fi
printf '\n%d passed\n' "$pass"
