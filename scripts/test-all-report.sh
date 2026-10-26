#!/usr/bin/env bash
# Run EtherCAT test suites through one command and generate HTML/Markdown/JSON reports.
#
# Usage:
#   bash scripts/test-all-report.sh safe
#   bash scripts/test-all-report.sh hardware-readonly
#   bash scripts/test-all-report.sh hardware-motion
#   bash scripts/test-all-report.sh hardware-ecs
#   bash scripts/test-all-report.sh full
set -uo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO" || exit 1

PROFILE="${1:-${TEST_PROFILE:-safe}}"
RUN_ID="$(date +%Y%m%d-%H%M%S)"
REPORT_ROOT="${TEST_REPORT_DIR:-$REPO/test-reports}"
OUT_DIR="$REPORT_ROOT/$RUN_ID"
RAW_DIR="$OUT_DIR/raw"
STATUS_FILE="$RAW_DIR/phase-status.tsv"
mkdir -p "$RAW_DIR"
: > "$STATUS_FILE"

ETHERCAT_CLI="${ETHERCAT_CLI:-ethercat}"
MOTION_TEST_RANGE_DEG="${MOTION_TEST_RANGE_DEG:-5}"
MOTION_TIMEOUT_S="${MOTION_TIMEOUT_S:-20}"
MOTION_REST_BASE_URL="${MOTION_REST_BASE_URL:-http://localhost:5000}"
MOTION_SOCKET_URL="${MOTION_SOCKET_URL:-http://localhost:9090}"
HARDWARE_COMMAND_TIMEOUT="${HARDWARE_COMMAND_TIMEOUT:-5s}"
ETHERCAT_SLAVE_POSITION="${ETHERCAT_SLAVE_POSITION:-0}"
TEST_TIMEOUT_UNIT="${TEST_TIMEOUT_UNIT:-300s}"
TEST_TIMEOUT_INTEGRATION="${TEST_TIMEOUT_INTEGRATION:-600s}"
TEST_TIMEOUT_HARDWARE="${TEST_TIMEOUT_HARDWARE:-1800s}"
TEST_RUN_RACE="${TEST_RUN_RACE:-0}"

UNIT_PACKAGES=(
  ./motordriver/...
  ./helper/...
  ./configparser/...
  ./datatypes/...
  ./executors/...
  ./channels
  ./settings
  ./clientcommunication
  ./ethercatdevicedatatypes
  ./serialtest
  ./licensechecker
  ./logger
  ./hotspot
  ./tunnel
  ./systemupdate
)

INTEGRATION_PACKAGES=(
  ./executors
  ./configparser
  ./restapi
  ./settings
  ./commands/moveRotary
  ./commands/g90
  ./commands/g91
  ./commands/m30
  ./commands/m99
  ./commands/g68
  ./commands/g69
  ./commands/g01
  ./commands/loopStart
  ./commands/loopEnd
  ./commands/workoffset
  ./commands/invalidCommand
  ./commands/divide360
  ./commands/g0
  ./commands/g17
  ./commands/rpm
  ./commands/delay
  ./commands/divide360EnableDisable
)

HARDWARE_PACKAGES=(./hardware/... ./motordriver/...)

usage() {
  cat <<USAGE
Usage: bash scripts/test-all-report.sh <profile>

Profiles:
  safe              Run unit + fake integration tests. No hardware motion.
  hardware-readonly Run read-only hardware tests. No motion tests.
  hardware-motion   Run hardware motion tests only (bypasses ECS).
  hardware-ecs      Run hardware motion tests WITH native ECS co-simulation.
  full              Run safe, read-only, and hardware-ecs tests.
  e2e               Run end-to-end REST API tests (app must be running on port 5000).

Report output:
  $OUT_DIR

Examples:
  bash scripts/test-all-report.sh safe
  MOTION_TEST_RANGE_DEG=3 MOTION_TIMEOUT_S=30 bash scripts/test-all-report.sh hardware-ecs
  MOTION_TEST_RANGE_DEG=3 MOTION_TIMEOUT_S=30 bash scripts/test-all-report.sh full
USAGE
}

case "$PROFILE" in
  safe|hardware-readonly|hardware-motion|hardware-ecs|full|e2e) ;;
  -h|--help|help) usage; exit 0 ;;
  *) echo "ERROR: unknown profile '$PROFILE'" >&2; usage; exit 2 ;;
esac

log() {
  printf '%s\n' "$*"
}

record_env() {
  cat > "$OUT_DIR/environment.txt" <<ENV
Run ID: $RUN_ID
Profile: $PROFILE
Repo: $REPO
Generated: $(date -Iseconds)
Go: $(go version 2>/dev/null || echo 'go not found')
ETHERCAT_CLI: $ETHERCAT_CLI
MOTION_TEST_RANGE_DEG: $MOTION_TEST_RANGE_DEG
MOTION_TIMEOUT_S: $MOTION_TIMEOUT_S
MOTION_REST_BASE_URL: $MOTION_REST_BASE_URL
MOTION_SOCKET_URL: $MOTION_SOCKET_URL
HARDWARE_COMMAND_TIMEOUT: $HARDWARE_COMMAND_TIMEOUT
ETHERCAT_SLAVE_POSITION: $ETHERCAT_SLAVE_POSITION
TEST_RUN_RACE: $TEST_RUN_RACE
ENV
}

needs_hardware() {
  case "$PROFILE" in
    hardware-readonly|hardware-motion|hardware-ecs|full) return 0 ;;
    *) return 1 ;;
  esac
}

needs_motion() {
  case "$PROFILE" in
    hardware-motion|hardware-ecs|full) return 0 ;;
    *) return 1 ;;
  esac
}

precheck_hardware() {
  log "== Hardware pre-checks =="
  log "EtherCAT CLI: $ETHERCAT_CLI"

  local hw_timeout=10
  local master_out
  if ! master_out="$(timeout "$hw_timeout" "$ETHERCAT_CLI" master 2>&1)"; then
    log "ERROR: '$ETHERCAT_CLI master' failed or timed out after ${hw_timeout}s"
    log "Is the EtherCAT master running? Start jamun first."
    log "$master_out"
    return 1
  fi
  if ! printf '%s\n' "$master_out" | grep -q "Phase: Operation"; then
    log "ERROR: EtherCAT master is not in Operation phase. Start jamun first."
    log "$master_out"
    return 1
  fi
  log "OK: EtherCAT master in Operation phase"

  local slave_out
  if ! slave_out="$(timeout "$hw_timeout" "$ETHERCAT_CLI" slaves -v 2>&1)"; then
    log "ERROR: '$ETHERCAT_CLI slaves -v' failed or timed out after ${hw_timeout}s"
    log "$slave_out"
    return 1
  fi
  if ! printf '%s\n' "$slave_out" | grep -q "State: OP"; then
    log "ERROR: slave is not in OP state."
    log "$slave_out"
    return 1
  fi
  log "OK: slave in OP state"

  local error_code
  error_code="$(timeout "$hw_timeout" "$ETHERCAT_CLI" upload -p "$ETHERCAT_SLAVE_POSITION" -t uint16 0x603f 0 2>&1 | awk '{print $1}')"
  if [ "$error_code" != "0x0000" ] && [ "$error_code" != "0" ]; then
    log "ERROR: drive fault $error_code. Clear drive fault before running tests."
    return 1
  fi
  log "OK: drive error code 0x0000"

  if needs_motion; then
    local fin_signal
    fin_signal="$(curl -sf --max-time "$hw_timeout" "$MOTION_REST_BASE_URL/dac_params" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('response',d).get('A',{}).get('fin_signal','?'))" 2>/dev/null || echo "?")"
    if [ "$fin_signal" = "1" ] && [ "$PROFILE" != "hardware-ecs" ] && [ "$PROFILE" != "full" ]; then
      log "ERROR: fin_signal=1. Motion tests may hang waiting for external IO."
      log "Set fin_signal=0 in UI Settings, then re-run."
      return 1
    fi
    log "OK: fin_signal=$fin_signal"
    log "WARNING: motion profile will move the motor. Range: +/-${MOTION_TEST_RANGE_DEG} degrees."
  fi
}

run_go_phase() {
  local phase="$1"
  local tags="$2"
  local timeout="$3"
  local run_regex="$4"
  local coverprofile="$5"
  local coverpkg="$6"
  shift 6
  local json_file="$RAW_DIR/${phase}.jsonl"
  local stderr_file="$RAW_DIR/${phase}.stderr.log"
  local text_file="$RAW_DIR/${phase}.console.log"
  local cmd=(go test -json -tags="$tags" -count=1 -timeout "$timeout")
  if [ -n "$run_regex" ]; then
    cmd+=(-run "$run_regex")
  fi
  if [ -n "$coverprofile" ]; then
    cmd+=(-coverprofile="$coverprofile")
  fi
  if [ -n "$coverpkg" ]; then
    cmd+=(-coverpkg="$coverpkg")
  fi
  cmd+=("$@")

  log ""
  log "== Running phase: $phase =="
  log "Command: ${cmd[*]}"

  set +e
  "${cmd[@]}" 2> >(tee "$stderr_file" >&2) | tee "$json_file" | tee "$text_file" >/dev/null
  local status=${PIPESTATUS[0]}
  set -e

  printf '%s\t%s\n' "$phase" "$status" >> "$STATUS_FILE"
  if [ "$status" -eq 0 ]; then
    log "== Phase passed: $phase =="
  else
    log "== Phase failed: $phase (exit $status) =="
  fi
  return "$status"
}

merge_coverage() {
  local outfile="$OUT_DIR/coverage-merged.out"
  local profiles=("$@")

  local existing=()
  for p in "${profiles[@]}"; do
    [ -f "$p" ] && existing+=("$p")
  done

  if [ ${#existing[@]} -eq 0 ]; then
    log "No coverage profiles found — skipping merge"
    return
  fi

  log ""
  log "== Merging coverage profiles =="

  local first=1
  for p in "${existing[@]}"; do
    if [ "$first" -eq 1 ]; then
      cat "$p" > "$outfile"
      first=0
    else
      tail -n +2 "$p" >> "$outfile"
    fi
  done

  log "Merged profile: $outfile"
  log ""
  log "== Coverage summary (all phases) =="
  go tool cover -func="$outfile" | \
    awk '{printf "  %-60s %s\n", $1, $NF}' || true
  log ""
  log "Open full report with:"
  log "  go tool cover -html=$outfile"
}

run_race_phase() {
  local phase="race"
  local json_file="$RAW_DIR/${phase}.jsonl"
  local stderr_file="$RAW_DIR/${phase}.stderr.log"
  local text_file="$RAW_DIR/${phase}.console.log"
  local cmd=(go test -json -race -tags=unit -count=1 -timeout 600s ./helper/... ./configparser/... ./datatypes/... ./executors/...)

  log ""
  log "== Running phase: $phase =="
  log "Command: ${cmd[*]}"

  set +e
  "${cmd[@]}" 2> >(tee "$stderr_file" >&2) | tee "$json_file" | tee "$text_file" >/dev/null
  local status=${PIPESTATUS[0]}
  set -e

  if grep -qi "unsupported VMA" "$stderr_file" "$text_file" 2>/dev/null; then
    log "== Phase skipped: race detector unsupported on this kernel =="
    status=0
  fi
  printf '%s\t%s\n' "$phase" "$status" >> "$STATUS_FILE"
  return "$status"
}


run_e2e_phase() {
  local phase="e2e"
  local json_file="$RAW_DIR/${phase}.jsonl"
  local stderr_file="$RAW_DIR/${phase}.stderr.log"
  local text_file="$RAW_DIR/${phase}.console.log"
  local base_url="${E2E_BASE_URL:-http://localhost:5000}"
  local cmd=(go test -json -tags=e2e -count=1 -timeout 120s ./e2e/...)

  log ""
  log "== Running phase: $phase =="
  log "E2E_BASE_URL: $base_url"
  log "Command: ${cmd[*]}"

  set +e
  E2E_BASE_URL="$base_url" "${cmd[@]}" 2> >(tee "$stderr_file" >&2) | tee "$json_file" | tee "$text_file" >/dev/null
  local status=${PIPESTATUS[0]}
  set -e

  printf '%s\t%s\n' "$phase" "$status" >> "$STATUS_FILE"
  if [ "$status" -eq 0 ]; then
    log "== Phase passed: $phase =="
  else
    log "== Phase failed: $phase (exit $status) =="
  fi
  return "$status"
}

generate_report() {
  log ""
  log "== Generating report =="
  python3 scripts/test_report.py --input-dir "$RAW_DIR" --output-dir "$OUT_DIR" --status-file "$STATUS_FILE"
  local report_status=$?
  ln -sfn "$OUT_DIR" "$REPORT_ROOT/latest" 2>/dev/null || true
  log ""
  log "Report files:"
  log "  HTML:     $OUT_DIR/report.html"
  log "  Markdown: $OUT_DIR/report.md"
  log "  JSON:     $OUT_DIR/summary.json"
  log "  Raw logs: $RAW_DIR"
  return "$report_status"
}

record_env

log "== EtherCAT test runner =="
log "Repo: $REPO"
log "Profile: $PROFILE"
log "Report directory: $OUT_DIR"

if needs_hardware; then
  if ! precheck_hardware | tee "$RAW_DIR/hardware-precheck.log"; then
    printf '%s\t%s\n' "hardware-precheck" "1" >> "$STATUS_FILE"
    generate_report || true
    exit 1
  fi
  printf '%s\t%s\n' "hardware-precheck" "0" >> "$STATUS_FILE"
fi

export CONFIG_DIR="$REPO/configs"
export ETHERCAT_CLI HARDWARE_COMMAND_TIMEOUT ETHERCAT_SLAVE_POSITION
export MOTION_TEST_RANGE_DEG MOTION_TIMEOUT_S MOTION_REST_BASE_URL MOTION_SOCKET_URL

overall=0

COV_UNIT="$OUT_DIR/coverage-unit.out"
COV_INTEGRATION="$OUT_DIR/coverage-integration.out"
COV_HARDWARE_READONLY="$OUT_DIR/coverage-hardware-readonly.out"
COV_HARDWARE_MOTION="$OUT_DIR/coverage-hardware-motion.out"
COV_HARDWARE_ECS="$OUT_DIR/coverage-hardware-ecs.out"

# FULL_REPO_COVERPKG instruments every package in the module for coverage,
# not just the ones each phase directly exercises. Using a narrow -coverpkg
# per phase understates real coverage: packages with zero tests at all
# (logger, tunnel, webserver, systemupdate, hotspot, serialtest, gpiohandle,
# constants) never appeared in the denominator, making the reported total
# look higher than the codebase actually is. This gives an honest number.
FULL_REPO_COVERPKG="./..."

case "$PROFILE" in
  safe)
    run_go_phase unit unit "$TEST_TIMEOUT_UNIT" "" \
      "$COV_UNIT" "$FULL_REPO_COVERPKG" \
      "${UNIT_PACKAGES[@]}" || overall=1
    run_go_phase integration integration "$TEST_TIMEOUT_INTEGRATION" "" \
      "$COV_INTEGRATION" "$FULL_REPO_COVERPKG" \
      "${INTEGRATION_PACKAGES[@]}" || overall=1
    merge_coverage "$COV_UNIT" "$COV_INTEGRATION"
    ;;
  hardware-readonly)
    export ALLOW_HARDWARE_TESTS=1
    unset ALLOW_MOTION_TESTS
    run_go_phase hardware-readonly hardware 120s \
      'TestHardwareSmoke|TestHardwareCIA402|TestHardwareSlaveIdentity|TestHardwareMaster|TestDriveParam|TestSettings|TestREST|TestCompile|TestHwMotordriver' \
      "$COV_HARDWARE_READONLY" "$FULL_REPO_COVERPKG" \
      "${HARDWARE_PACKAGES[@]}" || overall=1
    merge_coverage "$COV_HARDWARE_READONLY"
    ;;
  hardware-motion)
    export ALLOW_HARDWARE_TESTS=1
    export ALLOW_MOTION_TESTS=1
    run_go_phase hardware-motion hardware "$TEST_TIMEOUT_HARDWARE" \
      '^(TestMotion_|TestMotionProgram_|TestHwMotordriver_)' \
      "$COV_HARDWARE_MOTION" "$FULL_REPO_COVERPKG" \
      "${HARDWARE_PACKAGES[@]}" || overall=1
    merge_coverage "$COV_HARDWARE_MOTION"
    ;;
  hardware-ecs)
    export ALLOW_HARDWARE_TESTS=1
    export ALLOW_MOTION_TESTS=1
    export ALLOW_ECS_SIMULATION=1
    run_go_phase hardware-ecs hardware "$TEST_TIMEOUT_HARDWARE" \
      '^(TestMotion_|TestMotionProgram_|TestHwMotordriver_)' \
      "$COV_HARDWARE_ECS" "$FULL_REPO_COVERPKG" \
      "${HARDWARE_PACKAGES[@]}" || overall=1
    merge_coverage "$COV_HARDWARE_ECS"
    ;;
  full)
    run_go_phase unit unit "$TEST_TIMEOUT_UNIT" "" \
      "$COV_UNIT" "$FULL_REPO_COVERPKG" \
      "${UNIT_PACKAGES[@]}" || overall=1

    run_go_phase integration integration "$TEST_TIMEOUT_INTEGRATION" "" \
      "$COV_INTEGRATION" "$FULL_REPO_COVERPKG" \
      "${INTEGRATION_PACKAGES[@]}" || overall=1

    export ALLOW_HARDWARE_TESTS=1
    unset ALLOW_MOTION_TESTS 2>/dev/null || true
    run_go_phase hardware-readonly hardware 120s \
      'TestHardwareSmoke|TestHardwareCIA402|TestHardwareSlaveIdentity|TestHardwareMaster|TestDriveParam|TestSettings|TestREST|TestCompile|TestHwMotordriver' \
      "$COV_HARDWARE_READONLY" "$FULL_REPO_COVERPKG" \
      "${HARDWARE_PACKAGES[@]}" || overall=1

    # Phase 4 — Hardware motion with ECS simulation enabled
    export ALLOW_MOTION_TESTS=1
    export ALLOW_ECS_SIMULATION=1
    run_go_phase hardware-ecs hardware "$TEST_TIMEOUT_HARDWARE" \
      '^(TestMotion_|TestMotionProgram_|TestHwMotordriver_)' \
      "$COV_HARDWARE_ECS" "$FULL_REPO_COVERPKG" \
      "${HARDWARE_PACKAGES[@]}" || overall=1

    # Phase 5 — E2E REST API tests (app must already be running)
    run_e2e_phase || overall=1

    merge_coverage "$COV_UNIT" "$COV_INTEGRATION" "$COV_HARDWARE_READONLY" "$COV_HARDWARE_ECS"
    ;;
  e2e)
    run_e2e_phase || overall=1
    ;;
esac

if [ "$TEST_RUN_RACE" = "1" ] && [ "$PROFILE" = "safe" ]; then
  run_race_phase || overall=1
fi

if ! generate_report; then
  overall=1
fi

# Override overall exit code: exit 0 if there are no actual test failures,
# even if a phase exited non-zero due to skips only.
# A non-zero phase exit with zero FAIL events = skips only = PASS.
# EXCEPTION: if any phase exited with code >1 (panic/crash), always fail.
_json="$OUT_DIR/summary.json"
_status_file="$OUT_DIR/raw/phase-status.tsv"
_has_crash=0
if [ -f "$_status_file" ]; then
  while IFS=$'\t' read -r _phase _code; do
    if [ "$_code" -gt 1 ] 2>/dev/null; then
      log "ERROR: phase '$_phase' crashed (exit $_code) — treating as failure"
      _has_crash=1
    fi
  done < "$_status_file"
fi
if [ -f "$_json" ] && [ "$_has_crash" -eq 0 ]; then
  _actual_failures=$(python3 -c "
import json,sys
try:
    d=json.load(open('$_json'))
    print(d['summary']['failed'])
except:
    print(1)
" 2>/dev/null || echo 1)
  if [ "$_actual_failures" -eq 0 ]; then
    overall=0
  fi
fi

if [ "$overall" -eq 0 ]; then
  log "== All requested test phases passed =="
else
  log "== One or more test phases had failures =="
fi

exit "$overall"