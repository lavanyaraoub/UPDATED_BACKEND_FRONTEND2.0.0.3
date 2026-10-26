package motordriver

import (
	channels "EtherCAT/channels"
	logger "EtherCAT/logger"
	settings "EtherCAT/settings"
	"fmt"
	"strconv"
	"sync/atomic"
	"time"

	cmap "github.com/orcaman/concurrent-map"
)

type driverCurrentStatus struct {
	currentPosition     float64
	alarm               string
	mode                string //ABS or REL (absolute or relative mode)
	shortestPathEnabled bool
	destinationPosition float64
	direction           int //rotation direction -1 anti clockwise 1 clock wise
	backlash            float64
	workOffset          float64
	potNotExceeded      bool
	potExceeded         bool
	notExceeded         bool
	isMotorRunning      bool //true when motor is rotating
	backlashInSetting   float64
	isSendingFinSignal  bool
	isDriverOnOff       bool
}

func (d *driverCurrentStatus) reset() {
	d.currentPosition = 0
	d.alarm = ""
	d.mode = "ABS"
	d.shortestPathEnabled = false
	d.destinationPosition = -1
	d.direction = 1
	d.backlash = 0
	d.workOffset = 0
	d.isMotorRunning = false

	// Wipe the safety limit memory during a reset. Without this, checkPotNotLimit's
	// "already flagged" branch (see poll_drive_position.go) keeps re-alarming
	// "POT/NOT Limit Exceeded" forever after a Reset, since nothing else ever
	// clears these flags.
	d.potNotExceeded = false
	d.potExceeded = false
	d.notExceeded = false
}

// EventType type of event which can be notified to driver status keeper
type EventType string

const (
	current_position     EventType = "current_position"
	mode                           = "mode"
	shortest_path_enable           = "shortest_path_enable"
	destination_position           = "destination_position"
)

var driveStatusUpdated chan bool

// waitForDriveStatusUpdate is set by notifyDriverStatusWithWait to signal
// doneDriveStatusUpdate that the caller is blocking on driveStatusUpdated.
// Must be atomic: written by listenDriverAction goroutine, read by
// listenDriverStatus goroutine (via doneDriveStatusUpdate).
var waitForDriveStatusUpdate atomic.Bool

func doneDriveStatusUpdate() {
	if waitForDriveStatusUpdate.Load() {
		driveStatusUpdated <- true
	}
}

// isStatusListening is true while the listenDriverStatus goroutine is running.
// Must be atomic: written by listenDriverStatus goroutine, read by
// notifyDriverStatus and notifyDriverStatusWithWait from other goroutines.
// A stale false-read causes notifyDriverStatusWithWait to deadlock waiting on
// driveStatusUpdated with nobody to send on it.
var isStatusListening atomic.Bool

// driverStatusMap will keep the status of each driver configured, for e.g. driverStatusMap["A"] contains all the
// status of drive A
// var driverStatusMap map[string]driverCurrentStatus
var driverStatusMap cmap.ConcurrentMap

func initDriverStatusKeeperListener() {
	logger.Debug("initialize driver status listener")
	driverStatusMap = cmap.New()

	// driverStatusMap = make(map[string]driverCurrentStatus)
	for _, dev := range masterDevices {
		settings := settings.GetDriverSettings(dev.Name)
		driveStatus := getCurrentDriverStatus(dev.Name)
		driveStatus.backlashInSetting = settings.BackLash
		driveStatus.direction = 1
		driveStatus.backlash = 0
		driveStatus.isMotorRunning = false
		setCurrentDriverStatus(dev.Name, driveStatus)
	}
	startDriverStatusListener()
}

func startDriverStatusListener() {
	logger.Debug("start driver status listener")
	channels.BroadCastDriveStatusChannel = make(chan channels.DriverStatus, 1000)
	driveStatusUpdated = make(chan bool)
	isStatusListening.Store(false)
	go listenDriverStatus()
	// Block briefly until the goroutine confirms it has actually started.
	// Without this, isStatusListening reads false (its zero value) whether
	// the listener hasn't started yet or has already exited — an immediate
	// caller can't tell the difference, which previously let fast-running
	// callers race the goroutine's own startup.
	deadline := time.Now().Add(1 * time.Second)
	for !isStatusListening.Load() && time.Now().Before(deadline) {
		time.Sleep(1 * time.Millisecond)
	}
}

func stopDriveStatusListener() {
	driverStatus := channels.DriverStatus{Event: "exit"}
	channels.BroadCastDriveStatusChannel <- driverStatus
}

// getCurrentDriverStatus returns the current status for the named drive.
// Returns a zero-value status if the drive has not been registered yet.
func getCurrentDriverStatus(driveName string) driverCurrentStatus {
	if stat, ok := driverStatusMap.Get(driveName); !ok {
		return driverCurrentStatus{currentPosition: 0, alarm: "", isMotorRunning: false, isDriverOnOff: false}
	} else {
		return stat.(driverCurrentStatus)
	}
}

func setCurrentDriverStatus(driveName string, currentStatus driverCurrentStatus) {
	driverStatusMap.Set(driveName, currentStatus)
}

func isMotionBlockedByPotNotLimit(driverStatus driverCurrentStatus, requestedDirection int) bool {
	if !driverStatus.potNotExceeded {
		return false
	}
	if requestedDirection == 0 {
		return true
	}
	if driverStatus.potExceeded && requestedDirection > 0 {
		return true
	}
	if driverStatus.notExceeded && requestedDirection < 0 {
		return true
	}
	if !driverStatus.potExceeded && !driverStatus.notExceeded {
		return true
	}
	return false
}

func currentDriverPosition(device *MasterDevice, currPos float64) {
	driverStatus := channels.DriverStatus{DriveName: device.Name, Data: fmt.Sprintf("%.3f", currPos), Event: "current_position"}
	// Non-blocking send: if the channel has buffer space or a receiver
	// ready, this behaves exactly like an unconditional send always did.
	// If nobody is listening and the channel is unbuffered or full, this
	// drops the update instead of blocking pollDrivePositionProcess
	// forever — a position update lost because nothing was listening is
	// harmless; a permanently stuck polling goroutine is not.
	select {
	case channels.BroadCastDriveStatusChannel <- driverStatus:
	default:
	}
}

func notifyDriverStatus(event EventType, data string, device *MasterDevice) {
	if !isStatusListening.Load() {
		return
	}
	// NOTE: Do NOT touch waitForDriveStatusUpdate here.
	// Resetting it to false would silently kill any concurrent
	// notifyDriverStatusWithWait caller that is mid-flight waiting
	// on <-driveStatusUpdated — the listener would never send on that
	// channel and the caller would stall indefinitely.
	driverStatus := channels.DriverStatus{DriveName: device.Name, Data: data, Event: string(event)}
	channels.BroadCastDriveStatusChannel <- driverStatus
}

// notifyDriverStatusWithWait callers can call this function if they need to ensure the driver status is updated
// before moving to the next step. For e.g. setting backlash, the caller should move forward only after successfully
// set the backlash other wise the system behave incorrectly
func notifyDriverStatusWithWait(event EventType, data string, device *MasterDevice) {
	if !isStatusListening.Load() {
		return
	}
	waitForDriveStatusUpdate.Store(true)
	driverStatus := channels.DriverStatus{DriveName: device.Name, Data: data, Event: string(event)}
	channels.BroadCastDriveStatusChannel <- driverStatus
	<-driveStatusUpdated
	waitForDriveStatusUpdate.Store(false)
}

func listenDriverStatus() {
	isStatusListening.Store(true)
	for {
		msg := <-channels.BroadCastDriveStatusChannel
		switch msg.Event {
		case "current_position":
			pos, _ := strconv.ParseFloat(msg.Data, 64)
			driverStatus := getCurrentDriverStatus(msg.DriveName)
			driverStatus.currentPosition = pos
			setCurrentDriverStatus(msg.DriveName, driverStatus)
		case "pot_not_exceeded":
			driverStatus := getCurrentDriverStatus(msg.DriveName)
			driverStatus.potNotExceeded = true
			driverStatus.notExceeded = false
			driverStatus.potExceeded = false
			if msg.Data == "POT" {
				driverStatus.potExceeded = true
			} else {
				driverStatus.notExceeded = true
			}
			setCurrentDriverStatus(msg.DriveName, driverStatus)
		case "mode":
			driverStatus := getCurrentDriverStatus(msg.DriveName)
			driverStatus.mode = msg.Data
			logger.Debug("changing driver mode to ", msg.Data)
			setCurrentDriverStatus(msg.DriveName, driverStatus)
			doneDriverAction()
			doneDriveStatusUpdate()
		case "shortest_path_enable":
			driverStatus := getCurrentDriverStatus(msg.DriveName)
			shortestPathEnbl, _ := strconv.ParseBool(msg.Data)
			driverStatus.shortestPathEnabled = shortestPathEnbl

			// IMPORTANT:
			// G68 (shortest path) must NOT override ABS/REL mode.
			// Mode should only be controlled by G90/G91.
			logger.Debug("Shortest path set to ", shortestPathEnbl, " (rotation mode unchanged: ", driverStatus.mode, ")")

			setCurrentDriverStatus(msg.DriveName, driverStatus)
			doneDriverAction()
			doneDriveStatusUpdate()
		case "destination_position":
			pos, _ := strconv.ParseFloat(msg.Data, 64)
			driverStatus := getCurrentDriverStatus(msg.DriveName)
			driverStatus.destinationPosition = pos
			setCurrentDriverStatus(msg.DriveName, driverStatus)
		case "reset":
			driverStatus := getCurrentDriverStatus(msg.DriveName)
			driverStatus.reset()
			setCurrentDriverStatus(msg.DriveName, driverStatus)
			logger.Trace("reset driver status")
		case "rotation_direction":
			driverStatusDir := getCurrentDriverStatus(msg.DriveName)
			dir, _ := strconv.ParseInt(msg.Data, 10, 32)
			newDirection := int(dir)

			// This event is now a post-success commit, not a pre-move request.
			// Keep one deterministic loaded side:
			//   + direction = zero-backlash/CW side
			//   - direction = configured-backlash/CCW side
			// This behaves like a rotary-table backlash side, not an accumulated
			// correction. If direction is still unknown, choose the side from the
			// successful move so the next command starts from a known state.
			if newDirection < 0 {
				driverStatusDir.backlash = driverStatusDir.backlashInSetting
			} else if newDirection > 0 {
				driverStatusDir.backlash = 0
			}
			driverStatusDir.direction = newDirection
			setCurrentDriverStatus(msg.DriveName, driverStatusDir)
			doneDriveStatusUpdate()
		case "set_backlash":
			backlash, _ := strconv.ParseFloat(msg.Data, 64)
			driverStatus := getCurrentDriverStatus(msg.DriveName)
			driverStatus.backlash = backlash
			setCurrentDriverStatus(msg.DriveName, driverStatus)
			doneDriveStatusUpdate()
		case "workoffset":
			workOffset, _ := strconv.ParseFloat(msg.Data, 64)
			driverStatus := getCurrentDriverStatus(msg.DriveName)
			driverStatus.workOffset = workOffset
			setCurrentDriverStatus(msg.DriveName, driverStatus)
			doneDriveStatusUpdate()
		case "motor_running":
			driverStatus := getCurrentDriverStatus(msg.DriveName)
			motorRunning, _ := strconv.ParseBool(msg.Data)
			driverStatus.isMotorRunning = motorRunning
			setCurrentDriverStatus(msg.DriveName, driverStatus)
		case "fin_signal":
			driverStatus := getCurrentDriverStatus(msg.DriveName)
			finSignal, _ := strconv.ParseBool(msg.Data)
			driverStatus.isSendingFinSignal = finSignal
			setCurrentDriverStatus(msg.DriveName, driverStatus)
			doneDriveStatusUpdate()
		case "driver_on_off":
			driverStatus := getCurrentDriverStatus(msg.DriveName)
			driverOn, _ := strconv.ParseBool(msg.Data)
			driverStatus.isDriverOnOff = driverOn
			// driverStatus.isMotorRunning = driverOn
			setCurrentDriverStatus(msg.DriveName, driverStatus)
		case "exit":
			logger.Debug("stopping driver status keeper")
			isStatusListening.Store(false)
			return
		}
	}

}
