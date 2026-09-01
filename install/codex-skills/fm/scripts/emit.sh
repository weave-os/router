#!/usr/bin/env bash
# Alias of force-model/scripts/emit.sh
set -euo pipefail
if [ "$#" -eq 0 ] || [ -z "${1:-}" ]; then
  echo "usage: emit.sh <model-id>" >&2
  exit 1
fi
printf ' /force-model %s\n' "$*"
