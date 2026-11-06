#!/usr/bin/env bash
set -euo pipefail
if [ "$#" -ne 1 ] || [ -z "${1:-}" ]; then
  echo "usage: emit.sh <model-id>[:effort]" >&2
  exit 1
fi
printf ' /force-parent %s\n' "$1"
