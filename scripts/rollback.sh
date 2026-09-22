#!/bin/bash
set -eu
BACKUP="$1"
ROOT="/mnt/app/jamun/updates"
PENDING="$ROOT/pending_rollback"
LOG_DIR="/mnt/app/jamun/log"
STATUS="$ROOT/status"
mkdir -p "$ROOT" "$LOG_DIR"
exec >>"$LOG_DIR/ota.log" 2>&1
[ -d "$BACKUP/files" ] && [ -s "$BACKUP/manifest.tsv" ] || { echo "invalid verified backup: $BACKUP"; exit 1; }
grep -qx 'STATUS=verified' "$BACKUP/backup.meta" || { echo "backup is not verified"; exit 1; }
expected_manifest_sha="$(sed -n 's/^MANIFEST_SHA256=//p' "$BACKUP/backup.meta" | head -n1)"
[ "${#expected_manifest_sha}" -eq 64 ] || { echo "backup manifest SHA-256 missing"; exit 1; }
actual_manifest_sha="$(sha256sum "$BACKUP/manifest.tsv" | awk '{print $1}')"
[ "$actual_manifest_sha" = "$expected_manifest_sha" ] || { echo "backup manifest SHA-256 mismatch"; exit 1; }
printf '%s\n' "$BACKUP" > "$PENDING.tmp"
chmod 0640 "$PENDING.tmp"
sync "$PENDING.tmp"
mv "$PENDING.tmp" "$PENDING"
echo "$(date --iso-8601=seconds) ROLLBACK_HANDOFF backup=$BACKUP"
printf 'state=ROLLBACK_REQUESTED\nprogress=10\nsafe_to_reboot=no\nmessage=Restoring previous version. Do not power off.\nupdated_at=%s\n' "$(date --iso-8601=seconds)" > "$STATUS.tmp"
chmod 777 "$STATUS.tmp"; sync "$STATUS.tmp"; mv "$STATUS.tmp" "$STATUS"
( sleep 1; systemctl restart jamun.service ) &
exit 0
