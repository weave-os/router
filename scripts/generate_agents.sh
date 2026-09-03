#!/usr/bin/env bash
set -euo pipefail

usage() { echo "usage: $0 [--check] [--root <repository>]" >&2; }

check_only=false
repository_root=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --check) check_only=true; shift ;;
    --root)
      [[ $# -ge 2 ]] || { usage; exit 2; }
      repository_root=$2
      shift 2
      ;;
    *) usage; exit 2 ;;
  esac
done

if [[ -z "$repository_root" ]]; then
  repository_root=$(git rev-parse --show-toplevel)
fi
repository_root=$(cd "$repository_root" && pwd)

render_mirror() {
  local source=$1
  awk '
    NR == 1 {
      if ($0 !~ / — CLAUDE$/) {
        print "error: " FILENAME ": first line must end with \" — CLAUDE\"" > "/dev/stderr"
        exit 2
      }
      sub(/ — CLAUDE$/, " — AGENTS")
    }
    NR == 3 {
      if ($0 !~ /\[AGENTS\.md\]\(AGENTS\.md\)/) {
        print "error: " FILENAME ": line 3 must link to AGENTS.md" > "/dev/stderr"
        exit 2
      }
      sub(/\[AGENTS\.md\]\(AGENTS\.md\)/, "[CLAUDE.md](CLAUDE.md)")
    }
    { print }
  ' "$source"
}

claude_guides=()
while IFS= read -r guide; do
  claude_guides+=("$guide")
done < <(
  find "$repository_root" -type f -name CLAUDE.md \
    -not -path '*/.git/*' -not -path '*/node_modules/*' -print | LC_ALL=C sort
)

if [[ ${#claude_guides[@]} -eq 0 ]]; then
  echo "error: no CLAUDE.md guides found under $repository_root" >&2
  exit 1
fi

failures=0
for source in "${claude_guides[@]}"; do
  mirror="${source%CLAUDE.md}AGENTS.md"
  temporary=$(mktemp "${mirror}.tmp.XXXXXX")
  if ! render_mirror "$source" > "$temporary"; then
    rm -f "$temporary"
    exit 1
  fi
  if $check_only; then
    if [[ ! -f "$mirror" ]] || ! cmp -s "$temporary" "$mirror"; then
      echo "error: generated mirror is stale: ${mirror#"$repository_root"/}" >&2
      if [[ -f "$mirror" ]]; then diff -u "$mirror" "$temporary" || true; else echo "error: mirror is missing" >&2; fi
      failures=$((failures + 1))
    fi
    rm -f "$temporary"
  else
    mv "$temporary" "$mirror"
  fi
done

while IFS= read -r mirror; do
  source="${mirror%AGENTS.md}CLAUDE.md"
  if [[ ! -f "$source" ]]; then
    echo "error: AGENTS.md has no canonical CLAUDE.md: ${mirror#"$repository_root"/}" >&2
    failures=$((failures + 1))
  fi
done < <(
  find "$repository_root" -type f -name AGENTS.md \
    -not -path '*/.git/*' -not -path '*/node_modules/*' -print | LC_ALL=C sort
)

[[ $failures -eq 0 ]] || exit 1
action=generated
$check_only && action=checked
echo "$action ${#claude_guides[@]} CLAUDE.md -> AGENTS.md mirrors"
