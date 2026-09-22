#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

echo "=================================================================="
echo "  Running Hardware Motion Tests (with Native Go ECS Co-Simulation)"
echo "=================================================================="

# Enable hardware, motion, and our new simulation flag
export ALLOW_HARDWARE_TESTS=1
export ALLOW_MOTION_TESTS=1
export ALLOW_ECS_SIMULATION=1

# Run the motion test suite
go test -tags=hardware ./hardware/... -v \
  -run "^(TestMotion_|TestMotionProgram_)" \
  -count=1 \
  -timeout 600s

echo "== Tests Completed Successfully =="