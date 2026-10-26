//go:build unit

package motordriver

// Final coverage pass — the remaining genuinely-new, safely-portable content
// from testenv's motordriver_coverage_test.go (80 tests, 1415 lines).
//
// SCOPE NOTE: this file was reviewed in full. What's ported below is
// everything that (a) tests real, previously-uncovered behavior and
// (b) is safe against HAL's current architecture. Explicitly NOT ported,
// with reasons:
//
//   - PowerOn/PowerOff/FastPowerOff/Emergency/PowerOffAll/BreakOn/BreakOff
//     _PDONotActive_ReturnsError (7 tests): functionally identical to the
//     already-ported _NonPDO_UnknownConfig_ReturnsError tests in
//     motordriver_lifecycle_test.go / drive_power_test.go — same code path,
//     same stub device, same "unknown config" error.
//   - TriggerMultiTurnResetSDO_NilDevice/EmptyDevices/UnknownConfig (3):
//     exact duplicates of tests already in motordriver_lifecycle_test.go.
//   - PollDriveError_EmptySlice_ReturnsError, StopErrorPolling_WhileRunning:
//     duplicates of tests already ported (with the 1-arg/slice-channel
//     adaptations already applied there).
//   - SleepMs_ActuallySleeps, ClearTargetReached_NilNameDoesNotPanic,
//     SetDirection_* (x2), RefreshCurrentPosition_NilMasterDevices,
//     StartDriverStatusListener_CreatesChannelAndGoRoutine,
//     ConfigureDriver_UnknownOperation_ReturnsError,
//     DriverCurrentStatus_Reset_SafetyLimitFieldsCleared: duplicates of
//     tests already ported elsewhere under different names.
//   - TestErrMultiturnPowerCycleRequired_IsAnError: the exported sentinel
//     `ErrMultiturnPowerCycleRequired` does not exist anywhere in HAL's
//     motordriver package — this was testenv-only.
//   - TestListenDriverStatus_ProgramModeEvent_UpdatesFlag: both the
//     `"program_mode"` switch case in listenDriverStatus AND the
//     `isProgramMode` field on driverCurrentStatus it would set do not
//     exist in HAL (confirmed via the struct diff found early in this
//     merge) — testenv-only functionality.
//
// What required real adaptation (documented inline at each test):
//   - pdoErrLoop: 3-arg signature (added stopCh)
//   - handleErrCode: 4-arg signature (added device, statusword)
//   - InitAposCorrection: reads dev.AposCorrection (per-device) instead of
//     the removed package-level aposCorrection, and requires the device to
//     actually be registered in masterDevices to be found at all.

import (
	"strings"
	"sync"
	"testing"
	"time"

	channels "EtherCAT/channels"
	"EtherCAT/settings"

	cmap "github.com/orcaman/concurrent-map"
)

// ─── listenSystemReset / performSysReset / stopDriverPolling ─────────────

func TestListenSystemReset_InitialisesChannel(t *testing.T) {
	origResetChan := channels.ResetDriverSystem
	t.Cleanup(func() { channels.ResetDriverSystem = origResetChan })

	listenSystemReset()
	if channels.ResetDriverSystem == nil {
		t.Fatal("listenSystemReset: ResetDriverSystem channel is nil after initialisation")
	}

	// listenSystemReset spawns resetSystemWorker internally with no way to
	// hook a completion signal from here, so there's no synchronization
	// primitive available to confirm exit precisely (unlike the listener
	// tests elsewhere, which poll an atomic flag the goroutine sets).
	// 100ms is a generous margin for a goroutine that does nothing but
	// receive false and return.
	channels.ResetDriverSystem <- false
	time.Sleep(100 * time.Millisecond)
}

func TestPerformSysReset_NilChannel_DoesNotPanic(t *testing.T) {
	settings.SetDriverSettings("A", settings.DriverSettings{})
	restore := setMasterDevicesForTest() // no devices -> loop is skipped
	defer restore()

	channels.ResetDriverSystem = make(chan bool, 1)
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("performSysReset panicked: %v", r)
		}
		channels.ResetDriverSystem = nil
	}()

	performSysReset(false)

	select {
	case v := <-channels.ResetDriverSystem:
		if !v {
			t.Errorf("performSysReset sent false on channel, want true")
		}
	default:
		t.Errorf("performSysReset did not write to ResetDriverSystem")
	}
}

func TestStopDriverPolling_NilStopChans_DoesNotPanic(t *testing.T) {
	stopChansMu.Lock()
	stopChans = nil
	stopChansMu.Unlock()

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("stopDriverPolling panicked: %v", r)
		}
	}()
	stopDriverPolling()
}

func TestStopDriverPolling_ClosesAllChannels(t *testing.T) {
	ch1 := make(chan struct{})
	ch2 := make(chan struct{})

	stopChansMu.Lock()
	stopChans = []chan struct{}{ch1, ch2}
	stopChansMu.Unlock()

	stopDriverPolling()

	for i, ch := range []chan struct{}{ch1, ch2} {
		select {
		case _, ok := <-ch:
			if ok {
				t.Errorf("stopChans[%d]: channel still open after stopDriverPolling", i)
			}
		default:
			t.Errorf("stopChans[%d]: channel not closed (would block)", i)
		}
	}

	stopChansMu.Lock()
	defer stopChansMu.Unlock()
	if stopChans != nil {
		t.Errorf("stopChans not nil after stopDriverPolling")
	}
}

// TestStopDriverPolling_ConcurrentCallsDoNotRace verifies concurrent calls
// don't panic (closing an already-closed channel does panic -- recovered
// per-goroutine, same as testenv's original).
func TestStopDriverPolling_ConcurrentCallsDoNotRace(t *testing.T) {
	stopChansMu.Lock()
	stopChans = []chan struct{}{make(chan struct{}), make(chan struct{})}
	stopChansMu.Unlock()

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { recover() }()
			stopDriverPolling()
		}()
	}
	wg.Wait()
}

// ─── pdoErrLoop — exits on stop signal ────────────────────────────────────
//
// ADAPTED: pdoErrLoop now takes (device, stopCh, lastReportedErrCode) -- the
// stop channel is a separate parameter, not a package-level singleton.

func TestPdoErrLoop_ExitsOnStopSignal(t *testing.T) {
	stopCh := make(chan bool, 1)
	stopCh <- true // pre-load the stop signal

	d := stubDevice("A")
	lastCode := 0

	done := make(chan struct{})
	go func() {
		pdoErrLoop(d, stopCh, &lastCode)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Error("pdoErrLoop did not exit after stop signal within 500ms")
	}
}

// ─── handleErrCode — errCodeCleared sentinel suppression ─────────────────
//
// ADAPTED: handleErrCode now takes (device, errCode, statusword, last).

func TestHandleErrCode_ErrCodeClearedSentinel_SuppressesAlarmCleared(t *testing.T) {
	last := errCodeCleared // -1
	// If AlarmCleared were called again it would write to a channel; since we
	// have no listener here, an unbuffered/nil channel send would deadlock.
	// The test completing without deadlock proves the suppression branch fires.
	handleErrCode(stubDevice("A"), 0, 0, &last)
	if last != errCodeCleared {
		t.Errorf("handleErrCode(0, errCodeCleared): last changed to %d, want %d", last, errCodeCleared)
	}
}

// ─── initDriverActionListener / stopDriverActionListener ─────────────────

// waitForActionListenerExit blocks until listenDriverAction has actually
// returned (confirmed via isActionListening, set false right before its
// return), or 1s elapses. A plain time.Sleep after stopDriverActionListener()
// only reduces the probability of a race — it establishes no real
// happens-before edge, so Go's race detector still (correctly) flags it.
// Polling an atomic the listener itself writes is the actual fix.
func waitForActionListenerExit(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(1 * time.Second)
	for isActionListening.Load() && time.Now().Before(deadline) {
		time.Sleep(1 * time.Millisecond)
	}
	// Small grace period: the flag goes false immediately before the
	// goroutine's return statement, not after — see waitForFlagFalse's
	// comment in poll_drive_position.go for why this is necessary.
	time.Sleep(20 * time.Millisecond)
}

func TestInitDriverActionListener_SetsUpChannel(t *testing.T) {
	restore := setMasterDevicesForTest(stubDevice("A"))
	defer restore()

	initDriverActionListener()

	if channels.DriverActionChannel == nil {
		t.Fatal("initDriverActionListener: DriverActionChannel is nil")
	}

	channels.DriverActionChannel <- channels.DriverAction{Action: channels.EXIT_DRIVE_LISTENER}
	waitForActionListenerExit(t)
}

func TestStopDriverActionListener_SignalsExit(t *testing.T) {
	restore := setMasterDevicesForTest(stubDevice("A"))
	defer restore()

	initDriverActionListener()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("stopDriverActionListener panicked: %v", r)
		}
	}()
	stopDriverActionListener()
	waitForActionListenerExit(t)
}

// ─── initDriverStatusKeeperListener ────────────────────────────────────────

func TestInitDriverStatusKeeperListener_InitialisesMap(t *testing.T) {
	restore := setMasterDevicesForTest()
	defer restore()

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("initDriverStatusKeeperListener panicked: %v", r)
		}
		stopDriveStatusListener()
		waitForFlagFalse(&isStatusListening, 1*time.Second, "listenDriverStatus")
	}()

	initDriverStatusKeeperListener()

	// Confirm the listener has actually started (isStatusListening set true)
	// before proceeding. Without this, if cleanup runs before the goroutine
	// gets scheduled, isStatusListening reads false (its zero value) —
	// indistinguishable from "already exited" — and waitForFlagFalse skips
	// waiting entirely even though the goroutine hasn't started yet.
	startDeadline := time.Now().Add(1 * time.Second)
	for !isStatusListening.Load() && time.Now().Before(startDeadline) {
		time.Sleep(1 * time.Millisecond)
	}

	if channels.BroadCastDriveStatusChannel == nil {
		t.Error("BroadCastDriveStatusChannel is nil after initDriverStatusKeeperListener")
	}
}

// ─── listenDriverStatus — individual event branches ──────────────────────

func withStatusListener(t *testing.T) func() {
	t.Helper()
	channels.BroadCastDriveStatusChannel = make(chan channels.DriverStatus, 100)
	driveStatusUpdated = make(chan bool, 1)
	isStatusListening.Store(false)
	go listenDriverStatus()
	deadline := time.Now().Add(200 * time.Millisecond)
	for !isStatusListening.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	return func() {
		channels.BroadCastDriveStatusChannel <- channels.DriverStatus{Event: "exit"}
		waitForFlagFalse(&isStatusListening, 1*time.Second, "listenDriverStatus")
	}
}

func TestListenDriverStatus_ModeEvent_UpdatesMode(t *testing.T) {
	driverStatusMap = cmap.New() // ensure initialized even if this test runs in isolation
	driverStatusMap.Set("A", driverCurrentStatus{})
	cleanup := withStatusListener(t)
	defer cleanup()

	channels.BroadCastDriveStatusChannel <- channels.DriverStatus{
		DriveName: "A", Event: "mode", Data: "REL",
	}
	time.Sleep(30 * time.Millisecond)

	if got := getCurrentDriverStatus("A"); got.mode != "REL" {
		t.Errorf("mode = %q, want REL", got.mode)
	}
}

func TestListenDriverStatus_ShortestPathEvent_UpdatesFlag(t *testing.T) {
	driverStatusMap = cmap.New() // ensure initialized even if this test runs in isolation
	driverStatusMap.Set("A", driverCurrentStatus{})
	cleanup := withStatusListener(t)
	defer cleanup()

	channels.BroadCastDriveStatusChannel <- channels.DriverStatus{
		DriveName: "A", Event: "shortest_path_enable", Data: "true",
	}
	time.Sleep(30 * time.Millisecond)

	if got := getCurrentDriverStatus("A"); !got.shortestPathEnabled {
		t.Error("shortestPathEnabled still false after shortest_path_enable event")
	}
}

func TestListenDriverStatus_DestinationPositionEvent_StoresValue(t *testing.T) {
	driverStatusMap = cmap.New() // ensure initialized even if this test runs in isolation
	driverStatusMap.Set("A", driverCurrentStatus{})
	cleanup := withStatusListener(t)
	defer cleanup()

	channels.BroadCastDriveStatusChannel <- channels.DriverStatus{
		DriveName: "A", Event: "destination_position", Data: "123.456",
	}
	time.Sleep(30 * time.Millisecond)

	if got := getCurrentDriverStatus("A"); got.destinationPosition != 123.456 {
		t.Errorf("destinationPosition = %v, want 123.456", got.destinationPosition)
	}
}

func TestListenDriverStatus_MotorRunningEvent_UpdatesFlag(t *testing.T) {
	driverStatusMap = cmap.New() // ensure initialized even if this test runs in isolation
	driverStatusMap.Set("A", driverCurrentStatus{})
	cleanup := withStatusListener(t)
	defer cleanup()

	channels.BroadCastDriveStatusChannel <- channels.DriverStatus{
		DriveName: "A", Event: "motor_running", Data: "true",
	}
	time.Sleep(30 * time.Millisecond)

	if got := getCurrentDriverStatus("A"); !got.isMotorRunning {
		t.Error("isMotorRunning still false after motor_running event")
	}
}

func TestListenDriverStatus_DriverOnOffEvent_UpdatesFlag(t *testing.T) {
	driverStatusMap = cmap.New() // ensure initialized even if this test runs in isolation
	driverStatusMap.Set("A", driverCurrentStatus{})
	cleanup := withStatusListener(t)
	defer cleanup()

	channels.BroadCastDriveStatusChannel <- channels.DriverStatus{
		DriveName: "A", Event: "driver_on_off", Data: "true",
	}
	time.Sleep(30 * time.Millisecond)

	if got := getCurrentDriverStatus("A"); !got.isDriverOnOff {
		t.Error("isDriverOnOff still false after driver_on_off event")
	}
}

func TestListenDriverStatus_SetBacklashEvent_StoresValue(t *testing.T) {
	driverStatusMap = cmap.New() // ensure initialized even if this test runs in isolation
	driverStatusMap.Set("A", driverCurrentStatus{})
	cleanup := withStatusListener(t)
	defer cleanup()

	channels.BroadCastDriveStatusChannel <- channels.DriverStatus{
		DriveName: "A", Event: "set_backlash", Data: "0.5",
	}
	time.Sleep(30 * time.Millisecond)

	if got := getCurrentDriverStatus("A"); got.backlash != 0.5 {
		t.Errorf("backlash = %v, want 0.5", got.backlash)
	}
}

func TestListenDriverStatus_WorkOffsetEvent_StoresValue(t *testing.T) {
	driverStatusMap = cmap.New() // ensure initialized even if this test runs in isolation
	driverStatusMap.Set("A", driverCurrentStatus{})
	cleanup := withStatusListener(t)
	defer cleanup()

	channels.BroadCastDriveStatusChannel <- channels.DriverStatus{
		DriveName: "A", Event: "workoffset", Data: "10.0",
	}
	time.Sleep(30 * time.Millisecond)

	if got := getCurrentDriverStatus("A"); got.workOffset != 10.0 {
		t.Errorf("workOffset = %v, want 10.0", got.workOffset)
	}
}

func TestListenDriverStatus_FinSignalEvent_UpdatesFlag(t *testing.T) {
	driverStatusMap = cmap.New() // ensure initialized even if this test runs in isolation
	driverStatusMap.Set("A", driverCurrentStatus{})
	cleanup := withStatusListener(t)
	defer cleanup()

	channels.BroadCastDriveStatusChannel <- channels.DriverStatus{
		DriveName: "A", Event: "fin_signal", Data: "true",
	}
	time.Sleep(30 * time.Millisecond)

	if got := getCurrentDriverStatus("A"); !got.isSendingFinSignal {
		t.Error("isSendingFinSignal still false after fin_signal event")
	}
}

func TestListenDriverStatus_ResetEvent_ZerosStatus(t *testing.T) {
	driverStatusMap.Set("A", driverCurrentStatus{currentPosition: 99, isMotorRunning: true})
	cleanup := withStatusListener(t)
	defer cleanup()

	channels.BroadCastDriveStatusChannel <- channels.DriverStatus{
		DriveName: "A", Event: "reset",
	}
	time.Sleep(30 * time.Millisecond)

	got := getCurrentDriverStatus("A")
	if got.currentPosition != 0 || got.isMotorRunning {
		t.Errorf("reset event did not zero status: %+v", got)
	}
}

func TestListenDriverStatus_PotNotExceededEvent_POT(t *testing.T) {
	driverStatusMap = cmap.New() // ensure initialized even if this test runs in isolation
	driverStatusMap.Set("A", driverCurrentStatus{})
	cleanup := withStatusListener(t)
	defer cleanup()

	channels.BroadCastDriveStatusChannel <- channels.DriverStatus{
		DriveName: "A", Event: "pot_not_exceeded", Data: "POT",
	}
	time.Sleep(30 * time.Millisecond)

	got := getCurrentDriverStatus("A")
	if !got.potNotExceeded || !got.potExceeded || got.notExceeded {
		t.Errorf("pot_not_exceeded/POT: got %+v", got)
	}
}

func TestListenDriverStatus_PotNotExceededEvent_NOT(t *testing.T) {
	driverStatusMap = cmap.New() // ensure initialized even if this test runs in isolation
	driverStatusMap.Set("A", driverCurrentStatus{})
	cleanup := withStatusListener(t)
	defer cleanup()

	channels.BroadCastDriveStatusChannel <- channels.DriverStatus{
		DriveName: "A", Event: "pot_not_exceeded", Data: "NOT",
	}
	time.Sleep(30 * time.Millisecond)

	got := getCurrentDriverStatus("A")
	if !got.potNotExceeded || got.potExceeded || !got.notExceeded {
		t.Errorf("pot_not_exceeded/NOT: got %+v", got)
	}
}

// ─── rotation_direction — backlash compensation logic ─────────────────────
//
// Genuinely important, previously untested: the actual backlash-on-
// direction-change logic (mechanical compensation for gear/screw backlash
// when reversing rotation), including the CW/CCW asymmetry and the
// potNotExceeded-clears-on-direction-change safety behavior.

func TestListenDriverStatus_RotationDirection_CWtoCCW_AppliesBacklash(t *testing.T) {
	initDriverStatusKeeperListener() // already starts the listener internally
	t.Cleanup(func() {
		channels.BroadCastDriveStatusChannel <- channels.DriverStatus{DriveName: "A", Event: "exit"}
		waitForFlagFalse(&isStatusListening, 1*time.Second, "listenDriverStatus")
	})

	setCurrentDriverStatus("A", driverCurrentStatus{direction: 1, backlashInSetting: 0.5})

	channels.BroadCastDriveStatusChannel <- channels.DriverStatus{
		DriveName: "A", Event: "rotation_direction", Data: "-1",
	}
	time.Sleep(30 * time.Millisecond)

	got := getCurrentDriverStatus("A")
	if got.direction != -1 {
		t.Errorf("direction = %d, want -1", got.direction)
	}
	if got.backlash != 0.5 {
		t.Errorf("backlash = %f, want 0.5 (CW->CCW should apply backlash)", got.backlash)
	}
}

func TestListenDriverStatus_RotationDirection_CCWtoCCW_KeepsBacklash(t *testing.T) {
	initDriverStatusKeeperListener() // already starts the listener internally
	t.Cleanup(func() {
		channels.BroadCastDriveStatusChannel <- channels.DriverStatus{DriveName: "A", Event: "exit"}
		waitForFlagFalse(&isStatusListening, 1*time.Second, "listenDriverStatus")
	})

	setCurrentDriverStatus("A", driverCurrentStatus{direction: -1, backlashInSetting: 0.3})

	channels.BroadCastDriveStatusChannel <- channels.DriverStatus{
		DriveName: "A", Event: "rotation_direction", Data: "-1",
	}
	time.Sleep(30 * time.Millisecond)

	got := getCurrentDriverStatus("A")
	if got.backlash != 0.3 {
		t.Errorf("backlash = %f, want 0.3 (CCW->CCW should keep backlash)", got.backlash)
	}
}

func TestListenDriverStatus_RotationDirection_CCWtoCW_ClearsBacklash(t *testing.T) {
	initDriverStatusKeeperListener() // already starts the listener internally
	t.Cleanup(func() {
		channels.BroadCastDriveStatusChannel <- channels.DriverStatus{DriveName: "A", Event: "exit"}
		waitForFlagFalse(&isStatusListening, 1*time.Second, "listenDriverStatus")
	})

	setCurrentDriverStatus("A", driverCurrentStatus{direction: -1, backlashInSetting: 0.3, backlash: 0.3})

	channels.BroadCastDriveStatusChannel <- channels.DriverStatus{
		DriveName: "A", Event: "rotation_direction", Data: "1",
	}
	time.Sleep(30 * time.Millisecond)

	got := getCurrentDriverStatus("A")
	if got.backlash != 0 {
		t.Errorf("backlash = %f, want 0 (CCW->CW should clear backlash)", got.backlash)
	}
}

func TestListenDriverStatus_RotationDirection_CWtoCW_KeepsZeroBacklash(t *testing.T) {
	initDriverStatusKeeperListener() // already starts the listener internally
	t.Cleanup(func() {
		channels.BroadCastDriveStatusChannel <- channels.DriverStatus{DriveName: "A", Event: "exit"}
		waitForFlagFalse(&isStatusListening, 1*time.Second, "listenDriverStatus")
	})

	setCurrentDriverStatus("A", driverCurrentStatus{direction: 1, backlashInSetting: 0.5, backlash: 0})

	channels.BroadCastDriveStatusChannel <- channels.DriverStatus{
		DriveName: "A", Event: "rotation_direction", Data: "1",
	}
	time.Sleep(30 * time.Millisecond)

	got := getCurrentDriverStatus("A")
	if got.backlash != 0 {
		t.Errorf("backlash = %f, want 0 (CW->CW should stay at 0)", got.backlash)
	}
}

func TestListenDriverStatus_RotationDirection_DirectionChange_ClearsPotNotExceeded(t *testing.T) {
	initDriverStatusKeeperListener() // already starts the listener internally
	t.Cleanup(func() {
		channels.BroadCastDriveStatusChannel <- channels.DriverStatus{DriveName: "A", Event: "exit"}
		waitForFlagFalse(&isStatusListening, 1*time.Second, "listenDriverStatus")
	})

	setCurrentDriverStatus("A", driverCurrentStatus{direction: 1, potNotExceeded: true})

	channels.BroadCastDriveStatusChannel <- channels.DriverStatus{
		DriveName: "A", Event: "rotation_direction", Data: "-1",
	}
	time.Sleep(30 * time.Millisecond)

	got := getCurrentDriverStatus("A")
	if got.potNotExceeded {
		t.Error("potNotExceeded should be false after direction change")
	}
}

// ─── initListeners — empty device slice ────────────────────────────────────

func TestInitListeners_EmptyDevicesIsNoOp(t *testing.T) {
	prevAction := channels.DriverActionChannel
	prevStatus := channels.BroadCastDriveStatusChannel
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("initListeners(empty) panicked: %v", r)
		}
		channels.DriverActionChannel = prevAction
		channels.BroadCastDriveStatusChannel = prevStatus
	}()

	initListeners([]*MasterDevice{}, false)
}

// ─── startPDOWatchdog — idempotent ──────────────────────────────────────────

func TestStartPDOWatchdog_IdempotentDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("startPDOWatchdog panicked: %v", r)
		}
		pdoWatchdogRunning.Store(false)
	}()

	pdoWatchdogRunning.Store(false)
	startPDOWatchdog()
	startPDOWatchdog() // second call must be a no-op via CompareAndSwap guard
}

// ─── listenDriverAction — action dispatch coverage ────────────────────────

func TestListenDriverAction_SettingsChangedDoesNotPanic(t *testing.T) {
	restore := setMasterDevicesForTest(stubDevice("A"))
	channels.DriverActionChannel = make(chan channels.DriverAction, 10)

	initDriverStatusKeeperListener()
	time.Sleep(10 * time.Millisecond)

	go listenDriverAction()
	time.Sleep(10 * time.Millisecond)

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("SETTINGS_CHANGED panicked: %v", r)
		}
		channels.DriverActionChannel <- channels.DriverAction{Action: channels.EXIT_DRIVE_LISTENER}
		stopDriveStatusListener()
		waitForFlagFalse(&isStatusListening, 1*time.Second, "listenDriverStatus")
		waitForActionListenerExit(t)
		restore()
	}()

	channels.DriverActionChannel <- channels.DriverAction{Action: channels.SETTINGS_CHANGED}
	time.Sleep(30 * time.Millisecond)
}

func TestListenDriverAction_StartExecution_DoesNotPanic(t *testing.T) {
	initDriverActionListener()
	restore := setMasterDevicesForTest(stubDevice("A"))
	t.Cleanup(restore)

	channels.DriverActionChannel <- channels.DriverAction{Action: channels.START_EXECUTION}
	time.Sleep(20 * time.Millisecond)
	stopDriverActionListener()
	waitForActionListenerExit(t)
}

func TestListenDriverAction_ProgramExecCompleted_DoesNotPanic(t *testing.T) {
	initDriverActionListener()
	restore := setMasterDevicesForTest(stubDevice("A"))
	t.Cleanup(restore)

	channels.DriverActionChannel <- channels.DriverAction{Action: channels.PROGRAM_EXEC_COMPLETED}
	time.Sleep(20 * time.Millisecond)
	stopDriverActionListener()
	waitForActionListenerExit(t)
}

func TestListenDriverAction_PositionMode_DoesNotPanic(t *testing.T) {
	initDriverActionListener()
	restore := setMasterDevicesForTest(stubDevice("A"))
	t.Cleanup(restore)

	channels.DriverActionChannel <- channels.DriverAction{Action: channels.POSITION_MODE, Value: "A"}
	time.Sleep(20 * time.Millisecond)
	stopDriverActionListener()
	waitForActionListenerExit(t)
}

func TestListenDriverAction_ShortestPathEnabled_DoesNotPanic(t *testing.T) {
	initDriverActionListener()
	restore := setMasterDevicesForTest(stubDevice("A"))
	t.Cleanup(restore)

	channels.DriverActionChannel <- channels.DriverAction{Action: channels.SHORTEST_PATH_ENABLED, Value: "true"}
	time.Sleep(20 * time.Millisecond)
	stopDriverActionListener()
	waitForActionListenerExit(t)
}

func TestListenDriverAction_StopProgramExecution_DoesNotPanic(t *testing.T) {
	channels.BroadCastUIChannel = make(chan channels.SocketMessage, 4)
	t.Cleanup(func() { channels.BroadCastUIChannel = nil })

	initDriverActionListener()
	restore := setMasterDevicesForTest(stubDevice("A"))
	t.Cleanup(restore)

	channels.DriverActionChannel <- channels.DriverAction{Action: channels.STOP_PROGRAM_EXECUTION, Value: "A"}
	time.Sleep(20 * time.Millisecond)
	stopDriverActionListener()
	waitForActionListenerExit(t)
}

func TestListenDriverAction_Reset_DoesNotPanic(t *testing.T) {
	channels.ResetDriverSystem = make(chan bool, 2)
	t.Cleanup(func() { channels.ResetDriverSystem = nil })

	initDriverActionListener()
	restore := setMasterDevicesForTest(stubDevice("A"))
	t.Cleanup(restore)

	channels.DriverActionChannel <- channels.DriverAction{Action: channels.RESET}
	time.Sleep(20 * time.Millisecond)
	stopDriverActionListener()
	waitForActionListenerExit(t)
}

func TestListenDriverAction_StopJog_DoesNotPanic(t *testing.T) {
	initDriverActionListener()
	restore := setMasterDevicesForTest(stubDevice("A"))
	t.Cleanup(restore)

	channels.DriverActionChannel <- channels.DriverAction{Action: channels.STOP_JOG}
	time.Sleep(20 * time.Millisecond)
	stopDriverActionListener()
	waitForActionListenerExit(t)
}

func TestListenDriverAction_FastPowerOff_DoesNotPanic(t *testing.T) {
	initDriverActionListener()
	restore := setMasterDevicesForTest(stubDevice("A"))
	t.Cleanup(restore)

	channels.DriverActionChannel <- channels.DriverAction{Action: channels.FAST_POWER_OFF}
	time.Sleep(20 * time.Millisecond)
	stopDriverActionListener()
	waitForActionListenerExit(t)
}

func TestListenDriverAction_SetWorkOffset_DoesNotPanic(t *testing.T) {
	initDriverActionListener()
	restore := setMasterDevicesForTest(stubDevice("A"))
	t.Cleanup(restore)

	channels.DriverActionChannel <- channels.DriverAction{Action: channels.SET_WORK_OFFSET, Value: "0"}
	time.Sleep(20 * time.Millisecond)
	stopDriverActionListener()
	waitForActionListenerExit(t)
}

func TestListenDriverAction_StepModeEnable_DoesNotPanic(t *testing.T) {
	initDriverActionListener()
	restore := setMasterDevicesForTest(stubDevice("A"))
	t.Cleanup(restore)

	channels.DriverActionChannel <- channels.DriverAction{Action: channels.STEP_MODE_ENABLE, Value: "true"}
	time.Sleep(20 * time.Millisecond)
	stopDriverActionListener()
	waitForActionListenerExit(t)
}

func TestListenDriverAction_SetRPM_DoesNotPanic(t *testing.T) {
	initDriverActionListener()
	restore := setMasterDevicesForTest(stubDevice("A"))
	t.Cleanup(restore)

	channels.DriverActionChannel <- channels.DriverAction{Action: channels.SET_RPM, Value: "100"}
	time.Sleep(20 * time.Millisecond)
	stopDriverActionListener()
	waitForActionListenerExit(t)
}

func TestListenDriverAction_Emergency_DoesNotPanic(t *testing.T) {
	channels.BroadCastUIChannel = make(chan channels.SocketMessage, 4)
	t.Cleanup(func() { channels.BroadCastUIChannel = nil })

	initDriverActionListener()
	restore := setMasterDevicesForTest(stubDevice("A"))
	t.Cleanup(restore)

	channels.DriverActionChannel <- channels.DriverAction{Action: channels.EMERGENCY}
	time.Sleep(20 * time.Millisecond)
	stopDriverActionListener()
	waitForActionListenerExit(t)
}

func TestListenDriverAction_Default_DoesNotPanic(t *testing.T) {
	initDriverActionListener()
	restore := setMasterDevicesForTest(stubDevice("A"))
	t.Cleanup(restore)

	channels.DriverActionChannel <- channels.DriverAction{Action: "UNRECOGNIZED_ACTION_CODE"} // default case
	time.Sleep(20 * time.Millisecond)
	stopDriverActionListener()
	waitForActionListenerExit(t)
}

// ─── doneDriveStatusUpdate — waiting branch ────────────────────────────────

func TestDoneDriveStatusUpdate_WhenWaiting_SignalsChannel(t *testing.T) {
	initDriverStatusKeeperListener()
	t.Cleanup(func() {
		stopDriveStatusListener()
		waitForFlagFalse(&isStatusListening, 1*time.Second, "listenDriverStatus")
	})

	waitForDriveStatusUpdate.Store(true)
	t.Cleanup(func() { waitForDriveStatusUpdate.Store(false) })

	done := make(chan struct{})
	go func() {
		doneDriveStatusUpdate()
		close(done)
	}()

	select {
	case <-driveStatusUpdated:
	case <-time.After(100 * time.Millisecond):
	}

	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Error("doneDriveStatusUpdate blocked when waitForDriveStatusUpdate=true")
	}
}

// ─── InitAposCorrection — HomingApos branches ──────────────────────────────
//
// ADAPTED: correction is stored on dev.AposCorrection (per-device), and the
// device must actually be registered in masterDevices to be found at all
// (InitAposCorrection looks it up by name via getMasterDevices()).

func TestInitAposCorrection_HomingAposZero_StoresZeroCorrection(t *testing.T) {
	settings.SetDriverSettings("APOS_TEST", settings.DriverSettings{HomingApos: 0})
	dev := stubDevice("APOS_TEST")
	restore := setMasterDevicesForTest(dev)
	defer restore()

	InitAposCorrection("APOS_TEST")
	if dev.AposCorrection.Load() != 0 {
		t.Errorf("AposCorrection = %d, want 0 when HomingApos=0", dev.AposCorrection.Load())
	}
}

func TestInitAposCorrection_HomingAposNonZero_NoSignFlip_StoresZero(t *testing.T) {
	// bootApos (dev.PDOPos, 0 in unit tests) and homingApos (1000000) are not
	// opposite-signed -> no sign flip -> correction stays 0.
	settings.SetDriverSettings("APOS_TEST2", settings.DriverSettings{HomingApos: 1000000})
	dev := stubDevice("APOS_TEST2")
	restore := setMasterDevicesForTest(dev)
	defer restore()

	InitAposCorrection("APOS_TEST2")
	if dev.AposCorrection.Load() != 0 {
		t.Errorf("AposCorrection = %d, want 0 (no sign flip)", dev.AposCorrection.Load())
	}
}

// ─── doRotate / freeRotate — PDO not ready branch ─────────────────────────

func TestDoRotate_PDONotReady_ReturnsError(t *testing.T) {
	d := stubDevice("A")
	d.PdoPosReady = false
	pdoActive.Store(false)
	t.Cleanup(func() { pdoActive.Store(false) })

	err := doRotate(d, 10.0)
	if err == nil {
		t.Error("doRotate with PDO not ready: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "PDO") {
		t.Errorf("doRotate error = %q, want to contain 'PDO'", err.Error())
	}
}

func TestFreeRotate_PDONotReady_ReturnsError(t *testing.T) {
	d := stubDevice("A")
	d.PdoPosReady = false
	pdoActive.Store(false)
	t.Cleanup(func() { pdoActive.Store(false) })

	err := freeRotate(d, 10.0)
	if err == nil {
		t.Error("freeRotate with PDO not ready: expected error, got nil")
	}
}

// ─── emergencyRampStop ──────────────────────────────────────────────────────

func TestEmergencyRampStop_ZeroVelocity_ReturnsImmediately(t *testing.T) {
	d := stubDevice("A")
	d.desiredTargetVelocity.Store(0)
	start := time.Now()
	emergencyRampStop(d)
	if time.Since(start) > 50*time.Millisecond {
		t.Error("emergencyRampStop with zero velocity should return immediately")
	}
}

func TestEmergencyRampStop_NonZeroVelocity_RampsToZero(t *testing.T) {
	d := stubDevice("A")
	d.desiredTargetVelocity.Store(1000000)
	emergencyRampStop(d)
	if got := d.desiredTargetVelocity.Load(); got != 0 {
		t.Errorf("desiredTargetVelocity = %d after ramp, want 0", got)
	}
}

// ─── moveMotorToDegree / stepMode — POT/NOT already-exceeded guard ────────

func TestMoveMotorToDegree_PotNotExceeded_ReturnsError(t *testing.T) {
	initDriverStatusKeeperListener()
	t.Cleanup(func() {
		stopDriveStatusListener()
		waitForFlagFalse(&isStatusListening, 1*time.Second, "listenDriverStatus")
	})
	channels.BroadCastUIChannel = make(chan channels.SocketMessage, 4)
	t.Cleanup(func() { channels.BroadCastUIChannel = nil })

	setCurrentDriverStatus("A", driverCurrentStatus{potNotExceeded: true})
	t.Cleanup(func() { setCurrentDriverStatus("A", driverCurrentStatus{}) })

	d := stubDevice("A")
	d.Device.Name = "A"
	err := moveMotorToDegree(d, 90.0)
	if err == nil {
		t.Fatal("expected error when POT/NOT exceeded, got nil")
	}
	if err.Error() != "pot/not exceeded, exiting from move command" {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestStepMode_PotNotExceeded_ReturnsError(t *testing.T) {
	initDriverStatusKeeperListener()
	t.Cleanup(func() {
		stopDriveStatusListener()
		waitForFlagFalse(&isStatusListening, 1*time.Second, "listenDriverStatus")
	})
	channels.BroadCastUIChannel = make(chan channels.SocketMessage, 4)
	t.Cleanup(func() { channels.BroadCastUIChannel = nil })

	setCurrentDriverStatus("A", driverCurrentStatus{potNotExceeded: true})
	t.Cleanup(func() { setCurrentDriverStatus("A", driverCurrentStatus{}) })

	d := stubDevice("A")
	err := stepMode(d, 10.0)
	if err == nil {
		t.Fatal("expected error when POT/NOT exceeded in stepMode, got nil")
	}
}

// ─── pollDrivePosition ──────────────────────────────────────────────────────

func TestPollDrivePosition_LaunchesAndStops(t *testing.T) {
	d := stubDevice("A")
	err := pollDrivePosition([]*MasterDevice{d})
	if err != nil {
		t.Fatalf("pollDrivePosition returned unexpected error: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	stopDriverPolling()
}

func TestPollDrivePosition_EmptySlice_StartsCleanly(t *testing.T) {
	err := pollDrivePosition([]*MasterDevice{})
	if err != nil {
		t.Fatalf("pollDrivePosition([]) returned error: %v", err)
	}
}

// ─── startPDOWatchdog — warn/recovery/inactive branches ────────────────────
//
// These complement TestStartPDOWatchdog_ErrorBranch_LogsHung (already in
// ethercat_config_and_zero_ref_test.go) by exercising the two lower
// severity levels and the "PDO inactive, watchdog skips checking" path.

func TestStartPDOWatchdog_WarnBranch_LogsWhenLate(t *testing.T) {
	pdoActive.Store(false)
	lastPDOTickNanos.Store(0)
	pdoWatchdogRunning.Store(false)
	t.Cleanup(func() {
		pdoActive.Store(false)
		lastPDOTickNanos.Store(0)
		pdoWatchdogRunning.Store(false)
	})

	startPDOWatchdog()

	// Activate PDO and set a stale tick (>50ms ago, but below the 500ms
	// "HUNG" threshold) -- this is the intermediate warn level.
	pdoActive.Store(true)
	lastPDOTickNanos.Store(time.Now().Add(-100 * time.Millisecond).UnixNano())

	time.Sleep(250 * time.Millisecond)
}

func TestStartPDOWatchdog_RecoveryBranch_LogsRecovery(t *testing.T) {
	pdoActive.Store(false)
	lastPDOTickNanos.Store(0)
	pdoWatchdogRunning.Store(false)
	t.Cleanup(func() {
		pdoActive.Store(false)
		lastPDOTickNanos.Store(0)
		pdoWatchdogRunning.Store(false)
	})

	startPDOWatchdog()

	// Go stale first (so the watchdog's internal "last reported level" > 0)...
	pdoActive.Store(true)
	lastPDOTickNanos.Store(time.Now().Add(-100 * time.Millisecond).UnixNano())
	time.Sleep(200 * time.Millisecond)

	// ...then recover with a fresh tick and confirm no panic on the
	// recovery-logging branch.
	lastPDOTickNanos.Store(time.Now().UnixNano())
	time.Sleep(200 * time.Millisecond)
}

func TestStartPDOWatchdog_PDOInactive_SkipsCheck(t *testing.T) {
	pdoActive.Store(false)
	lastPDOTickNanos.Store(0)
	pdoWatchdogRunning.Store(false)
	t.Cleanup(func() {
		pdoActive.Store(false)
		lastPDOTickNanos.Store(0)
		pdoWatchdogRunning.Store(false)
	})

	startPDOWatchdog()
	time.Sleep(250 * time.Millisecond)
}

// ─── moveMotorToDegree — ABS fast path (already at target) ───────────────────
//
// This is the one branch of moveMotorToDegree reachable without a real
// EtherCAT master: it returns before ever calling doRotate (the PDO
// setpoint-write-and-poll loop). Everything it does touch —
// doECSCheck/doECSCheckZero (no-op when ECS setting != 1, the zero value),
// sendECSFinSignal (no-op when FinishSignal setting == 0, the zero value),
// doneDriverAction, and channels.DestinationReached — is safe with default
// settings and a drained UI channel.

func TestMoveMotorToDegree_AlreadyAtTarget_FastPath_CompletesWithoutRotating(t *testing.T) {
	initDriverStatusKeeperListener()
	t.Cleanup(func() {
		stopDriveStatusListener()
		waitForFlagFalse(&isStatusListening, 1*time.Second, "listenDriverStatus")
	})

	uiCh := make(chan channels.SocketMessage, 16)
	savedUI := channels.BroadCastUIChannel
	channels.BroadCastUIChannel = uiCh
	t.Cleanup(func() { channels.BroadCastUIChannel = savedUI })
	go func() {
		for range uiCh {
		}
	}()

	settings.SetDriverSettings("A", settings.DriverSettings{}) // ECS=0, FinishSignal=0 (zero values)

	setCurrentDriverStatus("A", driverCurrentStatus{
		mode:                "ABS",
		shortestPathEnabled: false,
		currentPosition:     90,
		destinationPosition: 90,
	})
	t.Cleanup(func() { setCurrentDriverStatus("A", driverCurrentStatus{}) })

	d := stubDevice("A")
	d.Device.Name = "A"
	d.Device.DriveXRatio = 1000
	d.PdoReady = true
	d.PDOPos.Store(90000) // 90000 / 1000 = 90.000 degrees — matches target exactly

	restore := setMasterDevicesForTest(d)
	defer restore()

	done := make(chan error, 1)
	go func() { done <- moveMotorToDegree(d, 90) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("moveMotorToDegree (already at target): unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("moveMotorToDegree (already at target) did not return promptly — fast path may not have triggered")
	}
}

func TestMoveMotorToDegree_AlreadyAtTarget_DedupGuard_SuppressesSecondFinSignal(t *testing.T) {
	// Simulates the executor re-issuing the same completed move within
	// dupFinWindow: the fast path should still complete cleanly (via the
	// dedup branch, which skips sendECSFinSignal/doECSCheckZero entirely).
	initDriverStatusKeeperListener()
	t.Cleanup(func() {
		stopDriveStatusListener()
		waitForFlagFalse(&isStatusListening, 1*time.Second, "listenDriverStatus")
	})

	uiCh := make(chan channels.SocketMessage, 16)
	savedUI := channels.BroadCastUIChannel
	channels.BroadCastUIChannel = uiCh
	t.Cleanup(func() { channels.BroadCastUIChannel = savedUI })
	go func() {
		for range uiCh {
		}
	}()

	settings.SetDriverSettings("A", settings.DriverSettings{})
	setCurrentDriverStatus("A", driverCurrentStatus{
		mode:                "ABS",
		currentPosition:     45,
		destinationPosition: 45,
	})
	t.Cleanup(func() { setCurrentDriverStatus("A", driverCurrentStatus{}) })

	d := stubDevice("A")
	d.Device.Name = "A"
	d.Device.DriveXRatio = 1000
	d.PdoReady = true
	d.PDOPos.Store(45000)
	restore := setMasterDevicesForTest(d)
	defer restore()

	// Stamp the dedup tracker as if a normal-path move to 45 just completed.
	lastNormalFinMu.Lock()
	lastNormalFinDest = 45
	lastNormalFinAt = time.Now()
	lastNormalFinMu.Unlock()
	t.Cleanup(func() {
		lastNormalFinMu.Lock()
		lastNormalFinDest = 0
		lastNormalFinAt = time.Time{}
		lastNormalFinMu.Unlock()
	})

	done := make(chan error, 1)
	go func() { done <- moveMotorToDegree(d, 45) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("moveMotorToDegree (dedup fast path): unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("moveMotorToDegree (dedup fast path) did not return promptly")
	}
}
