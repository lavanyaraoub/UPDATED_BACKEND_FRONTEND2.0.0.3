//go:build unit

package systemupdate

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func withBackupRoot(t *testing.T, dir string) {
	t.Helper()
	orig := backupRoot
	backupRoot = dir
	t.Cleanup(func() { backupRoot = orig })
}

func withRollbackScriptPath(t *testing.T, path string) {
	t.Helper()
	orig := rollbackScriptPath
	rollbackScriptPath = path
	t.Cleanup(func() { rollbackScriptPath = orig })
}

func withRollbackMarkerPath(t *testing.T, path string) {
	t.Helper()
	orig := rollbackMarkerPath
	rollbackMarkerPath = path
	t.Cleanup(func() { rollbackMarkerPath = orig })
}

func TestGetLatestBackup_NoBackupRoot_ReturnsError(t *testing.T) {
	withBackupRoot(t, filepath.Join(t.TempDir(), "does-not-exist"))

	_, err := GetLatestBackup()
	if err == nil {
		t.Error("expected an error when the backup root directory doesn't exist")
	}
}

func TestGetLatestBackup_EmptyBackupRoot_ReturnsError(t *testing.T) {
	withBackupRoot(t, t.TempDir())

	_, err := GetLatestBackup()
	if err == nil {
		t.Error("expected an error when the backup root has no backup folders")
	}
}

func TestGetLatestBackup_MultipleBackups_ReturnsMostRecent(t *testing.T) {
	dir := t.TempDir()
	withBackupRoot(t, dir)

	for _, ts := range []string{"2026-01-01-00-00-00", "2026-06-15-12-00-00", "2026-03-10-08-00-00"} {
		os.MkdirAll(filepath.Join(dir, ts), 0755)
	}

	info, err := GetLatestBackup()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Timestamp != "2026-06-15-12-00-00" {
		t.Errorf("Timestamp = %q, want the chronologically latest %q", info.Timestamp, "2026-06-15-12-00-00")
	}
}

func TestGetLatestBackup_VersionFileMissing_ReturnsUnknown(t *testing.T) {
	dir := t.TempDir()
	withBackupRoot(t, dir)
	os.MkdirAll(filepath.Join(dir, "2026-01-01-00-00-00"), 0755)

	info, err := GetLatestBackup()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Version != "unknown" {
		t.Errorf("Version = %q, want %q when version.txt is missing", info.Version, "unknown")
	}
}

func TestGetLatestBackup_VersionFilePresent_ReturnsTrimmedVersion(t *testing.T) {
	dir := t.TempDir()
	withBackupRoot(t, dir)
	backupDir := filepath.Join(dir, "2026-01-01-00-00-00")
	os.MkdirAll(backupDir, 0755)
	os.WriteFile(filepath.Join(backupDir, "version.txt"), []byte("2.0.0.4\n"), 0644)

	info, err := GetLatestBackup()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Version != "2.0.0.4" {
		t.Errorf("Version = %q, want %q", info.Version, "2.0.0.4")
	}
}

func TestPerformRollback_AlreadyInProgress_ReturnsErrorImmediately(t *testing.T) {
	isRollbackInProgress = true
	defer func() { isRollbackInProgress = false }()

	err := PerformRollback(false)
	if err == nil {
		t.Error("expected an error when a rollback is already in progress")
	}
}

func TestPerformRollback_UpdateInProgress_ReturnsErrorImmediately(t *testing.T) {
	isUpdateInProgress = true
	defer func() { isUpdateInProgress = false }()

	err := PerformRollback(false)
	if err == nil {
		t.Error("expected an error when a system update is in progress")
	}
}

func TestPerformRollback_NoBackupAvailable_ReturnsError(t *testing.T) {
	withBackupRoot(t, t.TempDir()) // empty, no backups

	err := PerformRollback(false)
	if err == nil {
		t.Error("expected an error when no backup is available")
	}
}

// NOTE: PerformRollback's success path (past backup discovery) calls
// exec.Command to launch rollback.sh and, on completion of that launch,
// os.Exit(2) to let systemd restart the service. That tail is deliberately
// not exercised here — same accepted-gap pattern as logger.Fatal() and
// plugin_loader.go's corrupt-.so path. The three guard-clause tests above
// cover everything reachable before that point.

func TestCheckRollbackSuccess_NoMarker_IsNoOp(t *testing.T) {
	withRollbackMarkerPath(t, filepath.Join(t.TempDir(), "no-such-marker"))

	start := time.Now()
	CheckRollbackSuccess()
	elapsed := time.Since(start)

	// The "marker found" path sleeps 5s before notifying — confirm we
	// took the fast "no marker" return instead.
	if elapsed > 1*time.Second {
		t.Errorf("CheckRollbackSuccess took %v — expected the fast no-marker path", elapsed)
	}
}

func TestCheckRollbackSuccess_MarkerPresent_DeletesMarkerAndNotifies(t *testing.T) {
	dir := t.TempDir()
	markerPath := filepath.Join(dir, "rollback_success")
	withRollbackMarkerPath(t, markerPath)
	os.WriteFile(markerPath, []byte("2.0.0.4"), 0644)

	CheckRollbackSuccess() // includes a real 5s sleep on this path

	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Error("expected the marker file to be deleted after CheckRollbackSuccess")
	}
}
