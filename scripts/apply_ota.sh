#!/bin/bash
set -eu
ROOT="/mnt/app/jamun/updates"
META="$ROOT/current_update"
LOG_DIR="/mnt/app/jamun/log"
STATUS="$ROOT/status"
mkdir -p "$LOG_DIR"
exec >>"$LOG_DIR/ota.log" 2>&1
[ -f "$META" ] || { echo "current_update missing"; exit 1; }
grep -Eq '^State:[[:space:]]*(ready|backup_complete)$' "$META" || { echo "update is not ready"; exit 1; }
echo "$(date --iso-8601=seconds) OTA_HANDOFF restart requested"
printf 'state=RESTARTING\nprogress=70\nsafe_to_reboot=no\nmessage=Restarting to install update. Do not power off.\nupdated_at=%s\n' "$(date --iso-8601=seconds)" > "$STATUS.tmp"
chmod 777 "$STATUS.tmp"; sync "$STATUS.tmp"; mv "$STATUS.tmp" "$STATUS"
( sleep 1; systemctl restart jamun.service ) &
exit 0
