#!/bin/bash
set -eu

APP="/mnt/app/jamun"
RTC="$APP/updates"
BACKUPS="$APP/jamun_backups"
LOG_DIR="$APP/log"

mkdir -p "$APP" "$LOG_DIR" "$RTC/downloads" "$RTC/staging" "$BACKUPS" \
  "$APP/ngrok" "$APP/gm_codes" "$APP/settings"
chown -R root:root "$APP"
[ ! -f "$APP/version.txt" ] && printf 'unknown\n' > "$APP/version.txt"
if [ ! -e "$LOG_DIR/ota.log" ]; then install -o root -g root -m 0640 /dev/null "$LOG_DIR/ota.log"; fi
if [ ! -e "$LOG_DIR/log.log" ]; then install -o root -g root -m 0640 /dev/null "$LOG_DIR/log.log"; fi
if [ ! -e "$RTC/status" ]; then
  printf 'state=IDLE\nprogress=0\nsafe_to_reboot=yes\nmessage=No update is currently running. It is safe to reboot.\nupdated_at=%s\n' "$(date --iso-8601=seconds)" > "$RTC/status"
fi
# Explicit product requirement: the complete live Jamun tree is world-writable.
chmod -R 777 "$APP"
systemctl daemon-reload
systemctl enable jamun.service
echo "Jamun OTA permissions and service setup complete."
