#!/bin/bash
set -u

ROOT="/mnt/app/jamun/updates"
DST="/mnt/app/jamun"
BACKUP_ROOT="$DST/jamun_backups"
META="$ROOT/current_update"
PENDING_ROLLBACK="$ROOT/pending_rollback"
LOCK="$ROOT/update.lock"
LOG_DIR="$DST/log"
LOG="$LOG_DIR/ota.log"
STATUS="$ROOT/status"
REPLACEABLE=(
  commands configs scripts www_v2
  ethercatinterface.c ethercatinterface.h ethercatinterface.o
  libethercatinterface.so jamun version.txt
)

mkdir -p "$ROOT" "$BACKUP_ROOT" "$LOG_DIR"
touch "$LOG"
chmod 0640 "$LOG"
exec 3>>"$LOG"
exec >>"$LOG" 2>&1

log() { printf '%s %s\n' "$(date --iso-8601=seconds)" "$*" >&3; }
write_status() {
  local state="$1" message="$2" progress="${3:-0}" safe="no" tmp="$STATUS.tmp"
  case "$state" in SUCCESS|ROLLBACK_COMPLETE) safe=yes;; esac
  printf 'state=%s\nprogress=%s\nsafe_to_reboot=%s\nmessage=%s\nupdated_at=%s\n' \
    "$state" "$progress" "$safe" "$message" "$(date --iso-8601=seconds)" > "$tmp"
  chmod 777 "$tmp"; sync "$tmp"; mv "$tmp" "$STATUS"; sync "$ROOT"
}
field() { sed -n "s/^$1:[[:space:]]*//p" "$META" | head -n1; }

sha256_file() { sha256sum "$1" | awk '{print $1}'; }

# Verify every file in a deterministic staging manifest and reject both
# missing and unlisted files. Format: SHA256<TAB>SIZE<TAB>relative/path.
verify_stage_manifest() {
  local files="$1" manifest="$2" rel expected_size expected_sha path listed actual
  [ -d "$files" ] && [ -s "$manifest" ] || return 1
  listed=0
  while IFS=$'\t' read -r expected_sha expected_size rel; do
    [ -n "$rel" ] && [ "${#expected_sha}" -eq 64 ] || return 1
    case "$rel" in /*|../*|*/../*|*$'\t'*|*$'\n'*) return 1;; esac
    path="$files/$rel"
    [ -f "$path" ] || { log "STAGING_FILE_MISSING file=$rel"; return 1; }
    [ "$(stat -c '%s' "$path")" = "$expected_size" ] || { log "STAGING_SIZE_MISMATCH file=$rel"; return 1; }
    [ "$(sha256_file "$path")" = "$expected_sha" ] || { log "STAGING_SHA256_MISMATCH file=$rel"; return 1; }
    listed=$((listed + 1))
  done < "$manifest"
  actual="$(find "$files" -type f | wc -l)"
  [ "$listed" -eq "$actual" ] || { log "STAGING_FILE_COUNT_MISMATCH manifest=$listed actual=$actual"; return 1; }
}

verify_staged_transaction() {
  local archive manifest expected_archive_sha expected_manifest_sha expected_size
  archive="$(field ArchivePath)"
  manifest="$(field StagingManifest)"
  expected_archive_sha="$(field SHA256)"
  expected_manifest_sha="$(field StagingSHA256)"
  expected_size="$(field Size)"
  [ -f "$archive" ] && [ -s "$manifest" ] || { log "OTA_SEAL_MISSING"; return 1; }
  [ "$(stat -c '%s' "$archive")" = "$expected_size" ] || { log "ARCHIVE_SIZE_MISMATCH"; return 1; }
  [ "$(sha256_file "$archive")" = "$expected_archive_sha" ] || { log "ARCHIVE_SHA256_MISMATCH"; return 1; }
  [ "$(sha256_file "$manifest")" = "$expected_manifest_sha" ] || { log "STAGING_MANIFEST_SHA256_MISMATCH"; return 1; }
  verify_stage_manifest "$stage" "$manifest" || return 1
  log "OTA_SHA256_VERIFIED archive=$expected_archive_sha staging_manifest=$expected_manifest_sha"
}

# Remove only transient OTA workspace data.  Never point this function at the
# application, backup, or log directories.  Rollback writes one short-lived
# success marker after this reset; the newly started Jamun consumes it.
reset_ota_workspace() {
  [ "$ROOT" = "/mnt/app/jamun/updates" ] || {
    log "REFUSING_CLEANUP unexpected_root=$ROOT"
    return 1
  }

  # The root path is fixed and checked above. Remove every transaction,
  # download, staging and stale marker entry left by the completed operation.
  # Backups and logs live outside ROOT, so this cannot remove either of them.
  find "$ROOT" -mindepth 1 -maxdepth 1 -exec rm -rf -- {} + || return 1
  mkdir -p "$ROOT/downloads" "$ROOT/staging" || return 1
  chown root:root "$ROOT" "$ROOT/downloads" "$ROOT/staging" || return 1
  chmod 0750 "$ROOT" "$ROOT/downloads" "$ROOT/staging" || return 1
  sync "$ROOT"
}

set_field() {
  local key="$1" value="$2" tmp="${META}.tmp"
  awk -v k="$key" -v v="$value" 'BEGIN{done=0} index($0,k ":")==1 {print k ": " v; done=1; next} {print} END{if(!done) print k ": " v}' "$META" > "$tmp" || return 1
  chmod 0640 "$tmp" && sync "$tmp" && mv "$tmp" "$META" && sync "$ROOT"
}

make_manifest() {
  local files="$1" manifest="$2" rel size sha mode uid gid
  : > "$manifest"
  while IFS= read -r -d '' path; do
    rel="${path#$files/}"
    case "$rel" in *$'\t'*|*$'\n'*) log "Invalid backup filename: $rel"; return 1;; esac
    size="$(stat -c '%s' "$path")" || return 1
    sha="$(sha256sum "$path" | awk '{print $1}')" || return 1
    mode="$(stat -c '%a' "$path")" || return 1
    uid="$(stat -c '%u' "$path")" || return 1
    gid="$(stat -c '%g' "$path")" || return 1
    printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$rel" "$size" "$sha" "$mode" "$uid" "$gid" >> "$manifest"
  done < <(find "$files" -type f -print0 | sort -z)
  sync "$manifest"
}

verify_manifest() {
  local files="$1" manifest="$2" rel expected_size expected_sha mode uid gid path listed actual
  [ -d "$files" ] && [ -s "$manifest" ] || return 1
  listed=0
  while IFS=$'\t' read -r rel expected_size expected_sha mode uid gid; do
    [ -n "$rel" ] && [ "${#expected_sha}" -eq 64 ] || return 1
    case "$rel" in /*|../*|*/../*|*$'\t'*|*$'\n'*) return 1;; esac
    path="$files/$rel"
    [ -f "$path" ] || { log "Manifest file missing: $rel"; return 1; }
    [ "$(stat -c '%s' "$path")" = "$expected_size" ] || { log "Size mismatch: $rel"; return 1; }
    [ "$(sha256sum "$path" | awk '{print $1}')" = "$expected_sha" ] || { log "SHA mismatch: $rel"; return 1; }
    listed=$((listed + 1))
  done < "$manifest"
  actual="$(find "$files" -type f | wc -l)"
  [ "$listed" -eq "$actual" ] || { log "BACKUP_FILE_COUNT_MISMATCH manifest=$listed actual=$actual"; return 1; }
}

verify_backup_seal() {
  local backup="$1" recorded actual
  [ -s "$backup/backup.meta" ] || return 1
  recorded="$(sed -n 's/^MANIFEST_SHA256=//p' "$backup/backup.meta" | head -n1)"
  [ "${#recorded}" -eq 64 ] || { log "BACKUP_SEAL_MISSING backup=$backup"; return 1; }
  actual="$(sha256_file "$backup/manifest.tsv")" || return 1
  [ "$actual" = "$recorded" ] || { log "BACKUP_MANIFEST_SHA256_MISMATCH backup=$backup"; return 1; }
  verify_manifest "$backup/files" "$backup/manifest.tsv"
}

verify_content_tree() {
  local src="$1" dst="$2" src_file rel dst_file
  while IFS= read -r -d '' src_file; do
    rel="${src_file#$src/}"
    dst_file="$dst/$rel"
    [ -f "$dst_file" ] || return 1
    [ "$(stat -c '%s' "$src_file")" = "$(stat -c '%s' "$dst_file")" ] || return 1
    [ "$(sha256sum "$src_file" | awk '{print $1}')" = "$(sha256sum "$dst_file" | awk '{print $1}')" ] || return 1
  done < <(find "$src" -type f -print0)
}

rotate_live_log() {
  local reason="$1" log_dir="$DST/log" current="$DST/log/log.log" stamp
  stamp="$(date '+%Y-%m-%d_%H-%M-%S')"
  mkdir -p "$log_dir"
  if [ -s "$current" ]; then mv "$current" "$log_dir/log_${stamp}_${reason}.log"; else rm -f "$current"; fi
  install -o root -g root -m 0640 /dev/null "$current"
  sync "$log_dir"
}

merge_saved_logs() {
  local saved_dir="$1" reason="$2" src name target stamp
  [ -d "$saved_dir" ] || return 0
  mkdir -p "$DST/log"
  stamp="$(date '+%Y-%m-%d_%H-%M-%S')"
  while IFS= read -r -d '' src; do
    name="${src#$saved_dir/}"
    [ "$name" = "log.log" ] && name="log_${stamp}_${reason}.log"
    target="$DST/log/$name"
    if [ -e "$target" ]; then
      target="$DST/log/${name%.*}_${stamp}_${reason}.${name##*.}"
    fi
    mkdir -p "$(dirname "$target")"
    cp -a "$src" "$target" || return 1
  done < <(find "$saved_dir" -type f -print0)
}

restore_backup() {
  local backup="$1" reason="$2" restore_tmp="${DST}.restore" item
  verify_backup_seal "$backup" || { log "Backup verification failed: $backup"; return 1; }
  rm -rf "$restore_tmp"
  mkdir -p "$restore_tmp"
  cp -a "$backup/files/." "$restore_tmp/" || return 1
  verify_manifest "$restore_tmp" "$backup/manifest.tsv" || return 1
  # Restore only the allowlisted application payload. Protected customer and
  # device data is never deleted or copied from an OTA backup.
  for item in "${REPLACEABLE[@]}"; do
    [ -e "$restore_tmp/$item" ] || continue
    rm -rf -- "$DST/$item" || return 1
    cp -a "$restore_tmp/$item" "$DST/$item" || return 1
  done
  rm -rf "$restore_tmp"
  rotate_live_log "$reason"
  [ -x "$DST/jamun" ] || return 1
  chmod -R 777 "$DST" || return 1
  # Verify every restored allowlisted file against the sealed backup.
  while IFS=$'\t' read -r rel expected_size expected_sha mode uid gid; do
    case "$rel" in
      commands/*|configs/*|scripts/*|www_v2/*|ethercatinterface.c|ethercatinterface.h|ethercatinterface.o|libethercatinterface.so|jamun|version.txt) ;;
      *) continue;;
    esac
    [ -f "$DST/$rel" ] || { log "ROLLBACK_FILE_MISSING file=$rel"; return 1; }
    [ "$(stat -c '%s' "$DST/$rel")" = "$expected_size" ] || { log "ROLLBACK_SIZE_MISMATCH file=$rel"; return 1; }
    [ "$(sha256_file "$DST/$rel")" = "$expected_sha" ] || { log "ROLLBACK_SHA256_MISMATCH file=$rel"; return 1; }
  done < "$backup/manifest.tsv"
  log "ROLLBACK_SHA256_VERIFIED backup=$backup"
  sync
}

create_backup_once() {
  local existing version source_version stamp id partial final manifest_sha item
  existing="$(field Backup)"
  if [ -n "$existing" ]; then
    verify_backup_seal "$existing" || return 1
    printf '%s\n' "$existing"
    return 0
  fi
  version="$(field Version)"
  source_version="$(tr -d '[:space:]' < "$DST/version.txt" 2>/dev/null || printf unknown)"
  stamp="$(date '+%Y-%m-%d_%H-%M-%S')"
  id="${stamp}_pre_ota_${source_version}_to_${version}"
  partial="$BACKUP_ROOT/${id}.partial"
  final="$BACKUP_ROOT/$id"
  rm -rf "$partial"
  mkdir -p "$partial/files"
  log "Creating single recovery backup: $final"
  for item in "${REPLACEABLE[@]}"; do
    [ -e "$DST/$item" ] || continue
    cp -a "$DST/$item" "$partial/files/$item" || { rm -rf "$partial"; return 1; }
  done
  make_manifest "$partial/files" "$partial/manifest.tsv" || { rm -rf "$partial"; return 1; }
  verify_manifest "$partial/files" "$partial/manifest.tsv" || { rm -rf "$partial"; return 1; }
  manifest_sha="$(sha256_file "$partial/manifest.tsv")" || { rm -rf "$partial"; return 1; }
  cat > "$partial/backup.meta" <<EOF
BACKUP_ID=$id
CREATED_AT=$(date --iso-8601=seconds)
SOURCE_VERSION=$source_version
TARGET_VERSION=$version
REASON=pre_ota
STATUS=verified
MANIFEST_SHA256=$manifest_sha
EOF
  sync "$partial/backup.meta" "$partial/manifest.tsv"
  mv "$partial" "$final" || return 1
  sync "$BACKUP_ROOT"
  set_field Backup "$final" || return 1
  set_field State backup_complete || return 1
  log "BACKUP_SHA256_VERIFIED backup=$final manifest_sha256=$manifest_sha"
  printf '%s\n' "$final"
}

apply_staged_update() {
  local src="$1" backup="$2" item tmp
  set_field State installing || return 1
  write_status INSTALLING "Installing verified update. CRITICAL: do not power off or reboot." 75
  rotate_live_log pre_ota
  for item in "${REPLACEABLE[@]}"; do
    [ -e "$src/$item" ] || continue
    tmp="$DST/.${item}.ota-new"
    rm -rf -- "$tmp" || { restore_backup "$backup" ota_failed; return 1; }
    cp -a "$src/$item" "$tmp" || { rm -rf -- "$tmp"; restore_backup "$backup" ota_failed; return 1; }
    if [ -f "$src/$item" ]; then
      [ "$(stat -c '%s' "$src/$item")" = "$(stat -c '%s' "$tmp")" ] || { rm -f -- "$tmp"; restore_backup "$backup" ota_failed; return 1; }
      [ "$(sha256sum "$src/$item"|awk '{print $1}')" = "$(sha256sum "$tmp"|awk '{print $1}')" ] || { rm -f -- "$tmp"; restore_backup "$backup" ota_failed; return 1; }
    elif [ -d "$src/$item" ]; then
      verify_content_tree "$src/$item" "$tmp" || { rm -rf -- "$tmp"; restore_backup "$backup" ota_failed; return 1; }
    fi
    sync "$tmp" || { rm -rf -- "$tmp"; restore_backup "$backup" ota_failed; return 1; }
    rm -rf -- "$DST/$item" || { restore_backup "$backup" ota_failed; return 1; }
    mv "$tmp" "$DST/$item" || { restore_backup "$backup" ota_failed; return 1; }
    sync "$DST" || { restore_backup "$backup" ota_failed; return 1; }
  done
  chown -R root:root "$DST"
  chmod -R 777 "$DST"
  [ -x "$DST/jamun" ] || { restore_backup "$backup" ota_failed; return 1; }
  # Recheck each replacement against the sealed staging tree after chmod/chown.
  # SHA-256 is content-only, so intended permission normalization is allowed.
  verify_stage_manifest "$src" "$(field StagingManifest)" || { log "INSTALL_SHA256_MISMATCH"; restore_backup "$backup" ota_failed; return 1; }
  for item in "${REPLACEABLE[@]}"; do
    [ -e "$src/$item" ] || continue
    if [ -f "$src/$item" ]; then
      [ "$(sha256_file "$src/$item")" = "$(sha256_file "$DST/$item")" ] || { log "INSTALL_SHA256_MISMATCH item=$item"; restore_backup "$backup" ota_failed; return 1; }
    else
      verify_content_tree "$src/$item" "$DST/$item" || { log "INSTALL_TREE_SHA256_MISMATCH item=$item"; restore_backup "$backup" ota_failed; return 1; }
    fi
  done
  log "INSTALL_SHA256_VERIFIED version=$(field Version)"
  sync
}

log "PRE_START_BEGIN"

if [ -f "$PENDING_ROLLBACK" ]; then
  write_status RECOVERING "Restoring the previous working version. Do not power off." 25
  rollback="$(cat "$PENDING_ROLLBACK")"
  log "ROLLBACK_PENDING backup=$rollback"
  # Validate before changing the live tree. An invalid rollback request must
  # not trap a healthy customer device in systemd's restart loop.
  if ! verify_backup_seal "$rollback"; then
    mkdir -p "$ROOT/quarantine"
    mv "$PENDING_ROLLBACK" "$ROOT/quarantine/pending_rollback_$(date '+%Y-%m-%d_%H-%M-%S')" 2>/dev/null || rm -f "$PENDING_ROLLBACK"
    log "ROLLBACK_REJECTED_SHA256 current_install_preserved"
    if [ -x "$DST/jamun" ]; then
      exit 0
    fi
    log "ROLLBACK_REJECTED_AND_CURRENT_INSTALL_INVALID"
    exit 1
  fi
  if restore_backup "$rollback" pre_rollback; then
    version="$(tr -d '[:space:]' < "$DST/version.txt" 2>/dev/null || printf unknown)"
    reset_ota_workspace || { log "ROLLBACK_CLEANUP_FAILED"; exit 1; }
    log "ROLLBACK_COMPLETE version=$version"
    mkdir -p "$ROOT"
    write_status ROLLBACK_COMPLETE "Restore completed successfully. Previous version $version is running. It is now safe to reboot or power-cycle the device." 100
    exit 0
  fi
  log "ROLLBACK_FAILED"
  exit 1
fi

if [ ! -f "$META" ]; then
  [ -x "$DST/jamun" ] || { log "No pending transaction and Jamun is not executable"; exit 1; }
  log "NO_PENDING_UPDATE allowing Jamun start"
  exit 0
fi

state="$(field State)"
filename="$(field Name)"
backup="$(field Backup)"

# "installing" means live files may already be mixed. Recover from the
# recorded verified backup before considering stale-metadata cleanup.
if [ "$state" = "installing" ]; then
  write_status RECOVERING "Interrupted update detected. Restoring the verified backup. Do not power off." 20
  log "INTERRUPTED_INSTALL restoring recorded backup=$backup"
  if [ -n "$backup" ] && restore_backup "$backup" interrupted_ota; then
    reset_ota_workspace || { log "RECOVERY_CLEANUP_FAILED"; exit 1; }
    log "INTERRUPTED_INSTALL_RECOVERED allowing old Jamun start"
    mkdir -p "$ROOT"
    write_status ROLLBACK_COMPLETE "Interrupted update recovered successfully. The previous working version is running. It is now safe to reboot or power-cycle the device." 100
    exit 0
  fi
  log "INTERRUPTED_INSTALL_UNRECOVERABLE"
  exit 1
fi

# A stale or zero-byte current_update must not trap a healthy Jamun in a
# two-second systemd restart loop. Atomic metadata writes mean a valid
# transaction always has Name, Version, SHA256 and a recognised State. If
# these fields are invalid, no installation is allowed. Clear only the OTA
# workspace and start the existing executable; if the executable is damaged,
# keep the service stopped for safe manual recovery.
case "$state" in ready|backup_complete|installing) metadata_state_ok=1 ;; *) metadata_state_ok=0 ;; esac
if [ "$metadata_state_ok" -ne 1 ] || [ -z "$filename" ] || [ -z "$(field Version)" ] || [ -z "$(field SHA256)" ] || [ -z "$(field StagingSHA256)" ]; then
  log "INVALID_TRANSACTION_METADATA state=$state name=$filename"
  if [ -x "$DST/jamun" ]; then
    reset_ota_workspace || { log "INVALID_METADATA_CLEANUP_FAILED"; exit 1; }
    log "INVALID_TRANSACTION_CLEARED allowing existing Jamun start"
    exit 0
  fi
  log "INVALID_TRANSACTION_AND_JAMUN_NOT_EXECUTABLE"
  exit 1
fi

stage="$ROOT/staging/${filename%.tar.gz}"

case "$state" in ready|backup_complete) ;; *) log "Invalid OTA state: $state"; exit 1;; esac
[ -d "$stage" ] && [ -x "$stage/jamun" ] || {
  log "Verified staging missing: $stage"
  reset_ota_workspace || exit 1
  exit 0
}

if ! verify_staged_transaction; then
  log "OTA_REJECTED_SHA256 current_install_preserved"
  reset_ota_workspace || exit 1
  [ -x "$DST/jamun" ] && exit 0
  exit 1
fi

backup="$(create_backup_once)" || { log "BACKUP_FAILED"; rm -f "$LOCK"; exit 1; }
write_status BACKUP_VERIFIED "Recovery backup verified. Installing update; do not power off." 65
if apply_staged_update "$stage" "$backup"; then
  version="$(field Version)"
  reset_ota_workspace || { log "OTA_CLEANUP_FAILED"; exit 1; }
  log "OTA_COMPLETE version=$version backup=$backup"
  mkdir -p "$ROOT"
  write_status SUCCESS "Update completed successfully. Version $version is running. It is now safe to reboot or power-cycle the device." 100
  exit 0
fi

reset_ota_workspace || log "FAILED_OTA_CLEANUP_FAILED"
if [ -x "$DST/jamun" ]; then log "OTA_FAILED_ROLLBACK_COMPLETE allowing old Jamun start"; exit 0; fi
log "OTA_AND_ROLLBACK_FAILED"
exit 1
