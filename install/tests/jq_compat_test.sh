#!/usr/bin/env bash
#
# The installer runs on whatever jq the machine already has; ensure_jq only
# downloads jq when none is on PATH. jq 1.6 (still the distro jq on some LTS
# releases) rejects variables named after its keywords, e.g. `--arg label` /
# `$label`, with "syntax error, unexpected label". jq 1.7+ accepts them, so CI
# alone would not notice. Keep every jq variable name in shipped installer
# scripts off jq 1.6's keyword list.

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
keywords='(def|if|then|elif|else|end|as|reduce|foreach|try|catch|label|import|include|and|or|module|__loc__)'
arg_binding="--(arg|argjson|slurpfile|rawfile)[[:space:]]+${keywords}([[:space:]]|$)"
pattern_binding="(as|label)[[:space:]]+[$]${keywords}([^[:alnum:]_]|$)"

cd "$repo_root"
tracked="$(git ls-files install | grep -v '^install/tests/')"
mapfile -t files <<<"$tracked"
scan_status=0
hits="$(grep -InE -e "$arg_binding" -e "$pattern_binding" -- "${files[@]}")" || scan_status=$?
[ "$scan_status" -le 1 ] || { echo "FAIL: grep could not scan the installer files (exit $scan_status)" >&2; exit 1; }

if [ -n "$hits" ]; then
  printf 'FAIL: jq variables named after jq 1.6 keywords (rename the variable):\n%s\n' "$hits" >&2
  exit 1
fi
echo "ok   installer jq variable names parse on jq 1.6"
