//go:build unit

package channels

import (
	"testing"
	"time"
)

// ─── DriveActionChannelReady / NotifyMotorDriver ──────────────────────────────

func TestNotifyMotorDriver_WhenNotReady_IsNoOp(t *testing.T) {
	isReady = false
	DriverActionChannel = make(chan DriverAction, 1)
	t.Cleanup(func() { DriverActionChannel = nil; isReady = false })

	NotifyMotorDriver(RESET, "", "", 0)

	if len(DriverActionChannel) != 0 {
		t.Error("NotifyMotorDriver should not send when isReady=false")
	}
}

func TestDriveActionChannelReady_SetsFlag(t *testing.T) {
	isReady = false
	DriveActionChannelReady()
	if !isReady {
		t.Error("expected isReady=true after DriveActionChannelReady()")
	}
	isReady = false
}

func TestNotifyMotorDriver_WhenReady_SendsAction(t *testing.T) {
	DriverActionChannel = make(chan DriverAction, 1)
	t.Cleanup(func() { DriverActionChannel = nil; isReady = false })
	DriveActionChannelReady()

	NotifyMotorDriver(MOVE_TO_POSITION, "90.0", "A", 1)

	select {
	case action := <-DriverActionChannel:
		if action.Action != MOVE_TO_POSITION {
			t.Errorf("Action = %q, want MOVE_TO_POSITION", action.Action)
		}
		if action.Value != "90.0" {
			t.Errorf("Value = %q, want 90.0", action.Value)
		}
		if action.DriveName != "A" {
			t.Errorf("DriveName = %q, want A", action.DriveName)
		}
		if action.Direction != 1 {
			t.Errorf("Direction = %d, want 1", action.Direction)
		}
	case <-time.After(200 * time.Millisecond):
		t.Error("expected action on DriverActionChannel, got nothing")
	}
}

func TestNotifyMotorDriver_AllActionTypes(t *testing.T) {
	actions := []ActionType{
		RESET, JOG, EXIT_DRIVE_LISTENER, STOP_JOG, MANUAL_JOG,
		ZERO_REF, STEP_MODE, STEP_MODE_ENABLE, SET_RPM,
		MOVE_TO_POSITION, POSITION_MODE, START_EXECUTION,
		SHORTEST_PATH_ENABLED, EMERGENCY, PROGRAM_EXEC_COMPLETED,
		FAST_POWER_OFF, RESET_MULTI_TURN, SET_WORK_OFFSET,
		SETTINGS_CHANGED, STOP_PROGRAM_EXECUTION,
	}

	for _, a := range actions {
		DriverActionChannel = make(chan DriverAction, 1)
		isReady = true
		NotifyMotorDriver(a, "val", "A", 0)
		select {
		case got := <-DriverActionChannel:
			if got.Action != a {
				t.Errorf("Action = %q, want %q", got.Action, a)
			}
		case <-time.After(100 * time.Millisecond):
			t.Errorf("no action sent for ActionType %q", a)
		}
	}
	DriverActionChannel = nil
	isReady = false
}

func TestDriverAction_ZeroValueFields(t *testing.T) {
	DriverActionChannel = make(chan DriverAction, 1)
	t.Cleanup(func() { DriverActionChannel = nil; isReady = false })
	DriveActionChannelReady()

	NotifyMotorDriver(JOG, "", "", 0)

	select {
	case action := <-DriverActionChannel:
		if action.DriveName != "" {
			t.Errorf("DriveName = %q, want empty", action.DriveName)
		}
		if action.Value != "" {
			t.Errorf("Value = %q, want empty", action.Value)
		}
		if action.Direction != 0 {
			t.Errorf("Direction = %d, want 0", action.Direction)
		}
	case <-time.After(200 * time.Millisecond):
		t.Error("no action received")
	}
}
