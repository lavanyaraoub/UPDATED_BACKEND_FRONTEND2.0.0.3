//go:build unit

package channels

import (
	"testing"
	"time"
)

// ─── helpers ──────────────────────────────────────────────────────────────────

func setupUIChannel(t *testing.T) {
	t.Helper()
	BroadCastUIChannel = make(chan SocketMessage, 4)
	t.Cleanup(func() { BroadCastUIChannel = nil })
}

func setupStatusChannel(t *testing.T) {
	t.Helper()
	BroadCastDriveStatusChannel = make(chan DriverStatus, 4)
	t.Cleanup(func() { BroadCastDriveStatusChannel = nil })
}

func receiveUI(t *testing.T) SocketMessage {
	t.Helper()
	select {
	case msg := <-BroadCastUIChannel:
		return msg
	case <-time.After(200 * time.Millisecond):
		t.Fatal("no message received on BroadCastUIChannel within 200ms")
		return SocketMessage{}
	}
}

// ─── SendAlarm ────────────────────────────────────────────────────────────────

func TestSendAlarm_SendsAlarmEvent(t *testing.T) {
	setupUIChannel(t)
	SendAlarm("drive fault 0x0011")
	msg := receiveUI(t)
	if msg.Event != "alarm_error" {
		t.Errorf("Event = %q, want alarm_error", msg.Event)
	}
	if msg.Alarm != "drive fault 0x0011" {
		t.Errorf("Alarm = %q, want 'drive fault 0x0011'", msg.Alarm)
	}
}

func TestSendAlarm_EmptyString(t *testing.T) {
	setupUIChannel(t)
	SendAlarm("")
	msg := receiveUI(t)
	if msg.Event != "alarm_error" {
		t.Errorf("Event = %q, want alarm_error", msg.Event)
	}
	if msg.Alarm != "" {
		t.Errorf("Alarm = %q, want empty", msg.Alarm)
	}
}

// ─── SendLineNumber ───────────────────────────────────────────────────────────

func TestSendLineNumber_SendsLineNumberEvent(t *testing.T) {
	setupUIChannel(t)
	SendLineNumber(42)
	msg := receiveUI(t)
	if msg.Event != "line_number" {
		t.Errorf("Event = %q, want line_number", msg.Event)
	}
	if msg.LineNumber != 42 {
		t.Errorf("LineNumber = %d, want 42", msg.LineNumber)
	}
}

func TestSendLineNumber_Zero(t *testing.T) {
	setupUIChannel(t)
	SendLineNumber(0)
	msg := receiveUI(t)
	if msg.LineNumber != 0 {
		t.Errorf("LineNumber = %d, want 0", msg.LineNumber)
	}
}

// ─── NotifyUIProgramCompleted ─────────────────────────────────────────────────

func TestNotifyUIProgramCompleted_SendsProgramCompleteEvent(t *testing.T) {
	setupUIChannel(t)
	NotifyUIProgramCompleted()
	msg := receiveUI(t)
	if msg.Event != "program_complete" {
		t.Errorf("Event = %q, want program_complete", msg.Event)
	}
}

// ─── StepModeComplete ─────────────────────────────────────────────────────────

func TestStepModeComplete_SendsEvent(t *testing.T) {
	setupUIChannel(t)
	StepModeComplete()
	msg := receiveUI(t)
	if msg.Event != "step_mode_completed" {
		t.Errorf("Event = %q, want step_mode_completed", msg.Event)
	}
}

// ─── DestinationReached ───────────────────────────────────────────────────────

func TestDestinationReached_SendsEvent(t *testing.T) {
	setupUIChannel(t)
	DestinationReached()
	msg := receiveUI(t)
	if msg.Event != "destination_reached" {
		t.Errorf("Event = %q, want destination_reached", msg.Event)
	}
}

// ─── SocketMessage struct fields ──────────────────────────────────────────────

func TestSocketMessage_AllFieldsSettable(t *testing.T) {
	msg := SocketMessage{
		Data:       "data",
		Reference:  "ref",
		Position:   "90.0",
		Event:      "test_event",
		Direction:  1,
		Action:     2,
		Position2:  180.5,
		FileName:   "motion.nc",
		Alarm:      "E01",
		LineNumber: 5,
		DriveName:  "A",
		UserLine:   "3",
		IOStat: IOStatus{
			ECS: true, FIN: true, SOLOP: false,
			CL: true, DCL: false, ALMIN: true,
			ALMOUT: false, HOME: true, POT: false, NOT: true,
		},
	}
	if msg.Event != "test_event" {
		t.Errorf("Event = %q, want test_event", msg.Event)
	}
	if msg.IOStat.ECS != true {
		t.Error("IOStat.ECS should be true")
	}
	if msg.IOStat.NOT != true {
		t.Error("IOStat.NOT should be true")
	}
}

// ─── BroadCastDriveStatusChannel ─────────────────────────────────────────────

func TestBroadCastDriveStatusChannel_SendAndReceive(t *testing.T) {
	setupStatusChannel(t)
	status := DriverStatus{
		DriveName:   "A",
		Data:        "0.000",
		Event:       "current_position",
		Description: "position updated",
	}
	BroadCastDriveStatusChannel <- status
	select {
	case got := <-BroadCastDriveStatusChannel:
		if got.DriveName != "A" {
			t.Errorf("DriveName = %q, want A", got.DriveName)
		}
		if got.Event != "current_position" {
			t.Errorf("Event = %q, want current_position", got.Event)
		}
		if got.Data != "0.000" {
			t.Errorf("Data = %q, want 0.000", got.Data)
		}
	case <-time.After(200 * time.Millisecond):
		t.Error("no status received on BroadCastDriveStatusChannel")
	}
}

func TestDriverStatus_AllFieldsSettable(t *testing.T) {
	s := DriverStatus{
		DriveName:   "B",
		Data:        "alarm_data",
		Event:       "alarm",
		Description: "drive fault",
	}
	if s.DriveName != "B" || s.Event != "alarm" {
		t.Errorf("DriverStatus fields not set correctly: %+v", s)
	}
}
