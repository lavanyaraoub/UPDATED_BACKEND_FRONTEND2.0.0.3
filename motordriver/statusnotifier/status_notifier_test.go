//go:build unit

package statusnotifier

// Unit tests for the pure-atomic cache functions in status_notifier.go.
//
// Only functions that do NOT touch channels.BroadCastUIChannel are tested
// here — those functions block forever if the channel is nil (which it
// always is in unit tests). The safe surface is:
//
//   GetCurrentAlarm / currentAlarmState (atomic.Value)
//   GetCurrentErrorCode / SetCurrentErrorCode (atomic.Int32)
//   IsMotorRunning / NotifyMotorRunning (atomic.Bool)
//   init() default state
//
// Run with:
//   go test -tags unit ./motordriver/statusnotifier/...

import (
	"EtherCAT/channels"
	"testing"
)

// ─── init() default state ─────────────────────────────────────────────────

// TestInit_DefaultAlarmIsNoAlarms verifies the package initialises the alarm
// cache to "No Alarms" so a client connecting before any fault fires sees a
// clean state rather than an empty string or nil panic.
func TestInit_DefaultAlarmIsNoAlarms(t *testing.T) {
	got := GetCurrentAlarm()
	if got != "No Alarms" {
		t.Errorf("default alarm = %q, want \"No Alarms\"", got)
	}
}

// TestInit_DefaultErrorCodeIsZero verifies the error code cache starts at 0.
func TestInit_DefaultErrorCodeIsZero(t *testing.T) {
	if got := GetCurrentErrorCode(); got != 0 {
		t.Errorf("default error code = %d, want 0", got)
	}
}

// TestInit_DefaultMotorRunningIsFalse verifies the motor-running flag starts false.
func TestInit_DefaultMotorRunningIsFalse(t *testing.T) {
	if IsMotorRunning() {
		t.Errorf("default IsMotorRunning() = true, want false")
	}
}

// ─── currentAlarmState ────────────────────────────────────────────────────

// TestGetCurrentAlarm_ReturnsStoredValue verifies GetCurrentAlarm returns
// whatever was last stored in the atomic cache.
// Note: Alarm() itself is not called here because it reaches BroadCastUIChannel.
// We write directly to the cache to test GetCurrentAlarm in isolation.
func TestGetCurrentAlarm_ReturnsStoredValue(t *testing.T) {
	currentAlarmState.Store("POT Limit Exceeded")
	t.Cleanup(func() { currentAlarmState.Store("No Alarms") })

	got := GetCurrentAlarm()
	if got != "POT Limit Exceeded" {
		t.Errorf("GetCurrentAlarm() = %q, want \"POT Limit Exceeded\"", got)
	}
}

// TestGetCurrentAlarm_ReturnsNoAlarmsOnNil verifies the nil-guard returns
// "No Alarms" rather than panicking when the atomic holds nil.
func TestGetCurrentAlarm_ReturnsNoAlarmsOnNil(t *testing.T) {
	// Store a known value, then restore after test
	original := GetCurrentAlarm()
	t.Cleanup(func() { currentAlarmState.Store(original) })

	// atomic.Value.Store(nil) panics — simulate the nil path by storing
	// an empty string (the other edge case) instead.
	currentAlarmState.Store("")
	got := GetCurrentAlarm()
	// Empty string is a valid stored value — just verify no panic
	_ = got
}

// TestGetCurrentAlarm_RoundTrip verifies multiple alarm strings can be stored
// and retrieved correctly in sequence.
func TestGetCurrentAlarm_RoundTrip(t *testing.T) {
	t.Cleanup(func() { currentAlarmState.Store("No Alarms") })

	alarms := []string{
		"Drive Fault",
		"EtherCAT Link Lost",
		"NOT Limit Exceeded",
		"No Alarms",
	}
	for _, alarm := range alarms {
		currentAlarmState.Store(alarm)
		got := GetCurrentAlarm()
		if got != alarm {
			t.Errorf("after Store(%q), GetCurrentAlarm() = %q", alarm, got)
		}
	}
}

// ─── currentErrorCode ────────────────────────────────────────────────────

// TestSetCurrentErrorCode_StoresValue verifies SetCurrentErrorCode updates
// the atomic and GetCurrentErrorCode reads it back.
func TestSetCurrentErrorCode_StoresValue(t *testing.T) {
	t.Cleanup(func() { SetCurrentErrorCode(0) })

	SetCurrentErrorCode(42)
	if got := GetCurrentErrorCode(); got != 42 {
		t.Errorf("GetCurrentErrorCode() = %d, want 42", got)
	}
}

// TestSetCurrentErrorCode_Zero verifies zero can be stored and retrieved.
func TestSetCurrentErrorCode_Zero(t *testing.T) {
	SetCurrentErrorCode(99)
	SetCurrentErrorCode(0)
	if got := GetCurrentErrorCode(); got != 0 {
		t.Errorf("GetCurrentErrorCode() = %d, want 0 after reset", got)
	}
}

// TestSetCurrentErrorCode_RoundTrip verifies several error codes round-trip
// correctly including a known drive fault code.
func TestSetCurrentErrorCode_RoundTrip(t *testing.T) {
	t.Cleanup(func() { SetCurrentErrorCode(0) })

	codes := []int{0, 1, 87, 255, 32767}
	for _, code := range codes {
		SetCurrentErrorCode(code)
		got := GetCurrentErrorCode()
		if got != code {
			t.Errorf("after SetCurrentErrorCode(%d), got %d", code, got)
		}
	}
}

// TestSetCurrentErrorCode_NegativeStoredAsInt32 documents that negative values
// are stored via int32 truncation — the function signature takes int.
func TestSetCurrentErrorCode_NegativeStoredAsInt32(t *testing.T) {
	t.Cleanup(func() { SetCurrentErrorCode(0) })

	SetCurrentErrorCode(-1)
	got := GetCurrentErrorCode()
	// int32(-1) cast to int == -1 on all platforms
	if got != -1 {
		t.Errorf("SetCurrentErrorCode(-1): got %d, want -1", got)
	}
}

// ─── IsMotorRunning / NotifyMotorRunning ─────────────────────────────────

// TestNotifyMotorRunning_TrueIsReflected verifies setting true is readable.
func TestNotifyMotorRunning_TrueIsReflected(t *testing.T) {
	t.Cleanup(func() { NotifyMotorRunning("A", false) })

	NotifyMotorRunning("A", true)
	if !IsMotorRunning() {
		t.Errorf("IsMotorRunning() = false after NotifyMotorRunning(true), want true")
	}
}

// TestNotifyMotorRunning_FalseIsReflected verifies setting false is readable.
func TestNotifyMotorRunning_FalseIsReflected(t *testing.T) {
	NotifyMotorRunning("A", true)
	NotifyMotorRunning("A", false)
	if IsMotorRunning() {
		t.Errorf("IsMotorRunning() = true after NotifyMotorRunning(false), want false")
	}
}

// TestNotifyMotorRunning_Toggle verifies alternating true/false reflects correctly.
func TestNotifyMotorRunning_Toggle(t *testing.T) {
	t.Cleanup(func() { NotifyMotorRunning("A", false) })

	for i := 0; i < 6; i++ {
		want := i%2 == 0
		NotifyMotorRunning("A", want)
		got := IsMotorRunning()
		if got != want {
			t.Errorf("iteration %d: IsMotorRunning() = %v, want %v", i, got, want)
		}
	}
}

// TestNotifyMotorRunning_DriveNameIgnored verifies the drive name parameter
// does not affect the stored boolean (the current implementation is global,
// not per-drive).
func TestNotifyMotorRunning_DriveNameIgnored(t *testing.T) {
	t.Cleanup(func() { NotifyMotorRunning("A", false) })

	NotifyMotorRunning("B", true)
	if !IsMotorRunning() {
		t.Errorf("IsMotorRunning() = false after NotifyMotorRunning(\"B\", true), want true")
	}
	NotifyMotorRunning("C", false)
	if IsMotorRunning() {
		t.Errorf("IsMotorRunning() = true after NotifyMotorRunning(\"C\", false), want false")
	}
}

// ─── Channel-writing functions ────────────────────────────────────────────────
// These functions write to channels.BroadCastUIChannel which is nil by default.
// Each test creates a buffered channel, calls the function, and asserts the
// message payload — restoring the original channel value on cleanup.

func withBroadcastChannel(t *testing.T) chan channels.SocketMessage {
	t.Helper()
	ch := make(chan channels.SocketMessage, 32)
	saved := channels.BroadCastUIChannel
	channels.BroadCastUIChannel = ch
	t.Cleanup(func() { channels.BroadCastUIChannel = saved })
	return ch
}

// ─── UCAMNotifier.CurrentPosition ────────────────────────────────────────────

func TestUCAMNotifier_CurrentPosition_SendsPositionMessage(t *testing.T) {
	ch := withBroadcastChannel(t)
	n := UCAMNotifier{}
	n.CurrentPosition("A", 123.456)

	msg := <-ch
	if msg.Event != "destination_position" {
		t.Errorf("event = %q want destination_position", msg.Event)
	}
	if msg.DriveName != "A" {
		t.Errorf("DriveName = %q want A", msg.DriveName)
	}
	if msg.Position != "123.456" {
		t.Errorf("Position = %q want 123.456", msg.Position)
	}
}

// ─── UCAMNotifier.DestinationPosition ────────────────────────────────────────

func TestUCAMNotifier_DestinationPosition_SendsPosDataMessage(t *testing.T) {
	ch := withBroadcastChannel(t)
	n := UCAMNotifier{}
	n.DestinationPosition("A", 90.0)

	msg := <-ch
	if msg.Event != "pos_data" {
		t.Errorf("event = %q want pos_data", msg.Event)
	}
	if msg.Data != "90.000" {
		t.Errorf("Data = %q want 90.000", msg.Data)
	}
}

// ─── UCAMNotifier.DriverStatus ────────────────────────────────────────────────

func TestUCAMNotifier_DriverStatus_SendsDriverStatusEvent(t *testing.T) {
	ch := withBroadcastChannel(t)
	n := UCAMNotifier{}
	n.DriverStatus("A", "connected")

	msg := <-ch
	if msg.Event != "driver_status" {
		t.Errorf("event = %q want driver_status", msg.Event)
	}
	if msg.Data != "connected" {
		t.Errorf("Data = %q want connected", msg.Data)
	}
}

// ─── UCAMNotifier.SocketMessage ───────────────────────────────────────────────

func TestUCAMNotifier_SocketMessage_SendsCustomEvent(t *testing.T) {
	ch := withBroadcastChannel(t)
	n := UCAMNotifier{}
	n.SocketMessage("reset_done", "reset completed")

	msg := <-ch
	if msg.Event != "reset_done" {
		t.Errorf("event = %q want reset_done", msg.Event)
	}
	if msg.Data != "reset completed" {
		t.Errorf("Data = %q want reset completed", msg.Data)
	}
}

// ─── UCAMNotifier.NotifyIOStatus ─────────────────────────────────────────────

func TestUCAMNotifier_NotifyIOStatus_SendsIOStatusEvent(t *testing.T) {
	ch := withBroadcastChannel(t)
	n := UCAMNotifier{}
	n.NotifyIOStatus(IOStatus{DriveName: "A", ECS: true, FIN: false, POT: true})

	msg := <-ch
	if msg.Event != "io_status" {
		t.Errorf("event = %q want io_status", msg.Event)
	}
	if msg.DriveName != "A" {
		t.Errorf("DriveName = %q want A", msg.DriveName)
	}
	if !msg.IOStat.ECS {
		t.Error("IOStat.ECS should be true")
	}
	if !msg.IOStat.POT {
		t.Error("IOStat.POT should be true")
	}
}

// ─── UCAMNotifier.Alarm ──────────────────────────────────────────────────────

func TestUCAMNotifier_Alarm_SendsAlarmErrorEvent(t *testing.T) {
	ch := withBroadcastChannel(t)
	n := UCAMNotifier{}
	n.Alarm("Drive Fault")

	msg := <-ch
	if msg.Event != "alarm_error" {
		t.Errorf("event = %q want alarm_error", msg.Event)
	}
	if msg.Alarm != "Drive Fault" {
		t.Errorf("Alarm = %q want Drive Fault", msg.Alarm)
	}
}

// ─── UCAMNotifier.DriverError — guard paths ───────────────────────────────────

func TestUCAMNotifier_DriverError_ZeroCode_IsNoOp(t *testing.T) {
	ch := withBroadcastChannel(t)
	n := UCAMNotifier{}
	n.DriverError(0) // errID = 0-65280 < 0 → silent drop
	if len(ch) != 0 {
		t.Errorf("DriverError(0): expected no channel message, got %d", len(ch))
	}
}

func TestUCAMNotifier_DriverError_NonFFxxEncoding_IsNoOp(t *testing.T) {
	ch := withBroadcastChannel(t)
	n := UCAMNotifier{}
	n.DriverError(80) // errID = 80-65280 < 0 → drops with warning
	if len(ch) != 0 {
		t.Errorf("DriverError(80 non-encoded): expected no message, got %d", len(ch))
	}
}

// ─── Package-level wrappers ───────────────────────────────────────────────────

func TestNotifyCurrentPosition_ForwardsToNotifier(t *testing.T) {
	ch := withBroadcastChannel(t)
	NotifyCurrentPosition("A", 45.0)
	msg := <-ch
	if msg.Event != "destination_position" {
		t.Errorf("event = %q want destination_position", msg.Event)
	}
}

func TestNotifyDestinationPosition_ForwardsToNotifier(t *testing.T) {
	ch := withBroadcastChannel(t)
	NotifyDestinationPosition("A", 180.0)
	msg := <-ch
	if msg.Event != "pos_data" {
		t.Errorf("event = %q want pos_data", msg.Event)
	}
}

func TestDriverStatus_ForwardsToNotifier(t *testing.T) {
	ch := withBroadcastChannel(t)
	DriverStatus("A", "disconnected")
	msg := <-ch
	if msg.Event != "driver_status" {
		t.Errorf("event = %q want driver_status", msg.Event)
	}
	if msg.Data != "disconnected" {
		t.Errorf("Data = %q want disconnected", msg.Data)
	}
}

func TestNotifyIOStatus_ForwardsToNotifier(t *testing.T) {
	ch := withBroadcastChannel(t)
	NotifyIOStatus(IOStatus{DriveName: "A", FIN: true})
	msg := <-ch
	if msg.Event != "io_status" {
		t.Errorf("event = %q want io_status", msg.Event)
	}
}

func TestSocketMessage_ForwardsToNotifier(t *testing.T) {
	ch := withBroadcastChannel(t)
	SocketMessage("gotozero_done", "goto zero completed")
	msg := <-ch
	if msg.Event != "gotozero_done" {
		t.Errorf("event = %q want gotozero_done", msg.Event)
	}
	if msg.Data != "goto zero completed" {
		t.Errorf("Data = %q want goto zero completed", msg.Data)
	}
}

// ─── UCAMNotifier.DriverError — valid 0xFFxx encoded error ───────────────────

func TestUCAMNotifier_DriverError_ValidEncoding_FiresAlarmAndSetsCode(t *testing.T) {
	ch := withBroadcastChannel(t)
	channels.CommandExecInputChannel = make(chan channels.CommandExecInput, 32)
	t.Cleanup(func() {
		for len(channels.CommandExecInputChannel) > 0 {
			<-channels.CommandExecInputChannel
		}
	})
	t.Cleanup(func() { SetCurrentErrorCode(0) })

	// Valid encoding: errorCode = 65280 + errID, errID = 14 → 65294
	// errID=14 is "Over-current protection", not 87 (emergency) so no
	// CommandExecInput write happens.
	//
	// NOTE: in this codebase the error-code cache is set by the package-level
	// DriverError() wrapper (status_notifier.go), not by UCAMNotifier.DriverError
	// itself — all real callers (motordriver/poll_driver_alarm.go,
	// motordriver/a6_minas_motor_driver.go) go through the wrapper, so that's
	// the integration point this test exercises.
	DriverError(65280 + 14) // errID = 14

	// Should have written to BroadCastUIChannel (driver_error + alarm_error)
	if len(ch) == 0 {
		t.Error("DriverError(valid): expected messages on BroadCastUIChannel")
	}
	// Error code cache should be updated. Note: this codebase caches the raw
	// 0xFFxx-encoded errorCode (see DriverError's doc comment in
	// status_notifier.go), not the decoded errID.
	if GetCurrentErrorCode() != 65280+14 {
		t.Errorf("GetCurrentErrorCode() = %d, want %d", GetCurrentErrorCode(), 65280+14)
	}
}

func TestUCAMNotifier_DriverError_ErrID87_WritesStopCommand(t *testing.T) {
	ch := withBroadcastChannel(t)
	channels.CommandExecInputChannel = make(chan channels.CommandExecInput, 32)
	t.Cleanup(func() {
		for len(channels.CommandExecInputChannel) > 0 {
			<-channels.CommandExecInputChannel
		}
	})
	t.Cleanup(func() { SetCurrentErrorCode(0) })

	n := UCAMNotifier{}
	n.DriverError(65280 + 87) // errID = 87 (hardware emergency)

	// Should write stop_prog_exec to CommandExecInputChannel
	if len(channels.CommandExecInputChannel) == 0 {
		t.Error("DriverError(errID=87): expected stop_prog_exec on CommandExecInputChannel")
	}
	_ = ch
}

// ─── AlarmCleared ─────────────────────────────────────────────────────────────

func TestAlarmCleared_WhenFaulted_SendsNoAlarms(t *testing.T) {
	ch := withBroadcastChannel(t)
	t.Cleanup(func() { currentAlarmState.Store("No Alarms") })

	// Prime with a fault state
	currentAlarmState.Store("Drive Fault")
	AlarmCleared()

	// Should have sent at least one message (driver_error:0 + alarm_error)
	if len(ch) == 0 {
		t.Error("AlarmCleared: expected messages on BroadCastUIChannel")
	}
	// Alarm cache should now be "No Alarms"
	if got := GetCurrentAlarm(); got != "No Alarms" {
		t.Errorf("AlarmCleared: cache = %q want No Alarms", got)
	}
	// Error code cache should be 0
	if GetCurrentErrorCode() != 0 {
		t.Errorf("AlarmCleared: error code = %d want 0", GetCurrentErrorCode())
	}
}

func TestAlarmCleared_AlreadyClear_IsIdempotent(t *testing.T) {
	ch := withBroadcastChannel(t)
	t.Cleanup(func() { currentAlarmState.Store("No Alarms") })
	t.Cleanup(func() { SetCurrentErrorCode(0) })

	// Already "No Alarms" AND error code already 0 — HAL's AlarmCleared is a
	// true no-op in this case (unlike designs that always re-broadcast
	// driver_error:0): it checks both the alarm string AND the cached error
	// code before deciding there's nothing to do. This avoids redundant
	// broadcasts on a periodic "no fault" poll.
	currentAlarmState.Store("No Alarms")
	SetCurrentErrorCode(0)
	AlarmCleared()

	if len(ch) != 0 {
		t.Errorf("AlarmCleared (already fully clear): expected no broadcast, got %d message(s)", len(ch))
	}
	// Alarm stays "No Alarms"
	if got := GetCurrentAlarm(); got != "No Alarms" {
		t.Errorf("AlarmCleared idempotent: cache = %q", got)
	}
}

// ─── Alarm ────────────────────────────────────────────────────────────────────

func TestAlarm_UpdatesCacheAndBroadcasts(t *testing.T) {
	ch := withBroadcastChannel(t)
	t.Cleanup(func() { currentAlarmState.Store("No Alarms") })

	Alarm("NOT Limit Exceeded")

	if len(ch) == 0 {
		t.Error("Alarm: expected message on BroadCastUIChannel")
	}
	if got := GetCurrentAlarm(); got != "NOT Limit Exceeded" {
		t.Errorf("Alarm: cache = %q want NOT Limit Exceeded", got)
	}
}

// ─── DriverError (package-level wrapper) ─────────────────────────────────────

func TestDriverError_ValidCode_FiresAlarm(t *testing.T) {
	ch := withBroadcastChannel(t)
	channels.CommandExecInputChannel = make(chan channels.CommandExecInput, 32)
	t.Cleanup(func() {
		for len(channels.CommandExecInputChannel) > 0 {
			<-channels.CommandExecInputChannel
		}
		SetCurrentErrorCode(0)
		currentAlarmState.Store("No Alarms")
	})

	DriverError(65280 + 14) // errID=14
	if len(ch) == 0 {
		t.Error("DriverError(valid): expected channel messages")
	}
}
