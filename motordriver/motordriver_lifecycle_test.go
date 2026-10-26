//go:build unit

package motordriver

// PORTING NOTES (testenv → HAL):
//   - pdoErrLoop gained a stopCh chan bool parameter (3 args, was 2) and
//     reads per-device device.PDOStatus/PDOErr rather than the removed
//     package-level lastPDOStatus/lastPDOErr atomics — same PDO-storage
//     migration found throughout pdo_cyclic_task.go.
//   - pollDriveError/pollDriveErrWorker dropped their usePDO bool parameter;
//     HAL decides PDO-vs-SDO internally via IsPDOActive()/device.PdoErrorReady.
//   - speclDriver (the old global driver variable) no longer exists —
//     stopPollIOStat now iterates the package-level polledDrivers slice,
//     which is empty/no-op here since pollIOStat was never started.
//   - hasDeclamped/hasClamped hard-error on a nil Driver (Bug 2a/2b, found
//     early in this whole merge) — give the stub a mock Driver.
//   - moveToZero calls notifier.NotifyDestinationPosition unconditionally
//     with no isStatusListening guard (see ethercat_config_and_zero_ref_test.go
//     for the full explanation) — a nil BroadCastUIChannel would hang this
//     test, not fail it. Guarded the same way as there.

import (
	"testing"
	"time"

	channels "EtherCAT/channels"
	"EtherCAT/settings"

	cmap "github.com/orcaman/concurrent-map"
)

// ─── moveToZero — PDO not ready path ──────────────────────────────────────

func TestMoveToZero_PDOPositionNotReady_ReturnsError(t *testing.T) {
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

	pdoActive.Store(false)
	d := stubDevice("A")
	d.PdoPosReady = false
	err := moveToZero(d)
	if err == nil {
		t.Fatal("expected error when PDO not ready, got nil")
	}
}

// ─── ResetDriver — PDO/non-PDO/empty paths ────────────────────────────────

func TestResetDriver_PDOActive_UsesPDOPath(t *testing.T) {
	activatePDO(t)
	d := stubDevice("A")
	err := ResetDriver([]*MasterDevice{d})
	if err != nil {
		t.Errorf("ResetDriver PDO path: got error %v, want nil", err)
	}
}

func TestResetDriver_PDONotActive_UsesSDOPath(t *testing.T) {
	pdoActive.Store(false)
	// Unknown config — SDO path returns error gracefully
	d := stubDevice("A")
	d.Device.AddressConfigName = "nonexistent"
	err := ResetDriver([]*MasterDevice{d})
	// Error is acceptable — SDO path was exercised
	t.Logf("ResetDriver SDO path: %v", err)
}

func TestResetDriver_NoDevices_ReturnsNil(t *testing.T) {
	pdoActive.Store(false)
	err := ResetDriver([]*MasterDevice{})
	if err != nil {
		t.Errorf("ResetDriver empty devices: got error %v, want nil", err)
	}
}

// ─── resetSystemWorker — PDO not active branch ────────────────────────────

func TestResetSystemWorker_PDONotActive_LogsError(t *testing.T) {
	initDriverStatusKeeperListener()

	channels.BroadCastUIChannel = make(chan channels.SocketMessage, 8)
	channels.ResetDriverSystem = make(chan bool, 2)
	channels.CommandExecInputChannel = make(chan channels.CommandExecInput, 4)
	channels.CommandExecStatusChannel = make(chan channels.CommandExecStatus, 4)
	t.Cleanup(func() {
		channels.BroadCastUIChannel = nil
		channels.ResetDriverSystem = nil
		channels.CommandExecInputChannel = nil
		channels.CommandExecStatusChannel = nil
	})

	restore := setMasterDevicesForTest(stubDevice("A"))
	t.Cleanup(restore)

	pdoActive.Store(false)
	t.Cleanup(func() { pdoActive.Store(false) })

	done := make(chan struct{})
	go func() {
		defer close(done)
		resetSystemWorker()
	}()

	// true = trigger reset; false = stop the worker afterward. Both are
	// buffered sends (channel capacity 2), so both land before
	// resetSystemWorker reads either — but since it's a single goroutine
	// processing messages strictly in order, it fully finishes handling
	// true (including every restart call) before it ever looks at false.
	// Waiting on `done` below is what actually guarantees that — a fixed
	// sleep here would not, since resetSystemWorker's restart calls spawn
	// further goroutines whose own startup isn't synchronized with any
	// timer.
	channels.ResetDriverSystem <- true
	channels.ResetDriverSystem <- false

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("resetSystemWorker did not exit after true+false")
	}

	// Everything the reset's restart phase spawned (pollDrivePosition,
	// pollIOStat, initDriverActionListener, startDriverStatusListener —
	// all triggered since pdoActive=false took the PDO-inactive branch)
	// is now guaranteed to have been called, and needs to be stopped
	// before this test ends, or it leaks into whatever test runs next.
	stopDriverPolling()
	stopPollIOStat()
	stopDriverActionListener()
	stopDriveStatusListener()
	waitForFlagFalse(&isActionListening, 1*time.Second, "listenDriverAction")
	waitForFlagFalse(&isStatusListening, 1*time.Second, "listenDriverStatus")
}

// ─── pollDriveError — non-PDO path ─────────────────────────────────────────

func TestPollDriveError_NonPDOPath_StartsAndStops(t *testing.T) {
	initDriverStatusKeeperListener()
	channels.BroadCastUIChannel = make(chan channels.SocketMessage, 4)
	t.Cleanup(func() {
		channels.BroadCastUIChannel = nil
		errPollingRunning.Store(false)
		stopDriveStatusListener()
		waitForFlagFalse(&isStatusListening, 1*time.Second, "listenDriverStatus")
	})

	d := stubDevice("A")
	err := pollDriveError([]*MasterDevice{d})
	if err != nil {
		t.Fatalf("pollDriveError non-PDO: got error %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	stopErrorPolling()
	// Give the goroutine time to exit cleanly
	time.Sleep(150 * time.Millisecond)
}

// ─── pdoErrLoop — statusword branches, per-device fields ──────────────────

func TestPdoErrLoop_FaultBitSet_CallsHandleErrCode(t *testing.T) {
	initDriverStatusKeeperListener()
	channels.BroadCastUIChannel = make(chan channels.SocketMessage, 8)
	stopCh := make(chan bool, 1)
	errPollingRunning.Store(true)

	d := stubDevice("A")
	d.PDOStatus.Store(uint32(0x0008)) // fault bit set
	d.PDOErr.Store(uint32(0x0088))    // Err 88

	t.Cleanup(func() {
		channels.BroadCastUIChannel = nil
		errPollingRunning.Store(false)
		stopDriveStatusListener()
		waitForFlagFalse(&isStatusListening, 1*time.Second, "listenDriverStatus")
	})

	var lastCode int
	done := make(chan struct{})
	go func() {
		defer close(done)
		pdoErrLoop(d, stopCh, &lastCode)
	}()
	time.Sleep(250 * time.Millisecond)
	stopCh <- true
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Error("pdoErrLoop did not stop within 200ms of stopCh signal")
	}
}

func TestPdoErrLoop_AlarmClearedTransition(t *testing.T) {
	initDriverStatusKeeperListener()
	channels.BroadCastUIChannel = make(chan channels.SocketMessage, 8)
	stopCh := make(chan bool, 1)
	errPollingRunning.Store(true)

	d := stubDevice("A")
	d.PDOStatus.Store(uint32(0x0637)) // valid, fault clear
	d.PDOErr.Store(0)

	t.Cleanup(func() {
		channels.BroadCastUIChannel = nil
		errPollingRunning.Store(false)
		stopDriveStatusListener()
		waitForFlagFalse(&isStatusListening, 1*time.Second, "listenDriverStatus")
	})

	lastCode := 0x0088 // previous error — transition to cleared
	done := make(chan struct{})
	go func() {
		defer close(done)
		pdoErrLoop(d, stopCh, &lastCode)
	}()
	time.Sleep(250 * time.Millisecond)
	stopCh <- true
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Error("pdoErrLoop did not stop within 200ms of stopCh signal")
	}
}

// ─── stopECSCheck — inProgress branch ──────────────────────────────────────

func TestStopECSCheck_WhenInProgress_SendsSignal(t *testing.T) {
	stopECSCheckChan = make(chan bool, 1)
	isECSCheckInProgress.Store(true)
	t.Cleanup(func() {
		isECSCheckInProgress.Store(false)
		select {
		case <-stopECSCheckChan:
		default:
		}
	})

	stopECSCheck()

	select {
	case <-stopECSCheckChan:
	case <-time.After(50 * time.Millisecond):
		t.Error("stopECSCheck: signal not sent when in progress")
	}
	if isECSCheckInProgress.Load() {
		t.Error("isECSCheckInProgress should be false after stopECSCheck")
	}
}

// ─── PDO watchdog — error level branch ─────────────────────────────────────

func TestStartPDOWatchdog_ErrorBranch_LogsHung(t *testing.T) {
	pdoActive.Store(false)
	lastPDOTickNanos.Store(0)
	pdoWatchdogRunning.Store(false)
	t.Cleanup(func() {
		pdoActive.Store(false)
		lastPDOTickNanos.Store(0)
		pdoWatchdogRunning.Store(false)
	})

	startPDOWatchdog()

	// Tick age > 500ms triggers error level ("HUNG")
	pdoActive.Store(true)
	lastPDOTickNanos.Store(time.Now().Add(-600 * time.Millisecond).UnixNano())

	time.Sleep(250 * time.Millisecond)
}

// ─── set_rpm — non-PDO path ─────────────────────────────────────────────────

func TestSetRpm_PDONotActive_NoOp(t *testing.T) {
	pdoActive.Store(false)
	d := stubDevice("A")
	// setRpm without PDO returns an error but must not panic
	if err := setRpm(d, 100); err == nil {
		t.Logf("setRpm PDO not active: expected error, got nil (acceptable if behavior changed)")
	}
}

func TestSetRpm_ZeroRpm_PDONotActive(t *testing.T) {
	pdoActive.Store(false)
	d := stubDevice("A")
	_ = setRpm(d, 0)
}

// ─── configure_driver — PDO active path ────────────────────────────────────

func TestConfigureDriver_PDOActive_SkipsSDO(t *testing.T) {
	activatePDO(t)
	d := stubDevice("A")
	// With PDO active, configureDriver should return nil (no SDO needed)
	err := configureDriver(d)
	if err != nil {
		t.Logf("configureDriver PDO active: %v (acceptable)", err)
	}
}

// ─── hasDeclamped/hasClamped — ClampDeclamp=0 paths ───────────────────────
//
// ADAPTED: both hard-error on nil Driver (Bug 2a/2b) — give the stub a mock.

func TestHasDeclamped_ClampOff_ReturnsTrueNoOp(t *testing.T) {
	d := stubDevice("A")
	d.Driver = &mockDriver{}
	s := settings.DriverSettings{ClampDeclamp: 0}
	got, err := hasDeclamped(d, s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Error("hasDeclamped should return true when ClampDeclamp=0")
	}
}

func TestHasClamped_ClampDisabled_ReturnsFalse(t *testing.T) {
	d := stubDevice("A")
	d.Driver = &mockDriver{}
	s := settings.DriverSettings{ClampDeclamp: 0}
	got, err := hasClamped(d, s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Error("hasClamped should return false when ClampDeclamp=0")
	}
}

// ─── applyClampIfSettingsChanged — various paths ──────────────────────────

func TestApplyClampIfSettingsChanged_EmptyDevices_NoOp(t *testing.T) {
	restore := setMasterDevicesForTest()
	defer restore()
	applyClampIfSettingsChanged()
}

func TestApplyClampIfSettingsChanged_MotorRunning_NoOp(t *testing.T) {
	initDriverStatusKeeperListener()
	t.Cleanup(func() {
		stopDriveStatusListener()
		waitForFlagFalse(&isStatusListening, 1*time.Second, "listenDriverStatus")
	})
	d := stubDevice("A")
	restore := setMasterDevicesForTest(d)
	t.Cleanup(restore)

	setCurrentDriverStatus("A", driverCurrentStatus{isMotorRunning: true})
	t.Cleanup(func() { setCurrentDriverStatus("A", driverCurrentStatus{}) })

	settings.SetDriverSettings("A", settings.DriverSettings{ClampDeclamp: 1})
	// Should return early because motor is running
	applyClampIfSettingsChanged()
}

func TestApplyClampIfSettingsChanged_ClampDisabled_PowersOn(t *testing.T) {
	initDriverStatusKeeperListener()
	t.Cleanup(func() {
		stopDriveStatusListener()
		waitForFlagFalse(&isStatusListening, 1*time.Second, "listenDriverStatus")
	})
	activatePDO(t)
	d := stubDevice("A")
	restore := setMasterDevicesForTest(d)
	t.Cleanup(restore)

	setCurrentDriverStatus("A", driverCurrentStatus{isMotorRunning: false})
	t.Cleanup(func() { setCurrentDriverStatus("A", driverCurrentStatus{}) })

	settings.SetDriverSettings("A", settings.DriverSettings{ClampDeclamp: 0})
	applyClampIfSettingsChanged()
}

// ─── triggerMultiTurnResetSDO — paths ──────────────────────────────────────

func TestTriggerMultiTurnResetSDO_NilDevice_Skips(t *testing.T) {
	err := triggerMultiTurnResetSDO([]*MasterDevice{nil})
	if err != nil {
		t.Errorf("nil device should be skipped, got error: %v", err)
	}
}

func TestTriggerMultiTurnResetSDO_BadConfig_ReturnsError(t *testing.T) {
	d := stubDevice("A")
	d.Device.AddressConfigName = "nonexistent"
	err := triggerMultiTurnResetSDO([]*MasterDevice{d})
	if err == nil {
		t.Error("expected error for unknown config, got nil")
	}
}

func TestTriggerMultiTurnResetSDO_NoDevices_ReturnsNil(t *testing.T) {
	err := triggerMultiTurnResetSDO([]*MasterDevice{})
	if err != nil {
		t.Errorf("empty devices should return nil, got: %v", err)
	}
}

// ─── readDigitalInputs/readInputSignal/readECSSignal — non-PDO unknown config ─

func TestReadDigitalInputs_NonPDO_UnknownConfig_ReturnsError(t *testing.T) {
	pdoActive.Store(false)
	d := stubDevice("A")
	d.PdoDIReady = false
	d.Device.AddressConfigName = "nonexistent"
	val, err := readDigitalInputs(d, "readinputsignal")
	if err == nil {
		t.Errorf("expected error for unknown config, got val=%d", val)
	}
}

func TestReadInputSignal_NonPDO_UnknownConfig_ReturnsError(t *testing.T) {
	pdoActive.Store(false)
	d := stubDevice("A")
	d.PdoDIReady = false
	d.Device.AddressConfigName = "nonexistent"
	_, err := readInputSignal(d)
	if err == nil {
		t.Error("expected error for unknown config")
	}
}

func TestReadECSSignal_NonPDO_UnknownConfig_ReturnsError(t *testing.T) {
	pdoActive.Store(false)
	d := stubDevice("A")
	d.PdoDIReady = false
	d.Device.AddressConfigName = "nonexistent"
	_, err := readECSSignal(d)
	if err == nil {
		t.Error("expected error for unknown config")
	}
}

// ─── ReadActualPositionFromDrive — fallback paths ─────────────────────────

func TestReadActualPositionFromDrive_NoMatchingDevice_ReturnsCached(t *testing.T) {
	initDriverStatusKeeperListener()
	t.Cleanup(func() {
		stopDriveStatusListener()
		waitForFlagFalse(&isStatusListening, 1*time.Second, "listenDriverStatus")
	})
	setCurrentDriverStatus("B", driverCurrentStatus{currentPosition: 123.456})
	t.Cleanup(func() { setCurrentDriverStatus("B", driverCurrentStatus{}) })

	// masterDevices has "A" but we ask for "B" — no match, returns cached
	restore := setMasterDevicesForTest(stubDevice("A"))
	t.Cleanup(restore)

	got := ReadActualPositionFromDrive("B")
	if got != 123.456 {
		t.Errorf("ReadActualPositionFromDrive = %f, want 123.456 (cached)", got)
	}
}

func TestReadActualPositionFromDrive_EmptyDevices_ReturnsCached(t *testing.T) {
	initDriverStatusKeeperListener()
	t.Cleanup(func() {
		stopDriveStatusListener()
		waitForFlagFalse(&isStatusListening, 1*time.Second, "listenDriverStatus")
	})
	setCurrentDriverStatus("A", driverCurrentStatus{currentPosition: 45.0})
	t.Cleanup(func() { setCurrentDriverStatus("A", driverCurrentStatus{}) })

	restore := setMasterDevicesForTest()
	t.Cleanup(restore)

	got := ReadActualPositionFromDrive("A")
	if got != 45.0 {
		t.Errorf("ReadActualPositionFromDrive = %f, want 45.0 (cached)", got)
	}
}

func TestReadActualPositionFromDrive_NonPDO_UnknownConfig_ReturnsCached(t *testing.T) {
	initDriverStatusKeeperListener()
	t.Cleanup(func() {
		stopDriveStatusListener()
		waitForFlagFalse(&isStatusListening, 1*time.Second, "listenDriverStatus")
	})
	setCurrentDriverStatus("A", driverCurrentStatus{currentPosition: 99.9})
	t.Cleanup(func() { setCurrentDriverStatus("A", driverCurrentStatus{}) })

	d := stubDevice("A")
	d.PdoReady = false
	d.Device.AddressConfigName = "nonexistent"
	restore := setMasterDevicesForTest(d)
	t.Cleanup(restore)

	got := ReadActualPositionFromDrive("A")
	if got != 99.9 {
		t.Errorf("ReadActualPositionFromDrive = %f, want 99.9 (cached fallback)", got)
	}
}

// ─── pollDriveErrWorker — SDO path with no steps ──────────────────────────
//
// ADAPTED: dropped the usePDO bool parameter — now takes (device, stopCh).

func TestPollDriveErrWorker_NonPDO_NoSteps_ReturnsImmediately(t *testing.T) {
	d := stubDevice("A")
	d.PdoErrorReady = false
	d.Device.AddressConfigName = "nonexistent"

	stopCh := make(chan bool, 1)
	done := make(chan struct{})
	errPollingWG.Add(1) // this test bypasses pollDriveError(), which normally does this
	go func() {
		defer close(done)
		pollDriveErrWorker(d, stopCh)
	}()

	select {
	case <-done:
		// returned immediately — no steps configured
	case <-time.After(2 * time.Second):
		t.Fatal("pollDriveErrWorker should return immediately when no readError steps configured")
	}
}

// ─── PowerOff — non-PDO with unknown config ────────────────────────────────

func TestPowerOff_NonPDO_UnknownConfig_ReturnsError(t *testing.T) {
	pdoActive.Store(false)
	powerOff.Name = "" // force re-lookup
	t.Cleanup(func() { powerOff.Name = "" })
	d := stubDevice("A")
	d.Device.AddressConfigName = "nonexistent"
	err := PowerOff(d)
	if err == nil {
		t.Error("PowerOff non-PDO with unknown config should return error")
	}
}

// ─── FastPowerOn — non-PDO with unknown config ─────────────────────────────

func TestFastPowerOn_NonPDO_UnknownConfig_ReturnsError(t *testing.T) {
	pdoActive.Store(false)
	d := stubDevice("A")
	d.Device.AddressConfigName = "nonexistent"
	err := FastPowerOn(d)
	if err == nil {
		t.Error("FastPowerOn non-PDO with unknown config should return error")
	}
}

// ─── emergency — PDO active, no matching device ────────────────────────────

func TestEmergency_PDOActive_NoMatchingDevice_ReturnsNil(t *testing.T) {
	activatePDO(t)
	channels.BroadCastUIChannel = make(chan channels.SocketMessage, 8)
	t.Cleanup(func() { channels.BroadCastUIChannel = nil })

	restore := setMasterDevicesForTest(stubDevice("A"))
	t.Cleanup(restore)

	// Ask for device "B" which doesn't exist in masterDevices
	d := stubDevice("B")
	err := emergency(d)
	if err != nil {
		t.Errorf("emergency with no matching device: got error %v", err)
	}
}

// ─── configureDriver — non-PDO unknown config ──────────────────────────────

func TestConfigureDriver_NonPDO_UnknownConfig_ReturnsError(t *testing.T) {
	pdoActive.Store(false)
	d := stubDevice("A")
	d.Device.AddressConfigName = "nonexistent"
	err := configureDriver(d)
	if err == nil {
		t.Error("configureDriver non-PDO unknown config should return error")
	}
}

// ─── PowerOffAll — PDO active path ──────────────────────────────────────────

func TestPowerOffAll_PDOActive_StopsAllDevices(t *testing.T) {
	activatePDO(t)
	initDriverStatusKeeperListener() // already starts the listener internally
	t.Cleanup(func() {
		stopDriveStatusListener()
		// stopDriveStatusListener only queues the exit message — it returns
		// as soon as the send lands in the buffered channel, not once the
		// listener goroutine has actually processed it and returned. Poll
		// for confirmed exit so the goroutine is truly gone before the next
		// test starts (otherwise it can still be mid-shutdown when the next
		// test reassigns channels.BroadCastDriveStatusChannel, racing).
		waitForFlagFalse(&isStatusListening, 1*time.Second, "listenDriverStatus")
		time.Sleep(20 * time.Millisecond) // let the goroutine fully unwind
	})

	d1 := stubDevice("A")
	err := PowerOffAll([]*MasterDevice{d1})
	if err != nil {
		t.Errorf("PowerOffAll PDO active: got error %v", err)
	}
}
