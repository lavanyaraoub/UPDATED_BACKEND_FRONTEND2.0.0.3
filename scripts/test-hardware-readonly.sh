#!/usr/bin/env bash
# test-hardware-readonly.sh — Tiers 1-4: drive parameters, settings, REST API,
# program compilation. No motion. Safe to run while the motor is loaded.
set -euo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"
ETHERCAT_CLI="${ETHERCAT_CLI:-ethercat}"
ETHERCAT_IFACE="${ETHERCAT_IFACE:-end0}"
HARDWARE_COMMAND_TIMEOUT="${HARDWARE_COMMAND_TIMEOUT:-5s}"
ETHERCAT_SLAVE_POSITION="${ETHERCAT_SLAVE_POSITION:-0}"
EXPECTED_SLAVE_COUNT="${EXPECTED_SLAVE_COUNT:-1}"
EXPECTED_ETHERCAT_VENDOR_ID="${EXPECTED_ETHERCAT_VENDOR_ID:-0x0000066f}"
EXPECTED_ETHERCAT_PRODUCT_CODE="${EXPECTED_ETHERCAT_PRODUCT_CODE:-0x60380008}"
EXPECTED_ETHERCAT_REVISION_NUMBER="${EXPECTED_ETHERCAT_REVISION_NUMBER:-0x00010000}"
REQUIRE_CIA402_NO_ERROR="${REQUIRE_CIA402_NO_ERROR:-1}"
MOTION_REST_BASE_URL="${MOTION_REST_BASE_URL:-http://localhost:5000}"

echo "== EtherCAT read-only hardware smoke tests =="
echo "Repo: $REPO"
echo "EtherCAT CLI: $ETHERCAT_CLI"
echo "EtherCAT interface: $ETHERCAT_IFACE"
echo "Command timeout: $HARDWARE_COMMAND_TIMEOUT"
echo "Config dir: $REPO/configs"
echo "Required config files: a6minas.yml"
echo "Expected slave count: $EXPECTED_SLAVE_COUNT"
echo "SDO slave position: $ETHERCAT_SLAVE_POSITION"
echo "SDO identity expectations:"
echo "  Vendor ID: $EXPECTED_ETHERCAT_VENDOR_ID"
echo "  Product code: $EXPECTED_ETHERCAT_PRODUCT_CODE"
echo "  Revision number: $EXPECTED_ETHERCAT_REVISION_NUMBER"
echo ""

export ALLOW_HARDWARE_TESTS=1
export CONFIG_DIR="$REPO/configs"
export ETHERCAT_CLI HARDWARE_COMMAND_TIMEOUT
export ETHERCAT_INTERFACE="$ETHERCAT_IFACE"
export ETHERCAT_SLAVE_POSITION EXPECTED_SLAVE_COUNT
export EXPECTED_ETHERCAT_VENDOR_ID EXPECTED_ETHERCAT_PRODUCT_CODE EXPECTED_ETHERCAT_REVISION_NUMBER
export REQUIRE_CIA402_NO_ERROR MOTION_REST_BASE_URL

cd "$REPO"
go test -tags=hardware ./hardware/... -v \
  -run 'TestHardwareSmoke|TestHardwareCIA402|TestHardwareSlaveIdentity|TestHardwareMaster|TestDriveParam|TestSettings|TestREST|TestCompile' \
  -count=1 -timeout 120s