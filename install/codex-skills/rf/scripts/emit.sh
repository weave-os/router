#!/usr/bin/env bash
set -euo pipefail
if [ "$#" -eq 0 ] || [ -z "${1:-}" ]; then
  echo "usage: emit.sh <feedback>" >&2
  exit 1
fi
printf ' /router-feedback %s\n' "$*"
