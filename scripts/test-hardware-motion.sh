#!/usr/bin/env bash
# Tier 5 hardware motion tests with report generation.
#
# This wrapper keeps the old command working, but delegates to the unified
# runner so motion tests are executed once and a report is generated.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
exec bash "$REPO/scripts/test-all-report.sh" hardware-motion
