#!/bin/bash
set -eu

APP=/mnt/app/jamun
SERVICE=/etc/systemd/system/jamun.service

grep -q '^ExecStart=.*/mnt/app/jamun/jamun$' "$SERVICE" || {
  echo "FAIL: systemd does not start /mnt/app/jamun/jamun"
  exit 1
}
grep -q '^User=root$' "$SERVICE" || { echo "FAIL: service User is not root"; exit 1; }
grep -q '^Group=root$' "$SERVICE" || { echo "FAIL: service Group is not root"; exit 1; }

if grep -RInE 'install.*jamun\.service|cp.*jamun\.service.*/etc/systemd|jamun version\.txt jamun\.service' "$APP/scripts"; then
  echo "FAIL: an OTA/RB script can replace jamun.service"
  exit 1
fi

echo "PASS: service is external and OTA/RB cannot replace it"
systemctl show jamun.service -p User -p Group -p WorkingDirectory -p ExecStart -p ExecStartPre
