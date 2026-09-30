package motordriver

import (
	channels "EtherCAT/channels"
	logger "EtherCAT/logger"
	notifier "EtherCAT/motordriver/statusnotifier"
	"EtherCAT/settings"
	"fmt"
	"math"
)

// zeroRefFeed is the fixed feed rate used exclusively for zero-reference moves.
// It is intentionally independent from JogFeed so operator jog-speed changes do
// not change the zero-reference move speed.
const zeroRefFeed = 10

// startZeroReference keeps compatibility with newer action listeners that call
// a wrapper instead of invoking moveToZero directly.
func startZeroReference(devices ...*MasterDevice) {
	var device *MasterDevice
	if len(devices) > 0 {
		device = devices[0]
	} else {
		masterDevices := getMasterDevices()
		if len(masterDevices) > 0 {
			device = masterDevices[0]
		}
	}
	if device == nil {
		err := fmt.Errorf("[PDO-ZERO] no master device available")
		logger.Error(err)
		notifier.Alarm(err.Error())
		doneDriverAction()
		return
	}
	if err := moveToZero(device); err != nil {
		logger.Error("[PDO-ZERO] failed:", err)
		notifier.Alarm(err.Error())
		doneDriverAction()
	}
}

// moveToZero moves to the fixed machine-zero encoder coordinate.
//
// Machine-zero policy:
//   - HomeOffset saved by the operator is the source of truth.
//   - Zero Reference is a MOVE to that saved zero, not a calibration action.
//   - HomingApos is not rewritten here.
//   - The final successful PP target remains enabled so Panasonic A6 continues
//     actively holding the exact indexed encoder position after FIN.
func moveToZero(device *MasterDevice) error {
	logger.Debug("move to zero started")

	driverStatus := getCurrentDriverStatus(device.Device.Name)
	envSettings := settings.GetDriverSettings(device.Name)

	const targetPosition = 0.0
	notifier.NotifyDestinationPosition(device.Name, float32(targetPosition))
	notifyDriverStatus("destination_position", fmt.Sprintf("%f", targetPosition), device)

	degToMove := shortestPathToZero(driverStatus.currentPosition, envSettings.HomeDirection)
	logger.Debug("zero ref degToMove (RS232 style):", degToMove)

	if !(IsPDOActive() && device.PdoPosReady) {
		return fmt.Errorf("PDO position not ready (PdoPosReady=false). Zero reference is PDO-only in this build")
	}

	// Use the EXACT SAME directional backlash state machine as normal ABS moves.
	// This is required for mechanical repeatability: A0 and Zero Reference must
	// take up gearbox/table play in the same way for the same travel direction.
	direction := 1
	if degToMove < 0 {
		direction = -1
	}
	notifyDriverStatusWithWait("rotation_direction", fmt.Sprintf("%d", direction), device)

	// rotation_direction updates driverStatus.backlash according to the existing
	// RTC/FRD direction policy. Refresh after the synchronous update so Zero uses
	// the same compensation value that A0/A30/A90 use.
	driverStatusForCompensation := getCurrentDriverStatus(device.Device.Name)
	backlash := driverStatusForCompensation.backlash
	if backlash < 0 {
		logger.Warn("[BACKLASH] negative value configured; using positive magnitude:", -backlash)
		backlash = -backlash
	}
	logger.Info("[ZERO-COMP] direction=", direction,
		" degToMove=", degToMove,
		" backlash=", backlash)

	// ZERO REFERENCE IS A MOVE, NOT A CALIBRATION.
	// Use the same fixed absolute target engine as G90 absolute positioning so
	// repeated zero commands cannot accumulate previous positioning residuals.
	currentPulse := device.PDOPos.Load()
	targetPulse, targetErr := calculateFixedAbsoluteGoal(
		device,
		targetPosition,
		degToMove,
		0.0,      // zero datum: no pitch compensation
		backlash, // SAME directional backlash rule as normal ABS positioning
	)
	if targetErr != nil {
		return targetErr
	}

	logger.Info("[PDO-ZERO-FIXED] current=", currentPulse,
		" target=", targetPulse,
		" homeOffset=", envSettings.HomingOffset,
		" direction=", direction,
		" backlash=", backlash)

	// If PP hold from the previous successful zero/indexing move has kept us
	// exactly on the machine-zero target, do not create a redundant new-setpoint
	// handshake. Complete immediately while preserving the existing PP hold.
	residual := int64(targetPulse) - int64(currentPulse)
	if residual < 0 {
		residual = -residual
	}
	if residual <= int64(positionPulseTolerance) {
		logger.Info("[PDO-ZERO] Already at fixed machine zero — finishing immediately. target=",
			targetPulse, " actual=", currentPulse, " residual=", residual,
			" HomeOffset unchanged.")

		// Preserve exact position regulation. If PP was not already enabled,
		// arm the current fixed zero target without creating a motion request.
		if !device.IsPosEnabled() {
			if err := device.SetTargetPositionPDO(targetPulse); err == nil {
				device.EnablePosPDO(true)
			}
		}

		notifyDriverStatus("motor_running", "false", device)

		_, clampErr := hasClamped(device, envSettings)
		if clampErr != nil {
			logger.Error(clampErr)
			return clampErr
		}

		channels.DestinationReached()
		notifier.SocketMessage("gotozero_done", "goto zero completed")
		sendECSFinSignal(device)
		return nil
	}

	logger.Info("[PDO-ZERO] setting homing speed. zeroRefFeed:", zeroRefFeed)
	if err := setRpm(device, zeroRefFeed); err != nil {
		logger.Warn("[PDO-ZERO] Could not set homing speed:", err)
	}

	logger.Info("[PDO-ZERO] Moving to fixed machine-zero target. current:",
		currentPulse, "target:", targetPulse)

	// Use the common PP executor. Its target construction is unchanged; the only
	// new post-move behaviour is that a successful target remains held in Mode 1.
	if err := runPositionGoal(device, targetPulse, "ZERO"); err != nil {
		return err
	}

	logger.Info("[PDO-ZERO HOLD] target=", targetPulse,
		" actual=", device.PDOPos.Load(),
		" posEnabled=", device.IsPosEnabled(),
		" HomeOffset unchanged")

	channels.DestinationReached()
	notifier.SocketMessage("gotozero_done", "goto zero completed")
	sendECSFinSignal(device)
	return nil
}

// shortestPathToZero returns the signed angular delta to reach 0 degrees along
// the shortest arc. HomeDirection is used only to break an exact 180-degree tie.
func shortestPathToZero(currentPosition float64, homeDirection int) float64 {
	cwDeg := getPos(currentPosition, 0, true)
	ccwDeg := getPos(currentPosition, 0, false)

	switch {
	case math.Abs(cwDeg) < math.Abs(ccwDeg):
		return cwDeg
	case math.Abs(ccwDeg) < math.Abs(cwDeg):
		return ccwDeg
	default:
		if homeDirection == 1 {
			return cwDeg
		}
		return ccwDeg
	}
}

// getPos returns the angular distance to travel from currentPos to targetPos.
// Positive is clockwise and negative is counter-clockwise.
func getPos(currentPos float64, targetPos float64, clockwise bool) float64 {
	currentPos = math.Mod(currentPos, 360)
	modeDiff := math.Mod((currentPos - targetPos), 360)

	if clockwise {
		return math.Mod((360 - modeDiff), 360)
	}
	return math.Mod((modeDiff * -1), 360)
}
