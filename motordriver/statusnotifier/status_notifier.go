package statusnotifier

import "sync/atomic"

// currentAlarmState caches the last alarm string sent to the UI.
//
// ROOT CAUSE OF "error not visible on GUI":
//
// statusnotifier.Alarm() pushes to channels.BroadCastUIChannel, which the
// WebSocket broadcaster consumes and sends to all currently connected clients.
// This is a fire-and-forget push — there is no history. If no browser client
// is connected when the alarm fires (which is always the case at startup —
// the user hasn't opened the web page yet), the message is broadcast to zero
// recipients and lost forever.
//
// When the user later opens the web page and the WebSocket connects, the
// server sends no "catch-up" state — the client only receives future events.
// So the client initialises showing whatever was last sent before it connected,
// typically "No Alarms" from command_executor.go Initialize().
//
// Fix: cache the last alarm string here. The WebSocket connection handler
// (in the web server package) must call GetCurrentAlarm() when a new client
// connects and send them the cached state as an "alarm_error" event.
// This ensures every new connection immediately reflects the current alarm
// state regardless of when the fault was first detected.
var currentMotorRunningState atomic.Bool
var currentAlarmState atomic.Value

// currentErrorCode caches the last raw error code integer sent via DriverError().
// Mirrors currentAlarmState for the numeric code — used by the WebSocket
// on-connect handler to push the current error code to newly connected clients,
// so the UI error code panel is correct after a page refresh.
var currentErrorCode atomic.Int32

func init() {
	currentAlarmState.Store("No Alarms")
	currentErrorCode.Store(0)
}

// GetCurrentAlarm returns the last alarm string that was sent to the UI.
// Call this in the WebSocket on-connect handler to send new clients the
// current alarm state. Example:
//
//	func onClientConnect(conn *websocket.Conn) {
//	    alarm := statusnotifier.GetCurrentAlarm()
//	    conn.WriteJSON(SocketMessage{Alarm: alarm, Event: "alarm_error"})
//	}
func GetCurrentAlarm() string {
	if v := currentAlarmState.Load(); v != nil {
		return v.(string)
	}
	return "No Alarms"
}

// getDriverStatusNotifier returns the specialized status notifier
// for now returns ucam's status notifier defined in ucamDriverStatusNotifier.go
func getDriverStatusNotifier() IDriverStatusNotifier {
	return &UCAMNotifier{}
}

// NotifyCurrentPosition notify the current position to client
func NotifyCurrentPosition(driveName string, currentPos float64) {
	notifier := getDriverStatusNotifier()
	notifier.CurrentPosition(driveName, currentPos)
}

// NotifyDestinationPosition notify destination position to client
func NotifyDestinationPosition(driveName string, destPos float32) {
	notifier := getDriverStatusNotifier()
	notifier.DestinationPosition(driveName, destPos)
}

// Alarm raise alarm to the ui
func Alarm(alarm string) {
	// Cache BEFORE broadcasting. If the broadcast goroutine runs immediately
	// and the WS handler queries GetCurrentAlarm() concurrently, it will
	// see the new value rather than the previous one.
	currentAlarmState.Store(alarm)
	notifier := getDriverStatusNotifier()
	notifier.Alarm(alarm)
}

// AlarmCleared emits a single "No Alarms" event and clears the cached numeric error.
func AlarmCleared() {
	const cleared = "No Alarms"
	if v := currentAlarmState.Load(); v != nil {
		if s, ok := v.(string); ok && s == cleared && currentErrorCode.Load() == 0 {
			return
		}
	}
	currentErrorCode.Store(0)
	Alarm(cleared)
}

// GetCurrentErrorCode returns the last raw error code integer sent via DriverError().
// Call this in the WebSocket on-connect handler alongside GetCurrentAlarm() to
// restore the full fault state for newly connected or reconnected clients.
func GetCurrentErrorCode() int {
	return int(currentErrorCode.Load())
}

// DriverError send driver error to the connected clients
func DriverError(errorCode int) {
	currentErrorCode.Store(int32(errorCode))
	notifier := getDriverStatusNotifier()
	notifier.DriverError(errorCode)
}

func DriverStatus(driveName, status string) {
	notifier := getDriverStatusNotifier()
	notifier.DriverStatus(driveName, status)
}

func NotifyMotorRunning(driveName string, isRunning bool) {
	currentMotorRunningState.Store(isRunning)
}

func IsMotorRunning() bool {
	return currentMotorRunningState.Load()
}

func NotifyIOStatus(ioStat IOStatus) {
	notifier := getDriverStatusNotifier()
	notifier.NotifyIOStatus(ioStat)
}

func SocketMessage(event string, msg string) {
	notifier := getDriverStatusNotifier()
	notifier.SocketMessage(event, msg)
}
