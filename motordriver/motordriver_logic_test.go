//go:build unit

package motordriver

// Tests for checkPotNotLimit's ACTUAL breach-detection paths.
//
// SCOPE NOTE: testenv's motordriver_logic_test.go (746 lines, 54 tests) was
// reviewed in full. The large majority duplicated scenarios already ported
// under different names in drive_rotation_test.go (cia402/getPos/
// shortestPathToZero), pdo_cyclic_task_test.go (PDOTickAge/PDOHealthy),
// drive_power_test.go (ResetDriver/applyClampIfSettingsChanged/StopSystem),
// and motordriver_lifecycle_test.go (reverseDir/breakOn/hasDeclamped/etc).
// Re-porting near-identical tests under new names would add file bloat
// without adding coverage.
//
// What was genuinely missing: every existing checkPotNotLimit test only
// covered "within limits" (false) or "limits disabled" (false) — none
// exercised an ACTUAL limit breach firing (true), or the "already latched,
// keep alarming" fast path. Those are the two most safety-relevant branches
// in the whole function and are ported here, verified against the exact
// current formula in poll_drive_position.go (confirmed identical to
// testenv's — this function was not part of the divergent set).

import (
	"testing"
	"time"

	channels "EtherCAT/channels"
	"EtherCAT/settings"
)

// withLimitTestSetup wires up the channels checkPotNotLimit's breach path
// writes to (statusnotifier.Alarm needs BroadCastUIChannel;
// channels.WriteCommandExecInput needs CommandExecInputChannel) so a real
// breach can be exercised without blocking.
func withLimitTestSetup(t *testing.T) {
	t.Helper()
	ch := make(chan channels.SocketMessage, 64)
	channels.BroadCastUIChannel = ch
	go func() {
		for range ch {
		}
	}()
	channels.CommandExecInputChannel = make(chan channels.CommandExecInput, 32)
	t.Cleanup(func() {
		channels.BroadCastUIChannel = nil
		isStatusListening.Store(false)
		for len(channels.CommandExecInputChannel) > 0 {
			<-channels.CommandExecInputChannel
		}
	})
}

// ─── Already-latched fast path ─────────────────────────────────────────────

func TestCheckPotNotLimit_AlreadyLatchedPOT_ReturnsTrueImmediately(t *testing.T) {
	withLimitTestSetup(t)

	d := stubDevice("A")
	ds := settings.DriverSettings{POT: 350, NOT: -10}
	// potNotExceeded=true means the latch already fired in a previous cycle —
	// function returns true immediately via the "keep alarming" branch,
	// without re-evaluating position at all.
	st := driverCurrentStatus{
		isMotorRunning: true,
		potNotExceeded: true,
		potExceeded:    true,
	}
	// currentPos is deliberately far from any limit — proves the latch
	// short-circuits position checking entirely.
	if !checkPotNotLimit(0, d, ds, st) {
		t.Error("checkPotNotLimit: potNotExceeded=true should return true regardless of position")
	}
}

func TestCheckPotNotLimit_AlreadyLatchedNOT_ReturnsTrueImmediately(t *testing.T) {
	withLimitTestSetup(t)

	d := stubDevice("A")
	ds := settings.DriverSettings{POT: 350, NOT: -10}
	st := driverCurrentStatus{
		isMotorRunning: true,
		potNotExceeded: true,
		potExceeded:    false, // false → NOT branch of the alarm message
	}
	if !checkPotNotLimit(0, d, ds, st) {
		t.Error("checkPotNotLimit: potNotExceeded=true (NOT variant) should return true")
	}
}

// ─── Actual breach detection ────────────────────────────────────────────────

func TestCheckPotNotLimit_POTBreachInBand_ReturnsTrue(t *testing.T) {
	withLimitTestSetup(t)
	pdoActive.Store(false) // FastPowerOff/StopJog error out cleanly (unknown SDO config) rather than touching cgo
	defer pdoActive.Store(false)

	d := stubDevice("A")
	d.Device.PotNotThreshold = 5.0
	ds := settings.DriverSettings{POT: 350, NOT: 0}
	st := driverCurrentStatus{
		isMotorRunning: true,
		direction:      1, // CW → POT check applies
		potNotExceeded: false,
	}

	// Band = [POT-threshold, POT+threshold*10] = [345, 400]. 352 is inside it.
	got := checkPotNotLimit(352, d, ds, st)
	if !got {
		t.Error("checkPotNotLimit: position within POT breach band should return true")
	}
}

func TestCheckPotNotLimit_NOTBreachInBand_ReturnsTrue(t *testing.T) {
	withLimitTestSetup(t)
	pdoActive.Store(false)
	defer pdoActive.Store(false)

	d := stubDevice("A")
	d.Device.PotNotThreshold = 5.0
	ds := settings.DriverSettings{POT: 0, NOT: -10} // effective NOT = 360-10 = 350
	st := driverCurrentStatus{
		isMotorRunning: true,
		direction:      -1, // CCW → NOT check applies
		potNotExceeded: false,
	}

	// Band = [not-threshold*10, not+threshold] = [300, 355]. 348 is inside it.
	got := checkPotNotLimit(348, d, ds, st)
	if !got {
		t.Error("checkPotNotLimit: position within NOT breach band should return true")
	}
}

func TestCheckPotNotLimit_POTBreachOutsideBand_ReturnsFalse(t *testing.T) {
	d := stubDevice("A")
	d.Device.PotNotThreshold = 1.0
	ds := settings.DriverSettings{POT: 350, NOT: 0}
	st := driverCurrentStatus{
		isMotorRunning: true,
		direction:      1,
	}

	// currentPos=200 is nowhere near the POT limit of 350 — band is [349, 360].
	got := checkPotNotLimit(200, d, ds, st)
	if got {
		t.Error("checkPotNotLimit: position far from limit should return false")
	}
}

// TestCheckPotNotLimit_NOTGuard_ZeroRawValueNeverBreaches pins the documented
// bug fix in poll_drive_position.go: the NOT check is guarded on the RAW UI
// value (driverSettings.NOT != 0), not the computed "not" value (360+NOT).
// Before the fix, NOT=0 (unconfigured) computed to "not=360", which is
// always > 0 — causing a false emergency stop every time the motor jogged
// CCW through the 360°/0° wrap, even with no NOT limit configured at all.
func TestCheckPotNotLimit_NOTGuard_ZeroRawValueNeverBreaches(t *testing.T) {
	d := stubDevice("A")
	d.Device.PotNotThreshold = 5.0
	ds := settings.DriverSettings{POT: 350, NOT: 0} // NOT unconfigured
	st := driverCurrentStatus{
		isMotorRunning: true,
		direction:      -1, // CCW — this is exactly the wrap-around scenario
	}

	// Position right at the wrap point, which would fall inside a computed
	// "not=360" band under the old (buggy) guard.
	got := checkPotNotLimit(358, d, ds, st)
	if got {
		t.Error("checkPotNotLimit: NOT=0 (unconfigured) must never breach, even near the 360° wrap")
	}
}

// ─── withUIChannel ────────────────────────────────────────────────────────────
// Sets up a buffered BroadCastUIChannel and drains it in a goroutine, then
// restores the original on cleanup. Required for handleErrCode and any test
// that reaches statusnotifier.Alarm/DriverError.

func withUIChannel(t *testing.T) {
	t.Helper()
	ch := make(chan channels.SocketMessage, 64)
	saved := channels.BroadCastUIChannel
	channels.BroadCastUIChannel = ch
	t.Cleanup(func() { channels.BroadCastUIChannel = saved })
	go func() {
		for range ch {
		}
	}()
}

// ─── cia402NextControlword — all CiA-402 state transitions ───────────────────

func TestCia402NextControlword_FaultBitSet_NoResetRequested_ReturnsZero(t *testing.T) {
	var opEnabled bool
	drv := &mockDriver{}
	if got := cia402NextControlword(0x0008, &opEnabled, false, drv); got != 0x0000 {
		t.Errorf("fault no reset: got 0x%04X want 0x0000", got)
	}
	if opEnabled {
		t.Error("opEnabled should be false on fault")
	}
}

func TestCia402NextControlword_FaultBitSet_ResetRequested_Returns0x0080(t *testing.T) {
	var opEnabled bool
	drv := &mockDriver{faultResetControlword: 0x0080}

	if got := cia402NextControlword(0x0008, &opEnabled, true, drv); got != 0x0080 {
		t.Errorf("fault+reset: got 0x%04X want 0x0080", got)
	}

	if opEnabled {
		t.Error("opEnabled should be false on fault reset")
	}
}

func TestCia402NextControlword_NotReadyOrDisabled_Returns0x0006(t *testing.T) {
	drv := &mockDriver{}
	for _, sw := range []uint16{0x0000, 0x0040} {
		var opEnabled bool
		if got := cia402NextControlword(sw, &opEnabled, false, drv); got != 0x0006 {
			t.Errorf("sw=0x%04X: got 0x%04X want 0x0006", sw, got)
		}
	}
}

func TestCia402NextControlword_ReadyToSwitchOn_Returns0x0007(t *testing.T) {
	var opEnabled bool
	drv := &mockDriver{}
	if got := cia402NextControlword(0x0021, &opEnabled, false, drv); got != 0x0007 {
		t.Errorf("ReadyToSwitchOn: got 0x%04X want 0x0007", got)
	}
}

func TestCia402NextControlword_SwitchedOn_Returns0x000F(t *testing.T) {
	var opEnabled bool
	drv := &mockDriver{}
	if got := cia402NextControlword(0x0023, &opEnabled, false, drv); got != 0x000F {
		t.Errorf("SwitchedOn: got 0x%04X want 0x000F", got)
	}
	if opEnabled {
		t.Error("opEnabled should be false in SwitchedOn")
	}
}

func TestCia402NextControlword_OperationEnabled_Returns0x000F_AndSetsOpEnabled(t *testing.T) {
	var opEnabled bool
	drv := &mockDriver{}
	if got := cia402NextControlword(0x0027, &opEnabled, false, drv); got != 0x000F {
		t.Errorf("OperationEnabled: got 0x%04X want 0x000F", got)
	}
	if !opEnabled {
		t.Error("opEnabled should be true in OperationEnabled")
	}
}

func TestCia402NextControlword_UnknownState_ReturnsZeroOrSix(t *testing.T) {
	var opEnabled bool
	drv := &mockDriver{}
	got := cia402NextControlword(0x00FF, &opEnabled, false, drv)
	if got != 0x0000 && got != 0x0006 {
		t.Errorf("unknown state: got 0x%04X want 0x0000 or 0x0006", got)
	}
	if opEnabled {
		t.Error("unknown state: opEnabled should be false")
	}
}

// ─── handleErrCode ────────────────────────────────────────────────────────────

func TestHandleErrCode_NewError_UpdatesLast(t *testing.T) {
	withUIChannel(t)
	last := 0
	handleErrCode(stubDevice("A"), 80, 0, &last)
	if last != 80 {
		t.Errorf("got %d want 80", last)
	}
}

func TestHandleErrCode_SameError_SuppressesRepeat(t *testing.T) {
	withUIChannel(t)
	last := 80
	handleErrCode(stubDevice("A"), 80, 0, &last)
	if last != 80 {
		t.Errorf("last changed to %d, want 80", last)
	}
}

func TestHandleErrCode_ErrorCleared_TransitionsToErrCodeCleared(t *testing.T) {
	withUIChannel(t)
	last := 80
	handleErrCode(stubDevice("A"), 0, 0, &last)
	if last != errCodeCleared {
		t.Errorf("got %d want errCodeCleared(%d)", last, errCodeCleared)
	}
}

func TestHandleErrCode_AlreadyCleared_IsNoOp(t *testing.T) {
	withUIChannel(t)
	last := errCodeCleared
	handleErrCode(stubDevice("A"), 0, 0, &last)
	if last != errCodeCleared {
		t.Errorf("last changed to %d", last)
	}
}

func TestHandleErrCode_ErrorChanges_UpdatesLast(t *testing.T) {
	withUIChannel(t)
	last := 80
	handleErrCode(stubDevice("A"), 88, 0, &last)
	if last != 88 {
		t.Errorf("got %d want 88", last)
	}
}

// ─── sleepMs ──────────────────────────────────────────────────────────────────

func TestSleepMs_ZeroDuration_ReturnsImmediately(t *testing.T) {
	done := make(chan struct{})
	go func() { sleepMs(0); close(done) }()
	select {
	case <-done:
	case <-time.After(50 * time.Millisecond):
		t.Error("sleepMs(0) blocked >50ms")
	}
}

func TestSleepMs_10ms_ActuallySleeps(t *testing.T) {
	start := time.Now()
	sleepMs(10)
	if time.Since(start) < 9*time.Millisecond {
		t.Error("sleepMs(10) returned too early")
	}
}

// ─── getCurrentDriverStatus ───────────────────────────────────────────────────

func TestGetCurrentDriverStatus_UnknownDrive_ReturnsZeroValue(t *testing.T) {
	st := getCurrentDriverStatus("NONEXISTENT")
	if st.isMotorRunning || st.alarm != "" || st.currentPosition != 0 {
		t.Errorf("unexpected non-zero status: %+v", st)
	}
}

// ─── notifyDriverStatus — listener off ───────────────────────────────────────

func TestNotifyDriverStatus_ListenerOff_IsNoOp(t *testing.T) {
	isStatusListening.Store(false)
	channels.BroadCastDriveStatusChannel = make(chan channels.DriverStatus, 10)
	d := stubDevice("A")
	done := make(chan struct{})
	go func() { notifyDriverStatus("mode", "ABS", d); close(done) }()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Error("notifyDriverStatus (listener off) blocked >100ms")
	}
	if len(channels.BroadCastDriveStatusChannel) != 0 {
		t.Error("channel should be empty when listener is off")
	}
}

// ─── driverCurrentStatus.reset ───────────────────────────────────────────────

func TestDriverCurrentStatus_Reset_ClearsMotionFields(t *testing.T) {
	s := driverCurrentStatus{currentPosition: 180.5, mode: "REL", isMotorRunning: true, direction: 1}
	s.reset()
	if s.currentPosition != 0 {
		t.Errorf("currentPosition=%v want 0", s.currentPosition)
	}
	if s.mode != "ABS" {
		t.Errorf("mode=%q want ABS", s.mode)
	}
	if s.isMotorRunning {
		t.Error("isMotorRunning should be false after reset")
	}
}

// ─── setDirection ─────────────────────────────────────────────────────────────

func TestSetDirection_ShortestPathDisabled_DoesNotBlock(t *testing.T) {
	isStatusListening.Store(false)
	channels.BroadCastDriveStatusChannel = make(chan channels.DriverStatus, 10)
	d := stubDevice("A")
	done := make(chan struct{})
	go func() { setDirection(d, driverCurrentStatus{shortestPathEnabled: false}, -45.0); close(done) }()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Error("setDirection (shortest off) blocked >200ms")
	}
}

func TestSetDirection_ShortestPathEnabled_NegativeDegree_DoesNotBlock(t *testing.T) {
	isStatusListening.Store(false)
	channels.BroadCastDriveStatusChannel = make(chan channels.DriverStatus, 10)
	d := stubDevice("A")
	done := make(chan struct{})
	go func() { setDirection(d, driverCurrentStatus{shortestPathEnabled: true}, -30.0); close(done) }()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Error("setDirection (CCW) blocked >200ms")
	}
}

func TestSetDirection_ShortestPathEnabled_PositiveDegree_DoesNotBlock(t *testing.T) {
	isStatusListening.Store(false)
	channels.BroadCastDriveStatusChannel = make(chan channels.DriverStatus, 10)
	d := stubDevice("A")
	done := make(chan struct{})
	go func() { setDirection(d, driverCurrentStatus{shortestPathEnabled: true}, 30.0); close(done) }()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Error("setDirection (CW) blocked >200ms")
	}
}

// ─── checkPotNotLimit — motor-not-running and limits-disabled guards ──────────
// (Breach detection paths are covered above in withLimitTestSetup tests)

func TestCheckPotNotLimit_MotorNotRunning_ReturnsFalse(t *testing.T) {
	d := stubDevice("A")
	ds := settings.DriverSettings{POT: 350, NOT: -10}
	if checkPotNotLimit(180, d, ds, driverCurrentStatus{isMotorRunning: false}) {
		t.Error("should return false when motor not running")
	}
}

func TestCheckPotNotLimit_LimitsDisabled_ReturnsFalse(t *testing.T) {
	d := stubDevice("A")
	ds := settings.DriverSettings{POT: 0, NOT: 0}
	if checkPotNotLimit(180, d, ds, driverCurrentStatus{isMotorRunning: true}) {
		t.Error("disabled limits should return false")
	}
}

func TestCheckPotNotLimit_NormalPosition_ReturnsFalse(t *testing.T) {
	d := stubDevice("A")
	ds := settings.DriverSettings{POT: 350, NOT: -10}
	if checkPotNotLimit(180, d, ds, driverCurrentStatus{isMotorRunning: true, direction: 1}) {
		t.Error("position within limits should return false")
	}
}

// ─── reverseDir / nonReverseDir — PDO-active guard ───────────────────────────

func TestReverseDir_PDOActive_ReturnsNilImmediately(t *testing.T) {
	pdoActive.Store(true)
	pdoWatchdogRunning.Store(true)
	t.Cleanup(func() { pdoActive.Store(false); pdoWatchdogRunning.Store(false) })
	if err := reverseDir(stubDevice("A")); err != nil {
		t.Errorf("reverseDir (PDO active): got error %v", err)
	}
}

func TestNonReverseDir_PDOActive_ReturnsNilImmediately(t *testing.T) {
	pdoActive.Store(true)
	pdoWatchdogRunning.Store(true)
	t.Cleanup(func() { pdoActive.Store(false); pdoWatchdogRunning.Store(false) })
	if err := nonReverseDir(stubDevice("A")); err != nil {
		t.Errorf("nonReverseDir (PDO active): got error %v", err)
	}
}

// ─── StopJog — PDO-inactive guard ────────────────────────────────────────────

func TestStopJog_PDONotActive_ReturnsError(t *testing.T) {
	pdoActive.Store(false)
	if err := StopJog(stubDevice("A")); err == nil {
		t.Error("StopJog (PDO inactive): expected error, got nil")
	}
}

// ─── breakOn / breakOff — PDO-inactive, no SDO config ────────────────────────

func TestBreakOn_PDOInactive_TriesSDOConfig(t *testing.T) {
	pdoActive.Store(false)
	_ = breakOn(stubDevice("A")) // error expected; no panic
}

func TestBreakOff_PDOInactive_TriesSDOConfig(t *testing.T) {
	pdoActive.Store(false)
	_ = breakOff(stubDevice("A")) // error expected; no panic
}

// ─── pollDriveError — empty-devices path ─────────────────────────────────────

func TestPollDriveError_NoDevices_ReturnsError(t *testing.T) {
	if err := pollDriveError([]*MasterDevice{}); err == nil {
		t.Error("pollDriveError(empty): expected error")
	}
}

// ─── resetMultiTurn — empty-devices no-panic ─────────────────────────────────

func TestResetMultiTurn_PDOInactive_NoDevices_NoPanic(t *testing.T) {
	pdoActive.Store(false)
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("resetMultiTurn panicked: %v", r)
		}
	}()
	_ = resetMultiTurn([]*MasterDevice{})
}
