#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

REQUIRE_PI_TARGET="${REQUIRE_PI_TARGET:-1}"
ETHERCAT_INCLUDE="${ETHERCAT_INCLUDE:-/opt/etherlab/include/ecrt.h}"
ETHERCAT_LIB="${ETHERCAT_LIB:-/home/pi/gosrc/src/EtherCAT/libethercatinterface.so}"
FALLBACK_ETHERCAT_LIB="$ROOT_DIR/libethercatinterface.so"

failures=0

check_file() {
  local label="$1"
  local path="$2"
  if [[ -e "$path" ]]; then
    echo "ok: $label: $path"
  else
    echo "missing: $label: $path"
    failures=$((failures + 1))
  fi
}

echo "== EtherCAT test environment check =="
echo "Repo: $ROOT_DIR"
echo

echo "== Go toolchain =="
go version
if [[ "$(go env CGO_ENABLED)" != "1" ]]; then
  echo "missing: CGO_ENABLED=1 is required for the target Raspberry Pi EtherCAT build"
  failures=$((failures + 1))
else
  echo "ok: CGO_ENABLED=1"
fi
echo

echo "== Target machine =="
if [[ "$REQUIRE_PI_TARGET" == "1" ]]; then
  if [[ -r /proc/cpuinfo ]] && grep -Eqi 'Raspberry Pi|BCM27|BCM28' /proc/cpuinfo; then
    echo "ok: Raspberry Pi CPU info detected"
  else
    echo "missing: Raspberry Pi CPU info not detected"
    echo "       Set REQUIRE_PI_TARGET=0 only for documentation or source-only checks."
    failures=$((failures + 1))
  fi
else
  echo "skip: Raspberry Pi detection disabled by REQUIRE_PI_TARGET=0"
fi
echo

echo "== EtherCAT build inputs =="
check_file "EtherLab header" "$ETHERCAT_INCLUDE"
if [[ -e "$ETHERCAT_LIB" ]]; then
  echo "ok: EtherCAT interface shared library: $ETHERCAT_LIB"
elif [[ -e "$FALLBACK_ETHERCAT_LIB" ]]; then
  echo "ok: EtherCAT interface shared library fallback: $FALLBACK_ETHERCAT_LIB"
else
  echo "missing: EtherCAT interface shared library: $ETHERCAT_LIB"
  echo "missing: EtherCAT interface shared library fallback: $FALLBACK_ETHERCAT_LIB"
  failures=$((failures + 1))
fi
echo

echo "== Hardware-test command inputs =="
if command -v "${ETHERCAT_CLI:-ethercat}" >/dev/null 2>&1; then
  echo "ok: EtherCAT CLI found: ${ETHERCAT_CLI:-ethercat}"
else
  echo "warn: EtherCAT CLI not found; safe unit/integration tests may still run, but make test-hardware will fail"
fi
if [[ -d "${CONFIG_DIR:-/home/pi/gosrc/src/EtherCAT/configs}" ]]; then
  echo "ok: CONFIG_DIR exists: ${CONFIG_DIR:-/home/pi/gosrc/src/EtherCAT/configs}"
else
  echo "warn: CONFIG_DIR missing: ${CONFIG_DIR:-/home/pi/gosrc/src/EtherCAT/configs}; hardware tests may need CONFIG_DIR override"
fi
echo

if [[ "$failures" -gt 0 ]]; then
  echo "Environment check failed with $failures required missing item(s)."
  exit 1
fi

echo "Environment check passed."
