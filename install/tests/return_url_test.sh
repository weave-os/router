#!/usr/bin/env bash
#
# Regression coverage for --return-url. The browser must open only after both
# post-install probes succeed, including when --quiet is set.

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
  MINGW*|MSYS*|CYGWIN*) opener="cmd.exe" ;;
  *) opener="xdg-open" ;;
esac
if [ "$opener" = "cmd.exe" ]; then
  printf '%s\n' '#!/usr/bin/env bash' 'printf "%s\\n" "${@: -1}" >"$OPEN_LOG"' >"$fake_bin/$opener"
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

health_home="$work/health-failure"
health_log="$work/health-failure-browser.log"
run_install "$health_home" "$health_log" 0 1
check "does not open return URL when health fails" "" "$(cat "$health_log")"

validate_home="$work/validate-failure"
validate_log="$work/validate-failure-browser.log"
run_install "$validate_home" "$validate_log" 1 0
check "does not open return URL when key validation fails" "" "$(cat "$validate_log")"

if [ "$fail" -gt 0 ]; then
  printf '\n%d passed, %d failed\n' "$pass" "$fail" >&2
  exit 1
fi
printf '\n%d passed\n' "$pass"
