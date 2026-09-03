#!/usr/bin/env bash

set -euo pipefail

if [ "$#" -ne 3 ]; then
  echo "usage: $0 BASE_REF HEAD_REF typescript|shell|python" >&2
  exit 2
fi

base_ref="$1"
head_ref="$2"
category="$3"

case "$category" in
  typescript | shell | python) ;;
  *)
    echo "unknown file category: $category" >&2
    exit 2
    ;;
esac

git rev-parse --verify --quiet "${base_ref}^{commit}" >/dev/null
git rev-parse --verify --quiet "${head_ref}^{commit}" >/dev/null
git merge-base "$base_ref" "$head_ref" >/dev/null

git diff --name-only --diff-filter=ACMR -z "${base_ref}...${head_ref}" -- |
  while IFS= read -r -d '' path; do
    [ -f "$path" ] || continue

    case "$category" in
      typescript)
        case "$path" in
          frontend/*.ts | frontend/*.tsx | frontend/package.json | frontend/package-lock.json | frontend/tsconfig.json)
            printf '%s\0' "$path"
            ;;
        esac
        ;;
      shell)
        case "$path" in
          *.sh)
            printf '%s\0' "$path"
            ;;
          *)
            first_line=""
            IFS= read -r first_line < "$path" || true
            if [[ "$first_line" =~ ^#!.*[/[:space:]](ba|z|k)?sh([[:space:]]|$) ]]; then
              printf '%s\0' "$path"
            fi
            ;;
        esac
        ;;
      python)
        case "$path" in
          *.py) printf '%s\0' "$path" ;;
        esac
        ;;
    esac
  done
