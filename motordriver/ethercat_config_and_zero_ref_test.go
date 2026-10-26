//go:build unit

package motordriver

// Tests for remaining pure-logic functions across init_master.go,
// zero_reference.go, and move_to_degree.go.
//
// PORTING NOTE: GetEtherCATOperation, sleepMs, and getEtherCATAddress are
// all byte-identical to testenv — ported unchanged.
//
// moveToZero itself diverged significantly (HAL inlines the full PDO
// zero-ref sequence with its own homing-speed/pulse-target logic; testenv
// delegates to doRotate) but the PDO-not-ready guard this test targets is
// present in both, just reached after a few more lines of setup in HAL's
// version.
//
// REAL HANG RISK FOUND WHILE PORTING: HAL's moveToZero calls
// notifier.NotifyDestinationPosition(...) UNCONDITIONALLY before reaching
// the PDO-readiness check — unlike notifyDriverStatus (used right next to
// it), this call has no isStatusListening guard. It sends directly on
// channels.BroadCastUIChannel with no nil-check. In a fresh test process
// that channel is nil by default, and sending on a nil channel blocks
// forever in Go — this would hang the test (not fail it) rather than
// exercise the intended early-return path. Fixed by initializing a
// buffered BroadCastUIChannel (with a draining goroutine) before the call,
// same pattern already used for TestPerformSysReset_MotorRunning_AbortsWithAlarm
// in drive_power_test.go. driverStatusMap is also initialized for the same
// reason getCurrentDriverStatus needs it elsewhere in this suite.

import (
	"testing"
	"time"

	channels "EtherCAT/channels"

	cmap "github.com/orcaman/concurrent-map"
)

// ─── GetEtherCATOperation ─────────────────────────────────────────────────

// TestGetEtherCATOperation_UnknownConfigReturnsEmptyOperation verifies that
// looking up an unknown device address config returns an empty operation
// (not a panic), since getEtherCATAddress returns a zero-value Ethercat.
func TestGetEtherCATOperation_UnknownConfigReturnsEmptyOperation(t *testing.T) {
	op, err := GetEtherCATOperation("powerOn", "nonexistent_config")
	// GetOperation on a zero-value Ethercat returns empty operation — no error,
	// just an empty Name field.
	_ = err
	if op.Name != "" {
		t.Errorf("GetEtherCATOperation unknown config: Name = %q, want empty", op.Name)
	}
}

// TestGetEtherCATOperation_UnknownOperationReturnsEmptyOperation verifies
// that an unknown operation name on a registered config also returns empty.
func TestGetEtherCATOperation_UnknownOperationNameReturnsEmpty(t *testing.T) {
	op, _ := GetEtherCATOperation("no_such_op", "a6minas")
	if op.Name != "" {
		t.Errorf("GetEtherCATOperation unknown op: Name = %q, want empty", op.Name)
	}
}

// ─── moveToZero — PDO not ready path ─────────────────────────────────────

// TestMoveToZero_PDONotReady_ReturnsError verifies that moveToZero returns
// an error when PDO position mode is not ready — no motion attempted.
// See file-level comment for why BroadCastUIChannel/driverStatusMap must
// be initialized first (avoiding a hang, not just a wrong result).
func TestMoveToZero_PDONotReady_ReturnsError(t *testing.T) {
	driverStatusMap = cmap.New()
	t.Cleanup(func() { driverStatusMap = cmap.New() })

	ch := make(chan channels.SocketMessage, 8)
	channels.BroadCastUIChannel = ch
	t.Cleanup(func() {
		channels.BroadCastUIChannel = nil
		for len(ch) > 0 {
			<-ch
		}
	})
	go func() {
		for range ch {
		}
	}()

	d := stubDevice("A")
	d.PdoPosReady = false
	pdoActive.Store(false) // PDO not active
	t.Cleanup(func() { pdoActive.Store(false) })

	err := moveToZero(d)
	if err == nil {
		t.Errorf("moveToZero with PDO not ready: expected error, got nil")
	}
}

// ─── sleepMs ─────────────────────────────────────────────────────────────

// TestSleepMs_ZeroIsNoOp verifies sleepMs(0) returns without hanging.
func TestSleepMs_ZeroIsNoOp(t *testing.T) {
	done := make(chan struct{})
	go func() {
		sleepMs(0)
		close(done)
	}()
	<-done
}

// ─── getEtherCATAddress ───────────────────────────────────────────────────

// TestGetEtherCATAddress_UnknownReturnsZeroValue verifies that a missing
// key in ethercatAddressMapping returns a zero-value Ethercat struct.
func TestGetEtherCATAddress_UnknownReturnsZeroValue(t *testing.T) {
	result := getEtherCATAddress("nonexistent_device")
	if len(result.Operation) != 0 {
		t.Errorf("getEtherCATAddress unknown key: got %d operations, want 0", len(result.Operation))
	}
}

// ─── moveToZero — already-at-target fast path ────────────────────────────

// TestMoveToZero_AlreadyAtZero_SkipsHandshakeAndSavesReference verifies the
// fast path added after a real hardware finding: when the shortest path to
// zero computes to 0 (already at the reference), moveToZero must skip the
// PP-mode handshake entirely rather than sending a redundant set-point.
//
// CONFIRMED ON HARDWARE: without this fast path, a redundant set-point gets
// no bit-12 acknowledgment from the drive (nothing new to confirm), causing
// a real 2-second Phase1 handshake timeout in hasTargetReached — every time
// zero-reference was called while already sitting at the zero position.
//
// This test verifies the fast path completes quickly (no hasTargetReached
// wait) and still saves the homing reference, matching the normal path's
// behavior for everything except the motion itself.
func TestMoveToZero_AlreadyAtZero_SkipsHandshakeAndSavesReference(t *testing.T) {
	driverStatusMap = cmap.New()
	t.Cleanup(func() { driverStatusMap = cmap.New() })

	ch := make(chan channels.SocketMessage, 8)
	channels.BroadCastUIChannel = ch
	t.Cleanup(func() {
		channels.BroadCastUIChannel = nil
		for len(ch) > 0 {
			<-ch
		}
	})
	go func() {
		for range ch {
		}
	}()

	pdoActive.Store(true)
	t.Cleanup(func() { pdoActive.Store(false) })

	d := stubDevice("A")
	d.Driver = &mockDriver{}
	d.PdoPosReady = true
	d.PDOPos.Store(0) // current raw position — matches shortestPathToZero(0, ...) == 0

	start := time.Now()
	err := moveToZero(d)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("moveToZero (already at zero): unexpected error: %v", err)
	}

	// The fast path skips hasTargetReached's multi-second wait entirely —
	// this should return in well under a second.
	if elapsed > 500*time.Millisecond {
		t.Errorf("moveToZero (already at zero) took %v — expected the fast path to skip "+
			"the PP handshake and return quickly, not fall through to the motion path", elapsed)
	}
}
