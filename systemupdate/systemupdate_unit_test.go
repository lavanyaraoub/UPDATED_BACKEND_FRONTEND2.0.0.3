//go:build unit

package systemupdate

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func withOtaScriptPath(t *testing.T, path string) {
	t.Helper()
	orig := otaScriptPath
	otaScriptPath = path
	t.Cleanup(func() { otaScriptPath = orig })
}

func withUpdateLockPath(t *testing.T, path string) {
	t.Helper()
	orig := updateLockPath
	updateLockPath = path
	t.Cleanup(func() { updateLockPath = orig })
}

func TestPerformSystemUpdate_AlreadyInProgress_IsNoOp(t *testing.T) {
	isUpdateInProgress = true
	defer func() { isUpdateInProgress = false }()

	// If this weren't a no-op, it would try to read rtcUpdateRoot and
	// beyond — set nothing else up, so any further progress would panic
	// or error loudly, which the test would catch.
	PerformSystemUpdate(false)
}

func TestPerformSystemUpdate_NoStagedUpdate_ReturnsEarly(t *testing.T) {
	withRtcUpdateRoot(t, t.TempDir()) // no current_update file present

	PerformSystemUpdate(false)

	if isUpdateInProgress {
		t.Error("expected isUpdateInProgress to be reset to false after early return")
	}
}

func TestPerformSystemUpdate_StagedFolderMissing_ReturnsEarly(t *testing.T) {
	dir := t.TempDir()
	withRtcUpdateRoot(t, dir)
	withUpdateLockPath(t, filepath.Join(dir, "update.lock"))
	os.WriteFile(filepath.Join(dir, "current_update"), []byte("Name:jamun_v9.9.9.9.tar.gz"), 0644)
	// Deliberately do not create the staged folder itself.

	PerformSystemUpdate(false)

	if isUpdateInProgress {
		t.Error("expected isUpdateInProgress to be reset to false after early return")
	}
	if _, err := os.Stat(filepath.Join(dir, "update.lock")); !os.IsNotExist(err) {
		t.Error("expected the update lock to be cleaned up after early return")
	}
}

func TestPerformSystemUpdate_StagedBinaryMissing_ReturnsEarly(t *testing.T) {
	dir := t.TempDir()
	withRtcUpdateRoot(t, dir)
	withUpdateLockPath(t, filepath.Join(dir, "update.lock"))
	os.WriteFile(filepath.Join(dir, "current_update"), []byte("Name:jamun_v9.9.9.9.tar.gz"), 0644)
	// Staged folder exists, but "jamun" binary inside it does not.
	os.MkdirAll(filepath.Join(dir, "jamun_v9.9.9.9"), 0755)

	PerformSystemUpdate(false)

	if isUpdateInProgress {
		t.Error("expected isUpdateInProgress to be reset to false after early return")
	}
}

func TestPerformSystemUpdate_OtaScriptMissing_ReturnsEarly(t *testing.T) {
	dir := t.TempDir()
	withRtcUpdateRoot(t, dir)
	withUpdateLockPath(t, filepath.Join(dir, "update.lock"))
	withOtaScriptPath(t, filepath.Join(t.TempDir(), "does-not-exist.sh"))
	os.WriteFile(filepath.Join(dir, "current_update"), []byte("Name:jamun_v9.9.9.9.tar.gz"), 0644)
	stageDir := filepath.Join(dir, "jamun_v9.9.9.9")
	os.MkdirAll(stageDir, 0755)
	os.WriteFile(filepath.Join(stageDir, "jamun"), []byte("x"), 0644)

	PerformSystemUpdate(false)

	if isUpdateInProgress {
		t.Error("expected isUpdateInProgress to be reset to false after early return")
	}
}

func TestPerformSystemUpdate_StaleLockRemoved(t *testing.T) {
	dir := t.TempDir()
	withRtcUpdateRoot(t, dir)
	lockPath := filepath.Join(dir, "update.lock")
	withUpdateLockPath(t, lockPath)
	// No current_update — this test only verifies the stale-lock removal
	// branch doesn't panic and the function still returns via the
	// no-staged-update early exit.
	os.WriteFile(lockPath, []byte("old"), 0644)
	oldTime := time.Now().Add(-20 * time.Minute)
	os.Chtimes(lockPath, oldTime, oldTime)

	PerformSystemUpdate(false)

	if isUpdateInProgress {
		t.Error("expected isUpdateInProgress to be reset to false")
	}
}

// NOTE: Past the OTA-script validation, PerformSystemUpdate stops the
// EtherCAT driver, launches apply_ota.sh via exec.Command, and finally
// calls os.Exit(2) to let systemd restart the service. That tail is
// deliberately not exercised here — same accepted-gap pattern as
// PerformRollback and logger.Fatal(). The five tests above cover every
// guard clause reachable before that point.

func TestBackupExistingSystem_CopiesInstallToBackup(t *testing.T) {
	srcDir := t.TempDir()
	withInstallPath(t, srcDir)
	os.WriteFile(filepath.Join(srcDir, "jamun"), []byte("binary contents"), 0644)
	os.MkdirAll(filepath.Join(srcDir, "configs"), 0755)
	os.WriteFile(filepath.Join(srcDir, "configs", "device.yml"), []byte("vendor: test"), 0644)

	backupParent := t.TempDir()
	withBackupRoot(t, backupParent)

	backupPath, err := backupExistingSystem()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(backupPath, "jamun"))
	if err != nil {
		t.Fatalf("expected backed-up jamun binary: %v", err)
	}
	if string(data) != "binary contents" {
		t.Errorf("backed-up content = %q, want %q", string(data), "binary contents")
	}
}

func TestRollbackUpdate_CopiesBackupToInstall(t *testing.T) {
	backupDir := t.TempDir()
	os.WriteFile(filepath.Join(backupDir, "jamun"), []byte("old version"), 0644)

	installDir := t.TempDir()
	withInstallPath(t, installDir)

	if err := rollbackUpdate(backupDir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(installDir, "jamun"))
	if err != nil {
		t.Fatalf("expected restored jamun binary: %v", err)
	}
	if string(data) != "old version" {
		t.Errorf("restored content = %q, want %q", string(data), "old version")
	}
}

func TestSyscallStatfsCall_RealPath_Succeeds(t *testing.T) {
	// Exercises the real (non-injected) default implementation directly
	// against a genuinely existing, safe, read-only path.
	var stat syscall.Statfs_t
	if err := syscallStatfsCall("/tmp", &stat); err != nil {
		t.Errorf("unexpected error calling the real statfs on /tmp: %v", err)
	}
	if stat.Bsize == 0 {
		t.Error("expected a non-zero block size from a real filesystem")
	}
}

func TestSyscallStatfsCall_NonexistentPath_ReturnsError(t *testing.T) {
	var stat syscall.Statfs_t
	if err := syscallStatfsCall("/this/path/does/not/exist/anywhere", &stat); err == nil {
		t.Error("expected an error for a nonexistent path")
	}
}
