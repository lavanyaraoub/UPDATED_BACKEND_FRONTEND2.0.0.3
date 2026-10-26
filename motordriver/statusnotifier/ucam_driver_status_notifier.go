package statusnotifier

import (
	channels "EtherCAT/channels"
	"EtherCAT/configparser"
	"EtherCAT/logger"
	"fmt"
	"strconv"
)

// UCAMNotifier UCAM specialized interface definition for status notifiers
type UCAMNotifier struct{}

// CurrentPosition notify the current position of the motor
func (ucam UCAMNotifier) CurrentPosition(driveName string, position float64) {
	message := channels.SocketMessage{Position: fmt.Sprintf("%.3f", position), DriveName: driveName, Event: "destination_position"}
	select {
	case channels.BroadCastUIChannel <- message:
	default:
	}
}

// DestinationPosition notify the destination position motor should rotate
func (ucam UCAMNotifier) DestinationPosition(driveName string, destPosition float32) {
	message := channels.SocketMessage{Data: fmt.Sprintf("%.3f", destPosition), DriveName: driveName, Event: "pos_data"}
	select {
	case channels.BroadCastUIChannel <- message:
	default:
	}
}

// Alarm raised
func (ucam UCAMNotifier) Alarm(alarm string) {
	message := channels.SocketMessage{Alarm: alarm, Event: "alarm_error"}
	select {
	case channels.BroadCastUIChannel <- message:
	default:
	}
}

// DriverError send driver error to the connected clients
func (ucam UCAMNotifier) DriverError(errorCode int) {
	// Keep the existing alarm path unchanged, but also publish the numeric
	// drive error using the event name consumed by the current UI Stats panel.
	// M700 reports 0xFF00|alarmID; other drives report the code directly.
	displayCode := errorCode
	if errorCode>>8 == 0xFF {
		displayCode = errorCode & 0xFF
	}
	message := channels.SocketMessage{Code: displayCode, Event: "drive_error_code"}
	select {
	case channels.BroadCastUIChannel <- message:
	default:
	}

	errID := errorCode - 65280
	if errID <= 0 {
		return
	}
	errString := configparser.GetErrorString(strconv.Itoa(errID))
	//errId 87 is when hardware emergency activated.
	if errID == 87 {
		logger.Trace("hardware emergency activated")
		channels.WriteCommandExecInput("stop_prog_exec", "")
	}
	// Call the package-level Alarm() (not ucam.Alarm() directly) so the
	// currentAlarmState cache in status_notifier.go is also updated.
	// If we called ucam.Alarm() here, new WebSocket clients connecting after
	// this error would get "No Alarms" from GetCurrentAlarm() instead of this
	// error string, because the cache update only happens in Alarm().
	Alarm(errString)
}

// DriverStatus notify whether the driver is connected or not.
func (ucam UCAMNotifier) DriverStatus(driveName, status string) {
	message := channels.SocketMessage{Data: status, Event: "driver_status"}
	select {
	case channels.BroadCastUIChannel <- message:
	default:
	}
}

func (ucam UCAMNotifier) NotifyIOStatus(ioStat IOStatus) {
	// j, err := json.Marshal(ioStat)
	// if err != nil {
	// 	logger.Error(err)
	// }
	ioStatus := channels.IOStatus{ECS: ioStat.ECS, FIN: ioStat.FIN, SOLOP: ioStat.SOLOP, CL: ioStat.CL, DCL: ioStat.DCL, ALMIN: ioStat.ALMIN, ALMOUT: ioStat.ALMOUT, HOME: ioStat.HOME, POT: ioStat.POT, NOT: ioStat.NOT}
	msg := channels.SocketMessage{Event: "io_status", DriveName: ioStat.DriveName, IOStat: ioStatus}
	// message := channels.SocketMessage{Data: string(j), Event: "io_status"}
	select {
	case channels.BroadCastUIChannel <- msg:
	default:
	}
}

func (ucam UCAMNotifier) SocketMessage(event string, msg string) {
	message := channels.SocketMessage{Data: msg, Event: event}
	select {
	case channels.BroadCastUIChannel <- message:
	default:
	}
}
