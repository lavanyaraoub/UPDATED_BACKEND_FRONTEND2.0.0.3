#!/bin/bash
set -euo pipefail
STATE_DIR=/var/lib/jamun-ota
STATE="$STATE_DIR/state"
ROOT=/home/pi/rtc_updates
[ -s "$STATE" ] || exit 1
field() { sed -n "s/^$1=//p" "$STATE" | head -n1; }
status="$(field STATE)"; active="$(field ACTIVE_SLOT)"; previous="$(field PREVIOUS_SLOT)"
case "$active" in A|B) ;; *) exit 1;; esac
[ -x "/mnt/app/releases/slot-$active/jamun" ] || exit 1
case "$status" in
  TESTING|ROLLBACK_TESTING)
    event="$(field LAST_EVENT)"
    tmp="$STATE.tmp"
    {
      printf 'STATE=GOOD\nACTIVE_SLOT=%s\nPREVIOUS_SLOT=%s\n' "$active" "$previous"
      printf 'TARGET_SLOT=\nTARGET_VERSION=\nLAST_EVENT=%s\nUPDATED_AT=%s\n' "$event" "$(date --iso-8601=seconds)"
    } > "$tmp"
    chmod 0640 "$tmp"; sync "$tmp"; mv "$tmp" "$STATE"; sync "$STATE_DIR"
    rm -rf -- "$ROOT/downloads" "$ROOT/staging"
    rm -f -- "$ROOT/current_update" "$ROOT/update.lock"
    mkdir -p "$ROOT/downloads" "$ROOT/staging"
    ;;
  GOOD) exit 0;;
  *) exit 1;;
esac
