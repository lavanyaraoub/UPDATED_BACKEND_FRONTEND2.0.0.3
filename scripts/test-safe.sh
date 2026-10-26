#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

echo "== EtherCAT safe test validation =="
echo "Repo: $ROOT_DIR"
echo

echo "== Checking Go toolchain =="
go version
echo

echo "== Downloading Go modules =="
go mod download
echo

echo "== Running safe unit + integration suite =="
make test-all-safe
echo

echo "== Running coverage suite =="
make test-coverage
echo

if [ -d vendor ]; then
  echo "== Running vendor/offline integration suite =="
  go test -mod=vendor -tags=integration \
    ./executors \
    ./configparser \
    ./commands/moveRotary \
    ./restapi \
    -count=1
  echo
else
  echo "== Skipping vendor/offline integration suite =="
  echo "vendor/ directory not found."
  echo "Run 'go mod vendor' if you want offline validation."
  echo
fi

echo "== Safe test validation completed successfully =="
echo "Coverage files:"
echo "  coverage-unit.out"
echo "  coverage-integration.out"
echo
echo "Open reports with:"
echo "  go tool cover -html=coverage-unit.out"
echo "  go tool cover -html=coverage-integration.out"