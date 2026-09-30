package motordriver

import (
	channels "EtherCAT/channels"
	logger "EtherCAT/logger"
	statusnotifier "EtherCAT/motordriver/statusnotifier"
	"EtherCAT/settings"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ----------------------------------------------------------
// Precision configuration
// ----------------------------------------------------------
const (
	// Used only for duplicate-FIN detection, not position acceptance.
	positionToleranceFinal = 0.01
)

// motionMu prevents two moveMotorToDegree goroutines from running concurrently.
// Without this guard, two rapid MOVE_TO_POSITION messages spawn two goroutines
// that both pass the ECS-HIGH poll, both complete their settle loops, and both
// call sendECSFinSignal — causing the double finish-signal bug.
var motionMu sync.Mutex

// lastNormalFinMu guards the fields below, which record the destination and
// wall-clock time of the most recently sent fin signal on the NORMAL (non
// fast-path) completion code path.
//
// Why we need this:
//
//	The executor has a rare timing bug where it re-issues the command it just
//	completed (same line, same destination) before its internal line counter
//	advances.  When that duplicate arrives, the motor is already at the target
//	and the "already at target" fast path fires, sending a SECOND fin pulse.
//	The external machine counts each rising edge as a separate "move done"
//	event and steps its state machine twice, losing synchronisation.
//
//	Fix: after every normal-path move we stamp the destination and the time.
//	If the fast path is triggered for that SAME destination within a short
//	guard window (dupFinWindow), it is treated as the duplicate and the fin
//	signal is suppressed.  The executor is still unblocked via doneDriverAction
//	so program flow continues correctly.
var (
	lastNormalFinMu   sync.Mutex
	lastNormalFinDest float64
	lastNormalFinAt   time.Time
)

// dupFinWindow is the maximum gap between a completed normal move and a
// subsequent "already at target" fast-path call to the SAME destination that
// we consider a duplicate.  Chosen to be comfortably longer than the observed
// ~1 s anomaly window but shorter than the ~10 s loop period.
const dupFinWindow = 3 * time.Second

// ----------------------------------------------------------
// Helpers
// ----------------------------------------------------------

func clearTargetReached(device *MasterDevice) {
	logger.Debug("Clearing 'target reached' status for device:", device.Name)
}

func sleepMs(ms int) {
	time.Sleep(time.Duration(ms) * time.Millisecond)
}

// ----------------------------------------------------------
// Main Motion Function
// ----------------------------------------------------------

func moveMotorToDegree(device *MasterDevice, degreeToRotate float64) error {
	// Prevent concurrent executions from each sending a finish signal.
	// If a move is already in progress (e.g. duplicate channel message or
	// rapid successive commands), block here until the active move finishes.
	motionMu.Lock()
	defer motionMu.Unlock()

	driverStatus := getCurrentDriverStatus(device.Device.Name)
	// FIX: Bypass the asynchronous Go channel cache.
	// Read true position synchronously from the EtherCAT buffer to prevent skipped lines.
	truePos := ReadActualPositionFromDrive(device.Name)
	driverStatus.currentPosition = truePos
	if driverStatus.potNotExceeded {
		logger.Error("pot/not exceeded, exiting from move command")
		return errors.New("pot/not exceeded, exiting from move command")
	}

	uid := uuid.New()
	logger.Info("rotate motor to", degreeToRotate,
		"current:", driverStatus.currentPosition,
		"prev dest:", driverStatus.destinationPosition,
		"workoffset:", driverStatus.workOffset,
		"id:", uid,
	)

	var moveToPos, destination float64

	if driverStatus.mode == "ABS" {
		moveToPos, destination = getAbsolutePosition(
			driverStatus.currentPosition,
			degreeToRotate,
			driverStatus.shortestPathEnabled,
		)
	} else {
		moveToPos, destination = getRelativePosition(
			driverStatus.currentPosition,
			degreeToRotate,
			driverStatus.destinationPosition,
		)
	}

	statusnotifier.NotifyDestinationPosition(device.Name, float32(destination-driverStatus.workOffset))

	// ----------------------------------------------------------
	// Wait for ECS
	// ----------------------------------------------------------
	channels.WriteCommandExecInput("waiting_for_ecs", "")
	gotEcs := doECSCheck(device, destination)
	if gotEcs == 0 || gotEcs == 2 {
		return fmt.Errorf("failed to receive ECS — program stopped or reset")
	}
	channels.WriteCommandExecInput("ecs_done", "")

	// Refresh status after ECS latency
	driverStatusAfterECS := getCurrentDriverStatus(device.Device.Name)
	// FIX: Ensure the post-ECS status also uses the true hardware position
	driverStatusAfterECS.currentPosition = ReadActualPositionFromDrive(device.Name)

	// ----------------------------------------------------------
	// ABS fast-path: already at target, no motion needed
	// ----------------------------------------------------------
	if driverStatusAfterECS.mode == "ABS" &&
		driverStatusAfterECS.currentPosition == destination {

		// Double-check to guard against a stale cache read
		//recheck := getCurrentDriverStatus(device.Device.Name)
		// FIX: Check against hardware, not the async cache again
		trueRecheckPos := ReadActualPositionFromDrive(device.Name)
		if trueRecheckPos == destination {

			// ----------------------------------------------------------
			// Deduplication guard (double-fin prevention)
			// ----------------------------------------------------------
			// If the executor re-issues the same command it just completed
			// (a known rare timing/race in the executor), the motor is still
			// at the target and we would send a second fin pulse.  Check
			// whether a NORMAL-PATH fin was sent for this exact destination
			// within dupFinWindow and, if so, suppress the duplicate.
			lastNormalFinMu.Lock()
			recentSameDest := math.Abs(lastNormalFinDest-destination) <= positionToleranceFinal &&
				time.Since(lastNormalFinAt) < dupFinWindow
			lastNormalFinMu.Unlock()

			if recentSameDest {
				logger.Warn("[DEDUP] 'Already at target' for same destination as just-completed move — "+
					"suppressing duplicate fin signal. dest:", destination,
					"lastNormalFinDest:", lastNormalFinDest,
					"age:", time.Since(lastNormalFinAt).Round(time.Millisecond),
					"id:", uid)
				doneDriverAction()
				channels.DestinationReached()
				return nil
			}

			logger.Info("Already at target — sending finish immediately, id:", uid)

			// The normal path clamps inside doRotate. The fast path must enforce
			// the same rule before FIN, otherwise a previous jog can leave the
			// table declamped while the command is reported complete.
			envSettings := settings.GetDriverSettings(device.Name)
			if envSettings.ClampDeclamp == 1 {
				if _, clampErr := hasClamped(device, envSettings); clampErr != nil {
					logger.Error("[FAST-PATH] clamp failed:", clampErr)
					statusnotifier.Alarm(clampErr.Error())
					doneDriverAction()
					return clampErr
				}
			}
			sendECSFinSignal(device)

			channels.WriteCommandExecInput("waiting_for_ecs", "")
			if z := doECSCheckZero(device, destination); z == 0 || z == 2 {
				return fmt.Errorf("failed to receive ECS zero")
			}
			channels.WriteCommandExecInput("ecs_done", "")

			doneDriverAction()
			channels.DestinationReached()
			return nil
		}
	}

	// ----------------------------------------------------------
	// Set direction and store destination
	// ----------------------------------------------------------
	setDirection(device, driverStatusAfterECS, moveToPos)
	notifyDriverStatus("destination_position", fmt.Sprintf("%f", destination), device)

	// setDirection updates the status keeper. Refresh it before reading
	// backlash so this movement uses the current direction's compensation.
	driverStatusForCompensation := getCurrentDriverStatus(device.Device.Name)
	backlash := driverStatusForCompensation.backlash
	if backlash < 0 {
		logger.Warn("[BACKLASH] negative value configured; using positive magnitude:", -backlash)
		backlash = -backlash
	}
	pitchErr := getPitchError(device.Name, destination)
	moveToWithComp := moveToPos + pitchErr - backlash
	logger.Info("[POSITION-COMP] direction=", driverStatusForCompensation.direction,
		" path=", moveToPos, " pitch=", pitchErr, " backlash=", backlash,
		" command=", moveToWithComp)

	clearTargetReached(device)

	var rotErr error
	if driverStatusAfterECS.mode == "ABS" {
		// Build one fixed encoder target from the saved zero reference. The live
		// position participates only in equivalent-revolution selection inside
		// calculateFixedAbsoluteGoal; it cannot change the destination count.
		absoluteGoal, goalErr := calculateFixedAbsoluteGoal(
			device,
			destination,
			moveToPos,
			pitchErr,
			backlash,
		)
		if goalErr != nil {
			rotErr = goalErr
		} else {
			rotErr = doRotateToAbsoluteGoal(device, absoluteGoal)
		}
	} else {
		// Relative mode intentionally retains current-position-plus-delta
		// semantics. Jog, step and zero-reference behavior are unchanged.
		rotErr = doRotate(device, moveToWithComp)
	}
	if rotErr != nil {
		errMsg := fmt.Sprintf("[PDO-PP] move failed: %v", rotErr)
		logger.Error(errMsg)
		statusnotifier.Alarm(errMsg)
		// CRITICAL: call doneDriverAction so the executor does NOT hang forever on
		// WaitExecuteNextCommand(). Without this, a failed move blocks the program
		// indefinitely — the user would have to manually click Stop every time.
		doneDriverAction()
		return rotErr
	}

	// NOTE: No settle loop here.
	// hasTargetReached() inside doRotate already confirmed bit10=1 AND bit12=0
	// stable for 50 consecutive 1ms PDO cycles before returning. Re-polling
	// position + bit10 again would add up to 20ms of unnecessary latency on
	// every move with zero safety benefit.

	if IsPDOActive() && device.PdoPosReady {
		logger.Info("[PDO-PP] position AND bit10 confirmed, sending finish signal. id:", uid)
	}

	// ----------------------------------------------------------
	// Send finish signal and stamp the deduplication tracker
	// ----------------------------------------------------------
	sendECSFinSignal(device)

	// Record this destination + time so the "already at target" fast path can
	// detect a rapid duplicate command from the executor and suppress the
	// spurious second fin pulse (see dupFinWindow / lastNormalFin vars above).
	lastNormalFinMu.Lock()
	lastNormalFinDest = destination
	lastNormalFinAt = time.Now()
	lastNormalFinMu.Unlock()

	// ----------------------------------------------------------
	// Wait for ECS zero
	// ----------------------------------------------------------
	channels.WriteCommandExecInput("waiting_for_ecs", "")
	if z := doECSCheckZero(device, destination); z == 0 || z == 2 {
		return fmt.Errorf("failed to receive ECS zero")
	}
	channels.WriteCommandExecInput("ecs_done", "")

	doneDriverAction()
	channels.DestinationReached()
	logger.Info("move to position completed, driver:", device.Name, "id:", uid)
	return nil
}

// ----------------------------------------------------------
// Step Mode
// ----------------------------------------------------------

func stepMode(masterDevice *MasterDevice, valueInDegreeToAdd float64) error {
	logger.Debug("step mode moving to position:", valueInDegreeToAdd)

	driverStatus := getCurrentDriverStatus(masterDevice.Device.Name)
	if driverStatus.potNotExceeded {
		logger.Error("pot/not exceeded, exiting from move command")
		channels.StepModeComplete()
		return errors.New("pot/not exceeded, exiting from move command")
	}

	err := freeRotate(masterDevice, valueInDegreeToAdd)
	channels.StepModeComplete()
	return err
}

// ----------------------------------------------------------
// Direction Selection
// ----------------------------------------------------------

func setDirection(device *MasterDevice, driverStatus driverCurrentStatus, degreeToRotate float64) {
	if !driverStatus.shortestPathEnabled {
		notifyDriverStatusWithWait("rotation_direction", "1", device)
		return
	}
	if degreeToRotate > 0 {
		notifyDriverStatusWithWait("rotation_direction", "1", device)
	} else {
		notifyDriverStatusWithWait("rotation_direction", "-1", device)
	}
}

type movementDirectionPlan struct {
	direction         int
	previousBacklash float64
	nextBacklash     float64
	reversed         bool
}

// planMovementDirection is deliberately read-only. It determines the physical
// direction and the backlash coordinate side needed by the proposed move, but
// does not modify driverStatus. The state is committed only after PP succeeds.
func planMovementDirection(driverStatus driverCurrentStatus, signedMovement float64) movementDirectionPlan {
	previousDirection := driverStatus.direction
	previousBacklash := positiveBacklashMagnitude(driverStatus.backlash)
	if previousDirection == 0 {
		previousDirection = 1
		previousBacklash = 0
	}

	plan := movementDirectionPlan{
		previousBacklash: previousBacklash,
		nextBacklash:     previousBacklash,
	}

	switch {
	case signedMovement > 0:
		plan.direction = 1
	case signedMovement < 0:
		plan.direction = -1
	default:
		return plan
	}

	plan.reversed = previousDirection != plan.direction
	if plan.reversed {
		// The calibrated encoder coordinate uses CW as the zero-backlash side and
		// CCW as the configured negative offset side. This changes once on a
		// reversal and remains active for subsequent moves in the same direction.
		if plan.direction < 0 {
			plan.nextBacklash = positiveBacklashMagnitude(driverStatus.backlashInSetting)
		} else {
			plan.nextBacklash = 0
		}
	}

	return plan
}

func commitMovementDirection(device *MasterDevice, plan movementDirectionPlan) {
	if device == nil || plan.direction == 0 {
		return
	}
	notifyDriverStatusWithWait(
		"rotation_direction",
		fmt.Sprintf("%d", plan.direction),
		device,
	)
	logger.Info("[DIRECTION-COMMIT] drive=", device.Name,
		" direction=", plan.direction,
		" reversed=", plan.reversed,
		" backlash=", plan.nextBacklash)
}

// positiveBacklashMagnitude normalises a configured backlash value. Settings
// should contain a positive magnitude, but accepting a negative legacy value
// here avoids reversing the compensation sign unexpectedly.
func positiveBacklashMagnitude(backlash float64) float64 {
	if backlash < 0 {
		logger.Warn("[BACKLASH] negative value configured; using positive magnitude:", -backlash)
		return -backlash
	}
	return backlash
}

// positionForMotionPlanning converts the raw machine-coordinate angle into the
// physical/display-equivalent angle used to plan the next path. A compensated
// move intentionally leaves the encoder target offset by pitch/backlash; adding
// the active backlash and removing the active pitch correction prevents that
// intentional encoder offset from looking like a new positioning error.
func positionForMotionPlanning(
	device *MasterDevice,
	driverStatus driverCurrentStatus,
	rawPosition float64,
) float64 {
	if device == nil {
		return rawPosition
	}

	pitchErr := getPitchError(device.Name, driverStatus.destinationPosition)
	position := rawPosition - pitchErr + positiveBacklashMagnitude(driverStatus.backlash)
	position = math.Mod(position, 360)
	if position < 0 {
		position += 360
	}
	if position >= 359.999 {
		position = 0
	}
	return position
}

// prepareFixedAbsoluteGoal is the shared target-preparation path for every
// absolute program move (including A0) and Zero Reference.
//
// It deliberately owns the complete sequence:
//   1. register the actual movement direction,
//   2. let the direction state machine decide whether this is a reversal,
//   3. read the resulting one-time backlash compensation,
//   4. apply pitch correction, and
//   5. build one fixed encoder target from HomingApos + AposCorrection.
//
// Keeping this in one helper prevents A0 and Zero Ref from silently using
// different compensation rules again.
func prepareFixedAbsoluteGoal(
	device *MasterDevice,
	driverStatus driverCurrentStatus,
	destination float64,
	signedPath float64,
) (goal int32, pitchErr float64, backlash float64, directionPlan movementDirectionPlan, err error) {
	if device == nil {
		return 0, 0, 0, movementDirectionPlan{}, errors.New("prepareFixedAbsoluteGoal: nil device")
	}

	directionPlan = planMovementDirection(driverStatus, signedPath)
	backlash = directionPlan.nextBacklash
	pitchErr = getPitchError(device.Name, destination)

	goal, err = calculateFixedAbsoluteGoal(
		device,
		destination,
		signedPath,
		pitchErr,
		backlash,
	)
	if err != nil {
		return 0, pitchErr, backlash, directionPlan, err
	}

	logger.Info("[POSITION-COMP ABS] previousDirection=", driverStatus.direction,
		" plannedDirection=", directionPlan.direction,
		" reversed=", directionPlan.reversed,
		" path=", signedPath,
		" destination=", destination,
		" pitch=", pitchErr,
		" backlash=", backlash,
		" goal=", goal)

	return goal, pitchErr, backlash, directionPlan, nil
}

// ----------------------------------------------------------
// Position Sync Helpers
// ----------------------------------------------------------

// RefreshCurrentPosition reads the actual encoder position from every drive
// and updates the internal status cache for each.
//
// FIX (multi-axis): the old version hard-coded devices[0], so after a PP move
// on Axis B the cached position for Axis A was refreshed instead — leaving the
// executor's "current position" stale for all axes except the first.
// Now all configured devices are refreshed in the same call.
func RefreshCurrentPosition() {
	devices := getMasterDevices()
	if len(devices) == 0 {
		logger.Warn("[SYNC] RefreshCurrentPosition: no master devices available")
		return
	}
	for _, dev := range devices {
		var rawPulses int32
		if dev.PdoReady {
			rawPulses = dev.PDOPos.Load()
		} else {
			// SDO fallback: read 0x6064 directly
			operation, opErr := GetEtherCATOperation("pollstatus", dev.Device.AddressConfigName)
			if opErr != nil || len(operation.Steps) == 0 {
				logger.Error("[SYNC] RefreshCurrentPosition: could not get pollstatus operation:", opErr)
				continue
			}
			val, sdoErr := DrivePosition(dev.Master, dev.Position, operation.Steps[0])
			if sdoErr != nil {
				logger.Error("[SYNC] RefreshCurrentPosition: SDO read failed:", sdoErr)
				continue
			}
			rawPulses = val
		}

		degrees, _ := currentPosition(rawPulses, dev.Device.DriveXRatio, dev.Name)
		currentDriverPosition(dev, degrees)
		logger.Info(fmt.Sprintf("[SYNC] Position refreshed to %.3f° (raw: %d) for drive %s",
			degrees, rawPulses, dev.Name))
	}
}

// ReadActualPositionFromDrive returns the current position in degrees directly
// from the PDO buffer (or SDO on fallback), NOT from the cached status keeper.
func ReadActualPositionFromDrive(driveName string) float64 {
	devices := getMasterDevices()
	for _, dev := range devices {
		if dev.Name == driveName {
			var rawPulses int32
			if dev.PdoReady {
				rawPulses = dev.PDOPos.Load()
			} else {
				operation, opErr := GetEtherCATOperation("pollstatus", dev.Device.AddressConfigName)
				if opErr != nil || len(operation.Steps) == 0 {
					break
				}
				val, sdoErr := DrivePosition(dev.Master, dev.Position, operation.Steps[0])
				if sdoErr != nil {
					break
				}
				rawPulses = val
			}
			degrees, _ := currentPosition(rawPulses, dev.Device.DriveXRatio, driveName)
			return degrees
		}
	}
	// Last resort: return cached value
	return getCurrentDriverStatus(driveName).currentPosition
}
