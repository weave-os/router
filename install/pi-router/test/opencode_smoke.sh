#!/usr/bin/env bash
# The Python driver bounds and reaps entire process groups on macOS and Linux.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec python3 "$SCRIPT_DIR/opencode_conformance.py" "$@"
