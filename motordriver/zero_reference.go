package motordriver

import (
	channels "EtherCAT/channels"
	logger "EtherCAT/logger"
	notifier "EtherCAT/motordriver/statusnotifier"
	"EtherCAT/settings"
	"fmt"
	"math"
	"sync/atomic"
	//"time"
)

// zeroRefFeed is the fixed feed rate used exclusively for zero-reference moves.
// This is intentionally decoupled from JogFeed so the customer can freely
// change jog speed without affecting the homing move speed.
// Unit: same as JogFeed (multiplied by RPMConst inside setRpm to get counts/sec).
const zeroRefFeed = 10

var zeroReferenceActive atomic.Bool

// IsZeroReferenceActive allows the program executor to reject a Run command
// while zero-reference owns the position PDO state.
func IsZeroReferenceActive() bool {
	return zeroReferenceActive.Load()
}

func startZeroReference(device *MasterDevice) {
	if !zeroReferenceActive.CompareAndSwap(false, true) {
		logger.Warn("[PDO-ZERO] zero reference is already active")
		return
	}
	defer zeroReferenceActive.Store(false)
	if err := moveToZero(device); err != nil {
		logger.Error("[PDO-ZERO] failed:", err)
		notifier.Alarm(err.Error())
	}
}

// moveToZero moves the motor to the absolute zero reference position using
// the shortest path.
//
// FIX (Bug 15): The old version called configureDriver(device) immediately
// before freeRotate.  configureDriver sends SDO initialisation sequences
// (operating mode, gains, limits …) to the drive.  Calling it during live
// PDO-cyclic operation is unsafe: the IgH EtherCAT master does not serialise
// SDO requests against the cyclic send/receive, so the two can collide and
// produce frame errors or drive faults.
//
// configureDriver belongs in the one-time startup sequence (InitMaster) where
// the master has not yet been activated.  It must NOT be called from any
// motion function.  It has been removed here; the drive is already correctly
// configured at boot time.
func moveToZero(device *MasterDevice) error {
	// Use the same motion lock as program positioning so Zero Ref and an A-axis
	// command can never own the Profile Position PDO state concurrently.
	motionMu.Lock()
	defer motionMu.Unlock()

	logger.Debug("move to zero started")

	envSettings := settings.GetDriverSettings(device.Name)

	const targetPosition = 0.0
	notifier.NotifyDestinationPosition(device.Name, float32(targetPosition))
	notifyDriverStatus("destination_position", fmt.Sprintf("%f", targetPosition), device)

	// --- PDO ONLY ---
	if !(IsPDOActive() && device.PdoPosReady) {
		return fmt.Errorf("PDO position not ready (PdoPosReady=false). Zero reference is PDO-only in this build")
	}
	if !envSettings.HomingValid {
		return fmt.Errorf("zero reference is not calibrated for %s; perform multiturn reset and apply Home Offset", device.Name)
	}
	if device.Device.DriveXRatio <= 0 {
		return fmt.Errorf("zero reference requires a positive drive_x_ratio for %s", device.Name)
	}

	// HomingApos is the one fixed raw encoder count representing machine 0 deg.
	// This uncompensated home target is used only for the no-motion fast path.
	// When movement is required, the final target is prepared by the exact same
	// direction/backlash/pitch/fixed-home helper used by the A0 command.
	ratio := int64(device.Device.DriveXRatio)
	countsPerRevolution := int64(360) * ratio
	currentPulse := int64(device.PDOPos.Load())
	fixedHome := int64(envSettings.HomingApos) + int64(device.AposCorrection.Load())
	revolution := nearestIntegerQuotient(currentPulse-fixedHome, countsPerRevolution)
	uncompensatedGoal64 := fixedHome + revolution*countsPerRevolution
	if uncompensatedGoal64 < int64(-1<<31) || uncompensatedGoal64 > int64(1<<31-1) {
		return fmt.Errorf("zero-reference target overflow for %s: %d", device.Name, uncompensatedGoal64)
	}
	uncompensatedTarget := int32(uncompensatedGoal64)

	// If the previous command already used backlash compensation, the physical
	// zero can intentionally sit away from raw HomingApos. Build the no-motion
	// target with the currently active compensation before deciding whether a
	// new PP command is necessary.
	driverStatus := getCurrentDriverStatus(device.Device.Name)
	driverStatus.currentPosition = positionForMotionPlanning(
		device,
		driverStatus,
		ReadActualPositionFromDrive(device.Name),
	)
	activeBacklash := positiveBacklashMagnitude(driverStatus.backlash)
	stationaryPitch := getPitchError(device.Name, targetPosition)
	targetPulse, stationaryGoalErr := calculateFixedAbsoluteGoal(
		device,
		targetPosition,
		0,
		stationaryPitch,
		activeBacklash,
	)
	if stationaryGoalErr != nil {
		return stationaryGoalErr
	}
	residual := int64(targetPulse) - currentPulse
	if residual < 0 {
		residual = -residual
	}
	logger.Info("[PDO-ZERO ABS] drive=", device.Name,
		" fixedHome=", fixedHome,
		" current=", currentPulse,
		" revolution=", revolution,
		" uncompensatedTarget=", uncompensatedTarget,
		" activeBacklash=", activeBacklash,
		" stationaryTarget=", targetPulse,
		" residual=", residual)

	// Fast path: already inside the calibrated zero tolerance — skip PP
	// handshake entirely.
	//
	// CONFIRMED ON HARDWARE via the cold-boot test: sending a redundant
	// set-point identical to the drive's current position gets no bit-12
	// acknowledgment (the drive has nothing new to confirm), which caused a
	// real 2-second Phase1 handshake timeout in hasTargetReached — every
	// time zero-reference was called while already sitting at the zero
	// position (a completely normal case, e.g. re-homing at startup).
	//
	// Mirrors the same "already at target" fast-path concept already
	// established and tested in moveMotorToDegree, simplified here since
	// moveToZero does not participate in that function's ECS synchronization
	// protocol (no doECSCheck/dedup-window logic applies to zero-reference).
	if residual <= int64(positionPulseTolerance) {
		logger.Info("[PDO-ZERO] Already inside fixed zero tolerance — skipping PP handshake. target=",
			targetPulse, " actual=", device.PDOPos.Load(), " residual=", residual)
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

	// Build the compensated goal with the same fixed-home mathematics as A0.
	// Zero Ref always takes the shortest path, independently of the current G68
	// state. The selected physical direction is used for reversal/backlash
	// planning and is committed only after the move succeeds.
	signedPath, destination := getAbsolutePosition(
		driverStatus.currentPosition,
		targetPosition,
		true,
	)

	compensatedGoal, pitchErr, backlash, directionPlan, goalErr := prepareFixedAbsoluteGoal(
		device,
		driverStatus,
		destination,
		signedPath,
	)
	if goalErr != nil {
		return goalErr
	}
	targetPulse = compensatedGoal
	compensatedResidual := int64(targetPulse) - currentPulse
	if compensatedResidual < 0 {
		compensatedResidual = -compensatedResidual
	}
	logger.Info("[PDO-ZERO A0-COMP] drive=", device.Name,
		" currentDeg=", driverStatus.currentPosition,
		" signedPath=", signedPath,
		" destination=", destination,
		" pitch=", pitchErr,
		" backlash=", backlash,
		" uncompensatedTarget=", uncompensatedTarget,
		" compensatedTarget=", targetPulse,
		" residual=", compensatedResidual)

	// The compensated target can occasionally equal the current encoder count
	// after rounding. Avoid sending a redundant PP set-point in that case.
	if compensatedResidual <= int64(positionPulseTolerance) {
		logger.Info("[PDO-ZERO] Already inside compensated A0 tolerance — skipping PP handshake. target=",
			targetPulse, " actual=", device.PDOPos.Load(), " residual=", compensatedResidual)
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

	// Use fixed zeroRefFeed constant — NOT envSettings.JogFeed.
	// This keeps zero-ref speed constant regardless of what the operator
	// sets for manual jog speed.
	logger.Info("[PDO-ZERO] setting homing speed. zeroRefFeed:", zeroRefFeed)
	if err := setRpm(device, zeroRefFeed); err != nil {
		logger.Warn("[PDO-ZERO] Could not set homing speed:", err)
	}

	// Use the exact same single-command PP executor as absolute program moves.
	// Target calculation remains fixed/home-referenced above; only execution is
	// shared, matching the reliable structure used by test environment23.
	logger.Info("[PDO-ZERO] one-shot PP move using A0 absolute target. current:", currentPulse, "target:", targetPulse)
	if err := runPositionGoal(device, targetPulse, "ZERO"); err != nil {
		return err
	}

	// Match A0: record the physical direction only after the common PP target,
	// settle and clamp sequence has completed successfully.
	commitMovementDirection(device, directionPlan)

	channels.DestinationReached()
	notifier.SocketMessage("gotozero_done", "goto zero completed")
	sendECSFinSignal(device)
	return nil
}

// shortestPathToZero returns the signed angular delta to reach 0° along the shortest arc.
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
// If clockwise is true, the result is the clockwise arc; otherwise counter-clockwise.
func getPos(currentPos float64, targetPos float64, clockwise bool) float64 {
	currentPos = math.Mod(currentPos, 360)
	modeDiff := math.Mod((currentPos - targetPos), 360)

	if clockwise {
		return math.Mod((360 - modeDiff), 360)
	}
	return math.Mod((modeDiff * -1), 360)
}
