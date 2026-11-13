package motordriver

//Listen for any action request on Motor to perform from the client. For e.g. reset, zero reference, etc

import (
	channels "EtherCAT/channels"
	logger "EtherCAT/logger"
	"EtherCAT/motordriver/statusnotifier"
	"EtherCAT/settings"
	"fmt"
	"math"
	"strconv"
	"sync/atomic"
	"time"
)

// isActionListening is true while the listenDriverAction goroutine is running.
var isActionListening atomic.Bool

// rotation_direction is a package-level variable used to store the last jog direction.
var rotation_direction int

func initDriverActionListener() {
	logger.Debug("starting driver action listener")
	channels.DriverActionChannel = make(chan channels.DriverAction, 100)
	channels.DriveActionChannelReady()
	isActionListening.Store(false)
	go listenDriverAction()
	deadline := time.Now().Add(1 * time.Second)
	for !isActionListening.Load() && time.Now().Before(deadline) {
		time.Sleep(1 * time.Millisecond)
	}
}

func stopDriverActionListener() {
	channels.NotifyMotorDriver("EXIT_DRIVE_LISTENER", "", "", 0)
}

func getDeviceForAction(msg channels.DriverAction) *MasterDevice {
	devices := getMasterDevices()
	if len(devices) == 0 {
		return nil
	}
	if msg.DriveName != "" {
		for _, d := range devices {
			if d.Name == msg.DriveName {
				return d
			}
		}
		logger.Warn("[ACTION] device not found for DriveName:", msg.DriveName,
			"— falling back to devices[0]")
	}
	return devices[0]
}

func listenDriverAction() {
	isActionListening.Store(true)
	for {
		msg := <-channels.DriverActionChannel

		switch msg.Action {
		case channels.RESET:
			performSysReset(true)

		case channels.MANUAL_JOG:
			device := getDeviceForAction(msg)
			if device == nil {
				logger.Error("[MANUAL_JOG] no device available")
				continue
			}
			rotation_direction = msg.Direction
			var err error
			if msg.Value == "" {
				// Legacy clients keep using Machine Parameters > Jog Feed.
				err = ManualJog(device, msg.Direction)
			} else {
				jogFeed, parseErr := strconv.ParseFloat(msg.Value, 64)
				if parseErr != nil || jogFeed < 0 || jogFeed > 20 {
					logger.Error("[MANUAL_JOG] invalid frontend Jog Feed:", msg.Value)
					statusnotifier.Alarm("Invalid Manual Jog Feed")
					continue
				}
				err = ManualJog(device, msg.Direction, jogFeed)
			}
			if err != nil {
				logger.Error("[MANUAL_JOG] failed:", err)
				statusnotifier.Alarm(err.Error())
			}

		case channels.STOP_JOG:
			device := getDeviceForAction(msg)
			if device == nil {
				continue
			}
			StopJog(device)

		case channels.ZERO_REF:
			device := getDeviceForAction(msg)
			if device == nil {
				continue
			}
			go startZeroReference(device)

		case channels.STEP_MODE_ENABLE:
			logger.Debug("step mode enabled - configureDriver bypassed")

		case channels.STEP_MODE:
			device := getDeviceForAction(msg)
			if device == nil {
				continue
			}
			pos, _ := strconv.ParseFloat(msg.Value, 64)
			// Step mode is incremental. Its direction comes from the requested
			// physical direction, and is committed only after the PP move succeeds.
			if msg.Direction < 0 {
				pos = -math.Abs(pos)
			} else if msg.Direction > 0 {
				pos = math.Abs(pos)
			}
			stepMode(device, pos)

		case channels.SET_RPM:
			device := getDeviceForAction(msg)
			if device == nil {
				continue
			}
			rpm, _ := strconv.ParseInt(msg.Value, 0, 32)
			setRpm(device, int(rpm))

		case channels.MOVE_TO_POSITION:
			device := getDeviceForAction(msg)
			if device == nil {
				continue
			}
			degree, _ := strconv.ParseFloat(msg.Value, 64)
			go moveMotorToDegree(device, degree)

		case channels.START_EXECUTION:
			device := getDeviceForAction(msg)
			if device == nil {
				continue
			}
			logger.Trace("program exec started")
			notifyDriverStatus("reset", "", device)

		case channels.POSITION_MODE:
			device := getDeviceForAction(msg)
			if device == nil {
				continue
			}
			notifyDriverStatus("mode", msg.Value, device)

		case channels.SHORTEST_PATH_ENABLED:
			device := getDeviceForAction(msg)
			if device == nil {
				continue
			}
			notifyDriverStatus("shortest_path_enable", msg.Value, device)

		case channels.EMERGENCY:
			device := getDeviceForAction(msg)
			if device == nil {
				continue
			}
			// Emergency is destructive to the active program. A single reset event
			// aborts the execution generation and clears all persisted resume state.
			channels.WriteCommandExecInput("reset", "")
			stopECSCheck()
			emergency(device)
			PowerOffAll(getMasterDevices())
			statusnotifier.SocketMessage("emergency_done", "emergency completed")
			statusnotifier.Alarm("Software Emergency pressed")

		case channels.PROGRAM_EXEC_COMPLETED:
			device := getDeviceForAction(msg)
			if device == nil {
				continue
			}
			logger.Trace("program exec completed")
			notifyDriverStatus("reset", "", device)

		case channels.FAST_POWER_OFF:
			device := getDeviceForAction(msg)
			if device == nil {
				continue
			}
			FastPowerOff(device)

		case channels.RESET_MULTI_TURN:
			device := getDeviceForAction(msg)
			if device == nil {
				statusnotifier.Alarm("Multiturn reset failed: selected drive not found")
				continue
			}
			if device.Driver == nil {
				statusnotifier.Alarm("Multiturn reset is not supported for drive: " + device.Name)
				continue
			}

			// Snapshot Error 40 before the encoder-clear sequence. After a
			// successful clear the PDO error becomes zero, so checking afterward
			// cannot distinguish recovery from a normal coordinate-zero reset.
			wasError40 := normalizeErrorCode(int(device.PDOErr.Load()&0xFFFF)) == 40

			// Reset only the selected drive. Resetting all axes would redefine
			// unrelated encoder origins.
			if err := resetMultiTurn([]*MasterDevice{device}); err != nil {
				message := "Multiturn reset failed for drive " + device.Name + ": " + err.Error()
				logger.Error("[RESET_MULTI_TURN] ", message)
				statusnotifier.Alarm(message)
				statusnotifier.SocketMessage("multiturn_reset_failed", message)
				continue
			}

			encoderOrigin, machineHome, err := captureAndSaveHomingCalibration(device)
			if err != nil {
				message := "Multiturn reset completed, but home calibration failed for drive " +
					device.Name + ": " + err.Error()
				logger.Error("[RESET_MULTI_TURN] ", message)
				statusnotifier.Alarm(message)
				statusnotifier.SocketMessage("multiturn_reset_failed", message)
				continue
			}
			logger.Info("[RESET_MULTI_TURN] home calibration saved: drive=", device.Name,
				" encoderOrigin=", encoderOrigin, " machineHome=", machineHome)

			if wasError40 {
				message := "Multiturn reset successful. Error 40 cleared and drive restored. Home reference updated; run Zero Reference."
				logger.Info("[RESET_MULTI_TURN] ", message)
				statusnotifier.AlarmCleared()
				statusnotifier.SocketMessage("multiturn_reset_success", message)
				continue
			}

			message := "Multiturn reset successful. Home reference updated from Home Offset; run Zero Reference."
			logger.Info("[RESET_MULTI_TURN] ", message)
			statusnotifier.SocketMessage("multiturn_reset_success", message)

		case channels.SET_WORK_OFFSET:
			device := getDeviceForAction(msg)
			if device == nil {
				continue
			}
			notifyDriverStatusWithWait("workoffset", msg.Value, device)

		case channels.SETTINGS_CHANGED:
			applyClampIfSettingsChanged()
			recalculateCalibratedHomes()

		case channels.STOP_PROGRAM_EXECUTION:
			device := getDeviceForAction(msg)
			if device == nil {
				continue
			}
			stopECSCheck()
			if IsPDOActive() && device.PdoReady {
				if device.IsJogEnabled() {
					_ = StopJog(device)
					_ = device.SetTargetVelocityPDO(0)
					logger.Info("[PDO] STOP_PROGRAM_EXECUTION: jog stopped immediately")
				}
				if device.IsPosEnabled() {
					logger.Info("[PDO] STOP_PROGRAM_EXECUTION: position move allowed to complete before stopping")
				}
			}

		case channels.EXIT_DRIVE_LISTENER:
			logger.Debug("stopping driver action listener")
			isActionListening.Store(false)
			return

		default:
			logger.Error("listenDriverAction->unrecognized driver action type passed", msg.Action)
		}
	}
}

// captureStableEncoderPosition waits until the PDO encoder value is no longer
// changing. A multiturn reset can complete at the SDO level one or more PDO
// cycles before the new absolute value is visible to the application.
func captureStableEncoderPosition(device *MasterDevice) (int32, error) {
	if device == nil || !device.PdoReady {
		return 0, fmt.Errorf("PDO position is not ready")
	}
	const (
		stableSamples = 5
		stableDrift   = int64(1)
	)
	// Do not accept the still-stable pre-reset PDO value. Give the drive and
	// EtherCAT domain time to publish several frames with the new origin first.
	time.Sleep(300 * time.Millisecond)
	deadline := time.Now().Add(3 * time.Second)
	previous := int64(device.PDOPos.Load())
	stable := 0
	for time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		current := int64(device.PDOPos.Load())
		drift := current - previous
		if drift < 0 {
			drift = -drift
		}
		if drift <= stableDrift {
			stable++
			if stable >= stableSamples {
				return int32(current), nil
			}
		} else {
			stable = 0
		}
		previous = current
	}
	return 0, fmt.Errorf("encoder position did not settle after multiturn reset")
}

func homingAposFromOffset(device *MasterDevice, encoderOrigin int32) (int32, error) {
	if device == nil || device.Device.DriveXRatio <= 0 {
		return 0, fmt.Errorf("invalid drive_x_ratio")
	}
	ds := settings.GetDriverSettings(device.Name)
	offsetPulses := int64(math.Round(float64(ds.HomingOffset) * float64(device.Device.DriveXRatio)))
	home := int64(encoderOrigin) + offsetPulses
	if home < int64(-1<<31) || home > int64(1<<31-1) {
		return 0, fmt.Errorf("home pulse target overflow: origin=%d offset=%d", encoderOrigin, offsetPulses)
	}
	return int32(home), nil
}

func captureAndSaveHomingCalibration(device *MasterDevice) (int32, int32, error) {
	encoderOrigin, err := captureStableEncoderPosition(device)
	if err != nil {
		return 0, 0, err
	}
	machineHome, err := homingAposFromOffset(device, encoderOrigin)
	if err != nil {
		return 0, 0, err
	}
	if err := settings.SaveHomingCalibration(device.Name, encoderOrigin, machineHome, device.Device.DriveXRatio); err != nil {
		return 0, 0, err
	}
	device.AposCorrection.Store(0)
	return encoderOrigin, machineHome, nil
}

// recalculateCalibratedHomes applies a newly edited Home Offset to the encoder
// origin captured by the last multiturn reset. It never reads the current
// table position, so changing settings cannot make the reference drift.
func recalculateCalibratedHomes() {
	for _, device := range getMasterDevices() {
		ds := settings.GetDriverSettings(device.Name)
		if !ds.HomingValid {
			continue
		}
		machineHome, err := homingAposFromOffset(device, ds.EncoderZeroApos)
		if err != nil {
			logger.Error("[HOME-CAL] cannot apply Home Offset for ", device.Name, ": ", err)
			continue
		}
		if machineHome == ds.HomingApos && ds.HomingDriveXRatio == device.Device.DriveXRatio {
			continue
		}
		if err := settings.SaveHomingCalibration(device.Name, ds.EncoderZeroApos, machineHome, device.Device.DriveXRatio); err != nil {
			logger.Error("[HOME-CAL] cannot save recalculated home for ", device.Name, ": ", err)
			continue
		}
		logger.Info("[HOME-CAL] Home Offset applied: drive=", device.Name,
			" encoderOrigin=", ds.EncoderZeroApos, " oldHome=", ds.HomingApos,
			" newHome=", machineHome, " oldRatio=", ds.HomingDriveXRatio,
			" newRatio=", device.Device.DriveXRatio)
	}
}

func doneDriverAction() {
	channels.NotifyCmdComplete()
}
