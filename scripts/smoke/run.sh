#!/usr/bin/env bash
# Isolated, replay-only by default. See docs/SMOKE.md for explicit recording.
set -euo pipefail
exec python3 "$(dirname "${BASH_SOURCE[0]}")/runner.py"
