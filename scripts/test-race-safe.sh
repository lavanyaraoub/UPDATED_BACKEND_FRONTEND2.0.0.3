#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

PACKAGES=(
  ./helper/...
  ./configparser/...
  ./datatypes/...
  ./executors/...
)

echo "== Race detector: safe package slice =="
echo "Repo: $ROOT_DIR"
echo "Packages: ${PACKAGES[*]}"
echo

go test -race -tags=unit "${PACKAGES[@]}" -count=1
