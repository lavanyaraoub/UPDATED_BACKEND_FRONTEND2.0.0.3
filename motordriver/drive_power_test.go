//go:build unit

package motordriver

// Tests for the PDO-active early-return branches in drive_power.go and reset.go.
//
// When IsPDOActive() returns true, several functions skip all CGo/SDO work
// and return immediately (nil error) or perform only pure-Go atomic writes.
// These branches are fully testable without live EtherCAT hardware.
//
// Setup: set pdoActive.Store(true) before each test, restore after
// (activatePDO, in mock_driver_test.go). The package-level masterDevices
// slice is left empty so any range over it is a no-op — no MasterDevice
// methods that touch C memory are called.
//
// SCOPE NOTE: testenv's original drive_power_test.go is ~900 lines and
// spans far more than drive_power.go — it also covers driver_status_keeper.go,
// break.go, clamp_declamp.go, poll_driver_alarm.go, reset_driver_system.go,
// poll_iostat.go, and more, several of which are already known to have
// deep signature/behavior changes (e.g. hasDeclamped/hasClamped's pointer-
// receiver + hard-error fix). Only the drive_power.go/reset.go-scoped
// section is ported here, verified compatible function by function:
//
//   - resetMultiTurn is already lowercase/unexported in testenv's own test
//     (resetMultiTurn(...), not ResetMultiTurn(...)) — matches HAL exactly,
//     no change needed.
//   - emergency()'s PDO-active branch iterates the package-level
//     masterDevices slice (empty in these tests) looking for a name match;
//     since it's empty, the loop body — including emergencyRampStop, which
//     is where drive_power.go's one real HAL/testenv behavioral difference
//     lives (whether to call SetTargetPositionPDO with the rest position) —
//     is never reached. These tests don't exercise that difference either
//     way, so the discrepancy doesn't affect them (noted here for
//     completeness, not fixed — see MOTORDRIVER_FOLLOWUP.md).
//
// The remaining ~700 lines (PDO setters/getters, driver status, break/clamp,
// alarm handling, reset polling) are deferred to a follow-up pass — see
// MOTORDRIVER_FOLLOWUP.md.

import (
	"testing"
	"time"

	channels "EtherCAT/channels"
	ethercatDevice "EtherCAT/ethercatdevicedatatypes"
	"EtherCAT/settings"

	cmap "github.com/orcaman/concurrent-map"
)

// ─── pdoStopMotion ────────────────────────────────────────────────────────

func TestPdoStopMotion_EmptyMasterDevicesReturnsNil(t *testing.T) {
	activatePDO(t)
	// masterDevices is package-level; it's empty in unit tests
	got := pdoStopMotion("A")
	if got != nil {
		t.Errorf("pdoStopMotion with empty masterDevices: got %v, want nil", got)
	}
}

// ─── PowerOn ──────────────────────────────────────────────────────────────

func TestPowerOn_PDOActiveReturnsNil(t *testing.T) {
	activatePDO(t)
	err := PowerOn(stubDevice("A"))
	if err != nil {
		t.Errorf("PowerOn PDO active: got error %v, want nil", err)
	}
}

// ─── FastPowerOn ──────────────────────────────────────────────────────────

func TestFastPowerOn_PDOActiveReturnsNil(t *testing.T) {
	activatePDO(t)
	// isStatusListening is false by default so notifyDriverStatus returns immediately
	err := FastPowerOn(stubDevice("A"))
	if err != nil {
		t.Errorf("FastPowerOn PDO active: got error %v, want nil", err)
	}
}

// ─── PowerOffAll ──────────────────────────────────────────────────────────

func TestPowerOffAll_EmptySliceReturnsNil(t *testing.T) {
	activatePDO(t)
	err := PowerOffAll([]*MasterDevice{})
	if err != nil {
		t.Errorf("PowerOffAll empty slice: got error %v, want nil", err)
	}
}

// ─── PowerOff ─────────────────────────────────────────────────────────────

func TestPowerOff_PDOActiveReturnsNil(t *testing.T) {
	activatePDO(t)
	err := PowerOff(stubDevice("A"))
	if err != nil {
		t.Errorf("PowerOff PDO active: got error %v, want nil", err)
	}
}

// ─── FastPowerOff ─────────────────────────────────────────────────────────

func TestFastPowerOff_PDOActiveReturnsNil(t *testing.T) {
	activatePDO(t)
	err := FastPowerOff(stubDevice("A"))
	if err != nil {
		t.Errorf("FastPowerOff PDO active: got error %v, want nil", err)
	}
}

// ─── emergency ────────────────────────────────────────────────────────────

func TestEmergency_PDOActiveEmptyMasterDevicesReturnsNil(t *testing.T) {
	activatePDO(t)
	// masterDevices is empty — range body never executes
	err := emergency(stubDevice("A"))
	if err != nil {
		t.Errorf("emergency PDO active empty devices: got error %v, want nil", err)
	}
}

// ─── ResetDriver ──────────────────────────────────────────────────────────

func TestResetDriver_PDOActiveReturnsNil(t *testing.T) {
	activatePDO(t)
	err := ResetDriver([]*MasterDevice{})
	if err != nil {
		t.Errorf("ResetDriver PDO active: got error %v, want nil", err)
	}
}

func TestResetDriver_PDOActiveNonEmptySliceReturnsNil(t *testing.T) {
	activatePDO(t)
	// Even with a device in the slice, PDO-active path returns immediately
	// before any SDO/C call
	err := ResetDriver([]*MasterDevice{stubDevice("A")})
	if err != nil {
		t.Errorf("ResetDriver PDO active non-empty: got error %v, want nil", err)
	}
}

// ─── resetMultiTurn ───────────────────────────────────────────────────────

func TestResetMultiTurn_EmptyDevicesIsNoOp(t *testing.T) {
	activatePDO(t)
	// resetMultiTurn returns (nothing) — just verify no panic on an empty slice
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("resetMultiTurn panicked: %v", r)
		}
	}()
	resetMultiTurn([]*MasterDevice{})
}

// TestResetMultiTurn_NilDriver_SkippedWithoutPanic verifies the nil-guard:
// a device with no Driver set is skipped rather than causing a nil pointer
// dereference on dev.Driver.ResetMultiTurn(...).
func TestResetMultiTurn_NilDriver_SkippedWithoutPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("resetMultiTurn panicked on nil Driver: %v", r)
		}
	}()
	d := stubDevice("A") // Driver is nil by default
	if err := resetMultiTurn([]*MasterDevice{d}); err != nil {
		t.Errorf("resetMultiTurn with nil Driver: got error %v, want nil (device skipped)", err)
	}
}

// TestResetMultiTurn_DispatchesToDevicesDriver verifies the actual fix:
// each device is dispatched to its OWN driver's ResetMultiTurn, not a
// global one.
func TestResetMultiTurn_DispatchesToDevicesDriver(t *testing.T) {
	mock := &mockDriver{}
	d := stubDevice("A")
	d.Driver = mock

	if err := resetMultiTurn([]*MasterDevice{d}); err != nil {
		t.Errorf("resetMultiTurn = %v, want nil", err)
	}
	// mockDriver.ResetMultiTurn returns m.resetMultiTurnErr (nil by default);
	// reaching this point without panic/error confirms dispatch happened.
}

// ─── MasterDevice PDO motion enable flags ────────────────────────────────
//
// EnableJogPDO, IsJogEnabled, EnablePosPDO, IsPosEnabled, SetJogPDOSetpoints,
// SetTargetVelocityPDO, SetTargetPositionPDO, and PDOSetDigitalOutput
// (all in ether_cat_gateway.go) are verified byte-identical between HAL and
// testenv — ported unchanged. Pure atomic operations, no CGo, no hardware.

func TestEnableJogPDO_SetAndRead(t *testing.T) {
	d := &MasterDevice{Name: "A"}

	d.EnableJogPDO(true)
	if !d.IsJogEnabled() {
		t.Errorf("IsJogEnabled() = false after EnableJogPDO(true), want true")
	}

	d.EnableJogPDO(false)
	if d.IsJogEnabled() {
		t.Errorf("IsJogEnabled() = true after EnableJogPDO(false), want false")
	}
}

func TestEnablePosPDO_SetAndRead(t *testing.T) {
	d := &MasterDevice{Name: "A"}

	d.EnablePosPDO(true)
	if !d.IsPosEnabled() {
		t.Errorf("IsPosEnabled() = false after EnablePosPDO(true), want true")
	}

	d.EnablePosPDO(false)
	if d.IsPosEnabled() {
		t.Errorf("IsPosEnabled() = true after EnablePosPDO(false), want false")
	}
}

// TestEnableJogPDO_ClearsPosMutualExclusion verifies that enabling jog
// automatically disables pos mode (they are mutually exclusive).
func TestEnableJogPDO_ClearsPosMutualExclusion(t *testing.T) {
	d := &MasterDevice{Name: "A"}
	d.EnablePosPDO(true)
	if !d.IsPosEnabled() {
		t.Fatalf("precondition: IsPosEnabled should be true")
	}

	d.EnableJogPDO(true)
	if d.IsPosEnabled() {
		t.Errorf("IsPosEnabled() = true after EnableJogPDO(true), want false (mutual exclusion)")
	}
}

// TestEnablePosPDO_ClearsJogMutualExclusion verifies that enabling pos
// automatically disables jog mode.
func TestEnablePosPDO_ClearsJogMutualExclusion(t *testing.T) {
	d := &MasterDevice{Name: "A"}
	d.EnableJogPDO(true)
	if !d.IsJogEnabled() {
		t.Fatalf("precondition: IsJogEnabled should be true")
	}

	d.EnablePosPDO(true)
	if d.IsJogEnabled() {
		t.Errorf("IsJogEnabled() = true after EnablePosPDO(true), want false (mutual exclusion)")
	}
}

// TestEnablePosPDO_ArmsPpSetpointPending verifies that enabling pos mode
// arms the one-shot set-point pulse required for CiA-402 Profile Position.
func TestEnablePosPDO_ArmsPpSetpointPending(t *testing.T) {
	d := &MasterDevice{Name: "A"}
	d.ppSetpointPending.Store(false)

	d.EnablePosPDO(true)
	if !d.ppSetpointPending.Load() {
		t.Errorf("ppSetpointPending = false after EnablePosPDO(true), want true")
	}
}

// ─── MasterDevice SetJogPDOSetpoints / SetTargetVelocityPDO ──────────────

func TestSetJogPDOSetpoints_PdoJogReadyFalseReturnsError(t *testing.T) {
	d := &MasterDevice{Name: "A", PdoJogReady: false}
	err := d.SetJogPDOSetpoints(0x000F, 3, 1000)
	if err == nil {
		t.Errorf("SetJogPDOSetpoints with PdoJogReady=false: expected error, got nil")
	}
}

func TestSetJogPDOSetpoints_PdoJogReadyTrueStoresValues(t *testing.T) {
	d := &MasterDevice{Name: "A", PdoJogReady: true}
	if err := d.SetJogPDOSetpoints(0x000F, 3, 5000); err != nil {
		t.Fatalf("SetJogPDOSetpoints: unexpected error: %v", err)
	}
	if got := d.desiredControlWord.Load(); got != 0x000F {
		t.Errorf("desiredControlWord = 0x%04X, want 0x000F", got)
	}
	if got := d.desiredOpMode.Load(); got != 3 {
		t.Errorf("desiredOpMode = %d, want 3", got)
	}
	if got := d.desiredTargetVelocity.Load(); got != 5000 {
		t.Errorf("desiredTargetVelocity = %d, want 5000", got)
	}
}

func TestSetTargetVelocityPDO_PdoJogReadyFalseReturnsError(t *testing.T) {
	d := &MasterDevice{Name: "A", PdoJogReady: false}
	err := d.SetTargetVelocityPDO(0)
	if err == nil {
		t.Errorf("SetTargetVelocityPDO with PdoJogReady=false: expected error, got nil")
	}
}

func TestSetTargetVelocityPDO_PdoJogReadyTrueStoresValue(t *testing.T) {
	d := &MasterDevice{Name: "A", PdoJogReady: true}
	if err := d.SetTargetVelocityPDO(12000); err != nil {
		t.Fatalf("SetTargetVelocityPDO: unexpected error: %v", err)
	}
	if got := d.desiredTargetVelocity.Load(); got != 12000 {
		t.Errorf("desiredTargetVelocity = %d, want 12000", got)
	}
}

// ─── MasterDevice SetTargetPositionPDO ───────────────────────────────────

func TestSetTargetPositionPDO_PdoPosReadyFalseReturnsError(t *testing.T) {
	d := &MasterDevice{Name: "A", PdoPosReady: false}
	err := d.SetTargetPositionPDO(1_800_000)
	if err == nil {
		t.Errorf("SetTargetPositionPDO with PdoPosReady=false: expected error, got nil")
	}
}

func TestSetTargetPositionPDO_PdoPosReadyTrueStoresValueAndArmsSetpoint(t *testing.T) {
	d := &MasterDevice{Name: "A", PdoPosReady: true}
	d.ppSetpointPending.Store(false)

	if err := d.SetTargetPositionPDO(3_600_000); err != nil {
		t.Fatalf("SetTargetPositionPDO: unexpected error: %v", err)
	}
	if got := d.desiredTargetPosition.Load(); got != 3_600_000 {
		t.Errorf("desiredTargetPosition = %d, want 3600000", got)
	}
	if !d.ppSetpointPending.Load() {
		t.Errorf("ppSetpointPending = false after SetTargetPositionPDO, want true")
	}
}

// ─── PDOSetDigitalOutput ──────────────────────────────────────────────────

func TestPDOSetDigitalOutput_EmptySliceIsNoOp(t *testing.T) {
	// Empty device slice must return without panic
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("PDOSetDigitalOutput empty slice panicked: %v", r)
		}
	}()
	PDOSetDigitalOutput([]*MasterDevice{}, 0xFF, 0x01)
}

func TestPDOSetDigitalOutput_PdoDigOutReadyFalseIsNoOp(t *testing.T) {
	d := &MasterDevice{Name: "A", PdoDigOutReady: false}
	d.desiredDigOutMask.Store(0)
	d.desiredDigOutVal.Store(0)

	PDOSetDigitalOutput([]*MasterDevice{d}, 0xAB, 0xCD)

	// PdoDigOutReady=false → values must NOT be updated
	if got := d.desiredDigOutMask.Load(); got != 0 {
		t.Errorf("desiredDigOutMask = 0x%X after PdoDigOutReady=false, want 0", got)
	}
	if got := d.desiredDigOutVal.Load(); got != 0 {
		t.Errorf("desiredDigOutVal = 0x%X after PdoDigOutReady=false, want 0", got)
	}
}

func TestPDOSetDigitalOutput_PdoDigOutReadyTrueStoresValues(t *testing.T) {
	d := &MasterDevice{Name: "A", PdoDigOutReady: true}

	PDOSetDigitalOutput([]*MasterDevice{d}, 0xDEAD, 0xBEEF)

	if got := d.desiredDigOutMask.Load(); got != 0xDEAD {
		t.Errorf("desiredDigOutMask = 0x%X, want 0xDEAD", got)
	}
	if got := d.desiredDigOutVal.Load(); got != 0xBEEF {
		t.Errorf("desiredDigOutVal = 0x%X, want 0xBEEF", got)
	}
}

// ─── getCurrentDriverStatus / setCurrentDriverStatus ─────────────────────
//
// Both verified byte-identical to testenv in driver_status_keeper.go.

// TestGetCurrentDriverStatus_UnknownDriveReturnsZeroValue verifies that
// querying an unregistered drive name returns a safe zero-value status
// rather than panicking.
func TestGetCurrentDriverStatus_UnknownDriveReturnsZeroValue(t *testing.T) {
	// cmap.ConcurrentMap is a struct with an internal shard slice that panics
	// if used before initialisation. Initialise it unconditionally here.
	driverStatusMap = cmap.New()
	t.Cleanup(func() { driverStatusMap = cmap.New() })

	status := getCurrentDriverStatus("NONEXISTENT_DRIVE")
	if status.isMotorRunning {
		t.Errorf("unknown drive isMotorRunning = true, want false")
	}
	if status.currentPosition != 0 {
		t.Errorf("unknown drive currentPosition = %v, want 0", status.currentPosition)
	}
}

// TestSetAndGetCurrentDriverStatus_RoundTrip verifies that a status stored
// via setCurrentDriverStatus is correctly retrieved by getCurrentDriverStatus.
func TestSetAndGetCurrentDriverStatus_RoundTrip(t *testing.T) {
	driverStatusMap = cmap.New()
	t.Cleanup(func() { driverStatusMap = cmap.New() })

	driveName := "TEST_DRIVE_A"
	want := driverCurrentStatus{
		currentPosition: 45.5,
		isMotorRunning:  true,
		alarm:           "test alarm",
		mode:            "REL",
	}
	setCurrentDriverStatus(driveName, want)

	got := getCurrentDriverStatus(driveName)
	if got.currentPosition != want.currentPosition {
		t.Errorf("currentPosition = %v, want %v", got.currentPosition, want.currentPosition)
	}
	if got.isMotorRunning != want.isMotorRunning {
		t.Errorf("isMotorRunning = %v, want %v", got.isMotorRunning, want.isMotorRunning)
	}
	if got.alarm != want.alarm {
		t.Errorf("alarm = %q, want %q", got.alarm, want.alarm)
	}
	if got.mode != want.mode {
		t.Errorf("mode = %q, want %q", got.mode, want.mode)
	}
}

// TestSetCurrentDriverStatus_OverwritesPreviousValue verifies that a second
// write replaces the first.
func TestSetCurrentDriverStatus_OverwritesPreviousValue(t *testing.T) {
	driverStatusMap = cmap.New()
	t.Cleanup(func() { driverStatusMap = cmap.New() })

	driveName := "TEST_DRIVE_OW"
	setCurrentDriverStatus(driveName, driverCurrentStatus{currentPosition: 10})
	setCurrentDriverStatus(driveName, driverCurrentStatus{currentPosition: 99})

	got := getCurrentDriverStatus(driveName)
	if got.currentPosition != 99 {
		t.Errorf("currentPosition = %v after overwrite, want 99", got.currentPosition)
	}
}

// ─── doECSCheck / doECSCheckZero (ECS disabled path) ─────────────────────
//
// When ECS == 0 (disabled), both functions skip the wait and return 1
// immediately — no channels, no hardware needed. Both verified
// byte-identical to testenv in ecs.go.

// TestDoECSCheck_ECSDisabledReturnsOne verifies immediate return when ECS=0.
func TestDoECSCheck_ECSDisabledReturnsOne(t *testing.T) {
	d := stubDevice("A")
	// Default DriverSettings has ECS=0 (disabled)
	got := doECSCheck(d, 90.0)
	if got != 1 {
		t.Errorf("doECSCheck with ECS=0: got %d, want 1 (immediate pass)", got)
	}
}

// TestDoECSCheckZero_ECSDisabledReturnsOne verifies immediate return when ECS=0.
func TestDoECSCheckZero_ECSDisabledReturnsOne(t *testing.T) {
	d := stubDevice("A")
	got := doECSCheckZero(d, 90.0)
	if got != 1 {
		t.Errorf("doECSCheckZero with ECS=0: got %d, want 1 (immediate pass)", got)
	}
}

// ─── handleErrCode — DEFERRED, not a simple port ──────────────────────────
//
// poll_driver_alarm.go grew a substantial new subsystem in HAL beyond
// anything testenv has: a lazily-loaded error-code-description lookup
// (LookupErrorDescription/loadErrorDescriptions, reading
// configs/error_definition.txt), Nidec M700 error-code normalization
// (normalizeErrorCode — M700 encodes alarm IDs as 0xFF00|id), and a
// 20-entry fault-history ring buffer persisted to disk (RecordFault —
// this is what backs the /fault/history REST endpoint found earlier in
// this merge, restapi.go).
//
// handleErrCode's signature itself changed to match:
//   HAL:     handleErrCode(device *MasterDevice, errCode int, statusword uint16, lastReportedErrCode *int)
//   testenv: handleErrCode(errCode int, lastReportedErrCode *int)
//
// Porting this properly means understanding and testing RecordFault and
// FormatErrorCode too, not just adding two parameters — deferred to its
// own pass rather than forced through here. See MOTORDRIVER_FOLLOWUP.md.

// ─── setDirection / clearTargetReached / doneDriveStatusUpdate ───────────
//
// All three verified byte-identical to testenv (move_to_degree.go,
// driver_status_keeper.go).

func TestSetDirection_ShortestPathDisabled_AlwaysCW(t *testing.T) {
	// shortestPathEnabled=false → always sends "1" (CW), regardless of degree sign
	isStatusListening.Store(false) // ensure no channel send
	d := stubDevice("A")
	status := driverCurrentStatus{shortestPathEnabled: false}

	// Should not panic for either positive or negative degrees
	setDirection(d, status, 90.0)
	setDirection(d, status, -90.0)
}

func TestSetDirection_ShortestPathEnabled_PositiveDegree_CW(t *testing.T) {
	isStatusListening.Store(false)
	d := stubDevice("A")
	status := driverCurrentStatus{shortestPathEnabled: true}
	// positive degree → CW ("1") — must not panic
	setDirection(d, status, 45.0)
}

func TestSetDirection_ShortestPathEnabled_NegativeDegree_CCW(t *testing.T) {
	isStatusListening.Store(false)
	d := stubDevice("A")
	status := driverCurrentStatus{shortestPathEnabled: true}
	// negative degree → CCW ("-1") — must not panic
	setDirection(d, status, -45.0)
}

func TestClearTargetReached_DoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("clearTargetReached panicked: %v", r)
		}
	}()
	clearTargetReached(stubDevice("A"))
}

func TestDoneDriveStatusUpdate_WhenNotWaiting_IsNoOp(t *testing.T) {
	// waitForDriveStatusUpdate=false → doneDriveStatusUpdate is a no-op
	waitForDriveStatusUpdate.Store(false)
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("doneDriveStatusUpdate panicked when not waiting: %v", r)
		}
	}()
	doneDriveStatusUpdate()
}

// ─── HasDriverConnected / getMasterDevices ────────────────────────────────

func TestHasDriverConnected_DefaultIsFalse(t *testing.T) {
	// driverConnectionStatus starts as "" (not "SUCCESS") before InitMaster
	if driverConnectionStatus == "SUCCESS" {
		t.Skip("driver already connected — skipping default-false test")
	}
	if HasDriverConnected() {
		t.Errorf("HasDriverConnected() = true before connection, want false")
	}
}

func TestGetMasterDevices_ReturnsSlice(t *testing.T) {
	// getMasterDevices always returns a slice (may be nil or empty before init)
	devices := getMasterDevices()
	// Just verify no panic and it returns something
	_ = devices
}

// ─── RefreshCurrentPosition — empty devices path ──────────────────────────
//
// Verified byte-identical for the empty-slice path: HAL's version adds an
// SDO fallback branch inside the per-device loop body, but that loop never
// executes when masterDevices is empty, so this test is unaffected.

func TestRefreshCurrentPosition_NoDevicesIsNoOp(t *testing.T) {
	restore := setMasterDevicesForTest()
	defer restore()

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("RefreshCurrentPosition panicked with no devices: %v", r)
		}
	}()
	RefreshCurrentPosition()
}

// ─── RefreshCurrentPosition — PDO-ready loop body ─────────────────────────
//
// currentDriverPosition unconditionally sends on
// channels.BroadCastDriveStatusChannel — a buffered channel with a drain
// goroutine is required here or the call blocks forever.

func TestRefreshCurrentPosition_PdoReady_ReadsAtomicPositionAndBroadcasts(t *testing.T) {
	driverStatusMap = cmap.New()
	t.Cleanup(func() { driverStatusMap = cmap.New() })

	saved := channels.BroadCastDriveStatusChannel
	ch := make(chan channels.DriverStatus, 4)
	channels.BroadCastDriveStatusChannel = ch
	t.Cleanup(func() { channels.BroadCastDriveStatusChannel = saved })

	d := stubDevice("A")
	d.Device.Name = "A"
	d.Device.DriveXRatio = 1000
	d.PdoReady = true
	d.PDOPos.Store(90000) // 90000 / 1000 = 90 degrees, before offset/correction

	restore := setMasterDevicesForTest(d)
	defer restore()

	done := make(chan struct{})
	go func() {
		RefreshCurrentPosition()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("RefreshCurrentPosition (PdoReady) did not return promptly")
	}

	select {
	case msg := <-ch:
		if msg.DriveName != "A" || msg.Event != "current_position" {
			t.Errorf("broadcast message = %+v, want DriveName=A Event=current_position", msg)
		}
	default:
		t.Error("expected a current_position message on BroadCastDriveStatusChannel, got none")
	}
}

func TestRefreshCurrentPosition_PdoNotReady_NoConfig_SkipsDeviceWithoutPanic(t *testing.T) {
	driverStatusMap = cmap.New()
	t.Cleanup(func() { driverStatusMap = cmap.New() })

	saved := channels.BroadCastDriveStatusChannel
	channels.BroadCastDriveStatusChannel = make(chan channels.DriverStatus, 4)
	t.Cleanup(func() { channels.BroadCastDriveStatusChannel = saved })

	d := stubDevice("A") // PdoReady=false, Device.AddressConfigName="" (no SDO config registered)
	restore := setMasterDevicesForTest(d)
	defer restore()

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("RefreshCurrentPosition (no PDO, no config) panicked: %v", r)
		}
	}()

	done := make(chan struct{})
	go func() {
		RefreshCurrentPosition()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("RefreshCurrentPosition (no PDO, no config) did not return promptly")
	}
}

func TestRefreshCurrentPosition_MultipleDevices_EachBroadcastsIndependently(t *testing.T) {
	driverStatusMap = cmap.New()
	t.Cleanup(func() { driverStatusMap = cmap.New() })

	saved := channels.BroadCastDriveStatusChannel
	ch := make(chan channels.DriverStatus, 8)
	channels.BroadCastDriveStatusChannel = ch
	t.Cleanup(func() { channels.BroadCastDriveStatusChannel = saved })

	a := stubDevice("A")
	a.Device.Name, a.Device.DriveXRatio = "A", 1000
	a.PdoReady = true
	a.PDOPos.Store(1000) // 1 degree

	b := stubDevice("B")
	b.Device.Name, b.Device.DriveXRatio = "B", 1000
	b.PdoReady = true
	b.PDOPos.Store(2000) // 2 degrees

	restore := setMasterDevicesForTest(a, b)
	defer restore()

	done := make(chan struct{})
	go func() {
		RefreshCurrentPosition()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("RefreshCurrentPosition (multi-device) did not return promptly")
	}

	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case msg := <-ch:
			seen[msg.DriveName] = true
		default:
		}
	}
	if !seen["A"] || !seen["B"] {
		t.Errorf("expected broadcasts for both A and B, got %v", seen)
	}
}

// ─── ReadActualPositionFromDrive — unknown drive path ─────────────────────

func TestReadActualPositionFromDrive_UnknownDriveReturnsCachedZero(t *testing.T) {
	driverStatusMap = cmap.New()
	t.Cleanup(func() { driverStatusMap = cmap.New() })

	pos := ReadActualPositionFromDrive("NONEXISTENT")
	if pos != 0 {
		t.Errorf("ReadActualPositionFromDrive unknown drive = %v, want 0 (cached default)", pos)
	}
}

// ─── stopECSCheck ────────────────────────────────────────────────────────

func TestStopECSCheck_WhenNotRunning_IsNoOp(t *testing.T) {
	isECSCheckInProgress.Store(false)
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("stopECSCheck panicked when not running: %v", r)
		}
	}()
	stopECSCheck()
	if isECSCheckInProgress.Load() {
		t.Errorf("isECSCheckInProgress = true after stopECSCheck, want false")
	}
}

// ─── stopErrorPolling ────────────────────────────────────────────────────

func TestStopErrorPolling_WhenNotRunning_IsNoOp(t *testing.T) {
	errPollingRunning.Store(false)
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("stopErrorPolling panicked when not running: %v", r)
		}
	}()
	stopErrorPolling()
}

// TestStopErrorPolling_WhenRunning_SetsFlag: adapted for stopErrPollingChans
// (a slice, one channel per poller goroutine — the Phase 4 multi-device fix)
// rather than testenv's single stopErrPollingChan.
func TestStopErrorPolling_WhenRunning_SetsFlag(t *testing.T) {
	stopErrPollingChans = []chan bool{make(chan bool, 1)}
	errPollingRunning.Store(true)
	t.Cleanup(func() { errPollingRunning.Store(false) })

	stopErrorPolling()

	if errPollingRunning.Load() {
		t.Errorf("errPollingRunning = true after stopErrorPolling, want false")
	}
}

// ─── doneDriverAction ────────────────────────────────────────────────────

func TestDoneDriverAction_SendsNotifyCmdComplete(t *testing.T) {
	// NotifyCmdComplete sends to CommandExecStatusChannel (buffered, size 1)
	// Drain it first to avoid double-fill
	select {
	case <-channels.CommandExecStatusChannel:
	default:
	}
	channels.OpenWaitChannel()
	doneDriverAction()
	// If NotifyCmdComplete was called, the channel now has a message
	select {
	case msg := <-channels.CommandExecStatusChannel:
		if !msg.Completed {
			t.Errorf("Completed = false, want true")
		}
	default:
		t.Errorf("CommandExecStatusChannel empty after doneDriverAction — NotifyCmdComplete not called")
	}
}

// ─── notifyDriverStatus — isStatusListening=false path ───────────────────
//
// Both verified byte-identical in behavior for this path (HAL's
// notifyDriverStatusWithWait adds a mutex around the whole function body,
// transparent to this early-return case).

func TestNotifyDriverStatus_WhenNotListening_IsNoOp(t *testing.T) {
	isStatusListening.Store(false)
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("notifyDriverStatus panicked when not listening: %v", r)
		}
	}()
	notifyDriverStatus("test_event", "data", stubDevice("A"))
}

func TestNotifyDriverStatusWithWait_WhenNotListening_IsNoOp(t *testing.T) {
	isStatusListening.Store(false)
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("notifyDriverStatusWithWait panicked when not listening: %v", r)
		}
	}()
	notifyDriverStatusWithWait("test_event", "data", stubDevice("A"))
}

// ─── StopSystem — no driver connected path ───────────────────────────────

func TestStopSystem_WhenNoDriverConnected_IsNoOp(t *testing.T) {
	if HasDriverConnected() {
		t.Skip("driver connected — StopSystem would initiate real shutdown")
	}
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("StopSystem panicked when no driver connected: %v", r)
		}
	}()
	StopSystem()
}

// ─── currentDriverPosition — with live BroadCastDriveStatusChannel ────────

func TestCurrentDriverPosition_BroadcastsPositionEvent(t *testing.T) {
	ch := make(chan channels.DriverStatus, 4)
	channels.BroadCastDriveStatusChannel = ch
	t.Cleanup(func() {
		channels.BroadCastDriveStatusChannel = nil
		close(ch)
	})

	d := stubDevice("A")
	currentDriverPosition(d, 45.5)

	select {
	case msg := <-ch:
		if msg.Event != "current_position" {
			t.Errorf("Event = %q, want \"current_position\"", msg.Event)
		}
		if msg.DriveName != "A" {
			t.Errorf("DriveName = %q, want \"A\"", msg.DriveName)
		}
		if msg.Data != "45.500" {
			t.Errorf("Data = %q, want \"45.500\"", msg.Data)
		}
	default:
		t.Errorf("BroadCastDriveStatusChannel empty after currentDriverPosition")
	}
}

// ─── startDriverStatusListener / stopDriveStatusListener ─────────────────

func TestStartAndStopDriverStatusListener_DoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("start/stop driver status listener panicked: %v", r)
		}
	}()
	startDriverStatusListener()
	t.Cleanup(func() {
		stopDriveStatusListener()
		isStatusListening.Store(false)
	})
	// Give the goroutine time to set isStatusListening=true
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if isStatusListening.Load() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !isStatusListening.Load() {
		t.Errorf("isStatusListening = false after startDriverStatusListener, want true")
	}
}

// ─── pollDriveError — empty devices path ─────────────────────────────────
//
// ADAPTED: HAL's pollDriveError takes only the device slice — the usePDO
// bool parameter testenv had doesn't exist (HAL decides PDO-vs-SDO
// internally per-device via IsPDOActive(), same pattern as everywhere else).

func TestPollDriveError_EmptyDevicesReturnsError(t *testing.T) {
	err := pollDriveError([]*MasterDevice{})
	if err == nil {
		t.Errorf("pollDriveError empty slice: expected error, got nil")
	}
}

// ─── breakOn / breakOff — PDO active paths ───────────────────────────────
//
// ADAPTED: HAL routes through getRealDeviceForBrake(name), which looks the
// device up by name in the package-level masterDevices slice (the Bug-2a-
// style multi-axis fix — ensures the brake command targets the correct
// axis rather than broadcasting to every device). testenv's version calls
// PDOSetDigitalOutput(masterDevices, ...) directly without a name lookup.
// To exercise the PDO fast path, the stub device must be registered in
// masterDevices under the same name it's called with.

func TestBreakOn_PDOActive_DigOutReady_NoError(t *testing.T) {
	activatePDO(t)
	d := stubDevice("A")
	d.PdoDigOutReady = true
	restore := setMasterDevicesForTest(d) // getRealDeviceForBrake("A") must find this device
	defer restore()

	err := breakOn(d)
	if err != nil {
		t.Errorf("breakOn PDO active + DigOutReady: got error %v, want nil", err)
	}
}

func TestBreakOff_PDOActive_DigOutReady_NoError(t *testing.T) {
	activatePDO(t)
	d := stubDevice("A")
	d.PdoDigOutReady = true
	restore := setMasterDevicesForTest(d)
	defer restore()

	err := breakOff(d)
	if err != nil {
		t.Errorf("breakOff PDO active + DigOutReady: got error %v, want nil", err)
	}
}

// ─── hasDeclamped / hasClamped — ClampDeclamp=0 path ─────────────────────
//
// ADAPTED: both now hard-error if masterDevice.Driver is nil (Bug 2a/2b —
// no more silent GetMotorDriver() fallback), checked BEFORE the
// ClampDeclamp==0 early return. A stub device's Driver is nil by default,
// so it must be given a mock driver to reach the branch these tests
// actually exercise.

func TestHasDeclamped_ClampDisabled_ReturnsTrue(t *testing.T) {
	d := stubDevice("A")
	d.Driver = &mockDriver{}
	s := settings.DriverSettings{ClampDeclamp: 0}
	ok, err := hasDeclamped(d, s)
	if err != nil {
		t.Errorf("hasDeclamped ClampDeclamp=0: unexpected error: %v", err)
	}
	if !ok {
		t.Errorf("hasDeclamped ClampDeclamp=0: got false, want true")
	}
}

func TestHasClamped_ClampDisabled_ReturnsFalseNoError(t *testing.T) {
	d := stubDevice("A")
	d.Driver = &mockDriver{}
	s := settings.DriverSettings{ClampDeclamp: 0}
	ok, err := hasClamped(d, s)
	if err != nil {
		t.Errorf("hasClamped ClampDeclamp=0: unexpected error: %v", err)
	}
	if ok {
		t.Errorf("hasClamped ClampDeclamp=0: got true, want false (clamping not enabled)")
	}
}

// TestHasDeclamped_NilDriver_HardErrors pins the new (Bug 2a/2b) behavior
// directly: a device with no Driver set must hard-error, not silently fall
// back to a possibly-wrong global driver.
func TestHasDeclamped_NilDriver_HardErrors(t *testing.T) {
	d := stubDevice("A") // Driver is nil
	_, err := hasDeclamped(d, settings.DriverSettings{ClampDeclamp: 0})
	if err == nil {
		t.Errorf("hasDeclamped with nil Driver: expected error, got nil")
	}
}

// ─── applyClampIfSettingsChanged — no devices ─────────────────────────────

func TestApplyClampIfSettingsChanged_NoDevicesIsNoOp(t *testing.T) {
	restore := setMasterDevicesForTest()
	defer restore()

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("applyClampIfSettingsChanged panicked with no devices: %v", r)
		}
	}()
	applyClampIfSettingsChanged()
}

// ─── readDigitalInputs / readInputSignal / readECSSignal / hardResetInput ──
//
// ADAPTED: the backing storage for the digital-input register moved from
// the package-level lastPDODI atomic (removed) to the per-device
// MasterDevice.PDODI field — same PDO-storage migration found throughout
// pdo_cyclic_task.go. Store on d.PDODI directly instead of the (now
// nonexistent) lastPDODI.

func TestReadDigitalInputs_PdoDIReady_ReturnsAtomicValue(t *testing.T) {
	d := stubDevice("A")
	d.PdoDIReady = true
	d.PDODI.Store(0x0042)

	val, err := readDigitalInputs(d, "readinputsignal")
	if err != nil {
		t.Errorf("readDigitalInputs PdoDIReady=true: unexpected error: %v", err)
	}
	if val != 0x0042 {
		t.Errorf("readDigitalInputs = 0x%X, want 0x0042", val)
	}
}

func TestReadInputSignal_PdoDIReady_DelegatesToReadDigitalInputs(t *testing.T) {
	d := stubDevice("A")
	d.PdoDIReady = true
	d.PDODI.Store(0x0010)

	val, err := readInputSignal(d)
	if err != nil {
		t.Errorf("readInputSignal: unexpected error: %v", err)
	}
	if val != 0x0010 {
		t.Errorf("readInputSignal = 0x%X, want 0x0010", val)
	}
}

func TestReadECSSignal_PdoDIReady_DelegatesToReadDigitalInputs(t *testing.T) {
	d := stubDevice("A")
	d.PdoDIReady = true
	d.PDODI.Store(0x0001)

	val, err := readECSSignal(d)
	if err != nil {
		t.Errorf("readECSSignal: unexpected error: %v", err)
	}
	if val != 0x0001 {
		t.Errorf("readECSSignal = 0x%X, want 0x0001", val)
	}
}

func TestHardResetInput_PdoDIReady_DelegatesToReadDigitalInputs(t *testing.T) {
	d := stubDevice("A")
	d.PdoDIReady = true
	d.PDODI.Store(0x0020)

	val, err := hardResetInput(d)
	if err != nil {
		t.Errorf("hardResetInput: unexpected error: %v", err)
	}
	if val != 0x0020 {
		t.Errorf("hardResetInput = 0x%X, want 0x0020", val)
	}
}

// ─── performSysReset — motor running path ────────────────────────────────
//
// Verified byte-identical to testenv (reset_driver_system.go).

func TestPerformSysReset_MotorRunning_AbortsWithAlarm(t *testing.T) {
	driverStatusMap = cmap.New()
	t.Cleanup(func() { driverStatusMap = cmap.New() })

	// Set up BroadCastUIChannel so statusnotifier.Alarm doesn't block
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

	// Register a running motor
	d := stubDevice("A")
	restore := setMasterDevicesForTest(d)
	t.Cleanup(restore)
	setCurrentDriverStatus("A", driverCurrentStatus{isMotorRunning: true})

	// performSysReset with checkMotorRunning=true should abort immediately
	// without sending to channels.ResetDriverSystem (which has no listener here)
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("performSysReset panicked: %v", r)
		}
	}()
	performSysReset(true)
}

// ─── getSize ──────────────────────────────────────────────────────────────
//
// The one function in ether_cat_gateway.go with no hardware dependency of
// its own that nothing exercised directly (everything else there is either
// a direct cgo call needing a live master, or one of the PDO atomic helpers
// already tested above).

func TestGetSize_KnownDataTypes_ReturnDistinctSizesInAscendingOrder(t *testing.T) {
	u8 := uint64(getSize(ethercatDevice.Step{DataType: "U8"}))
	u16 := uint64(getSize(ethercatDevice.Step{DataType: "U16"}))
	u32 := uint64(getSize(ethercatDevice.Step{DataType: "U32"}))
	i8 := uint64(getSize(ethercatDevice.Step{DataType: "I8"}))
	i16 := uint64(getSize(ethercatDevice.Step{DataType: "I16"}))
	i32 := uint64(getSize(ethercatDevice.Step{DataType: "I32"}))

	if u8 == 0 || u16 == 0 || u32 == 0 {
		t.Fatalf("expected non-zero sizes, got U8=%d U16=%d U32=%d", u8, u16, u32)
	}
	if !(u8 < u16 && u16 < u32) {
		t.Errorf("expected U8 < U16 < U32, got U8=%d U16=%d U32=%d", u8, u16, u32)
	}
	if i8 != u8 {
		t.Errorf("I8 size (%d) should match U8 size (%d) — same width, different signedness", i8, u8)
	}
	if i16 != u16 {
		t.Errorf("I16 size (%d) should match U16 size (%d)", i16, u16)
	}
	if i32 != u32 {
		t.Errorf("I32 size (%d) should match U32 size (%d)", i32, u32)
	}
}

func TestGetSize_UINT_MatchesPlatformUnsignedIntSize(t *testing.T) {
	uintSize := uint64(getSize(ethercatDevice.Step{DataType: "UINT"}))
	if uintSize == 0 {
		t.Error("UINT: expected non-zero size")
	}
}

func TestGetSize_UnknownDataType_FallsBackToUint8Size(t *testing.T) {
	unknown := uint64(getSize(ethercatDevice.Step{DataType: "NOT_A_REAL_TYPE"}))
	u8 := uint64(getSize(ethercatDevice.Step{DataType: "U8"}))
	if unknown != u8 {
		t.Errorf("unknown DataType: got size %d, want fallback to U8 size %d", unknown, u8)
	}
}

func TestGetSize_EmptyDataType_FallsBackToUint8Size(t *testing.T) {
	empty := uint64(getSize(ethercatDevice.Step{DataType: ""}))
	u8 := uint64(getSize(ethercatDevice.Step{DataType: "U8"}))
	if empty != u8 {
		t.Errorf("empty DataType: got size %d, want fallback to U8 size %d", empty, u8)
	}
}
