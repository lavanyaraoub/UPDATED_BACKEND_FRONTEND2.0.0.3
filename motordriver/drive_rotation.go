package motordriver

import (
	helper "EtherCAT/helper"
	logger "EtherCAT/logger"
	"EtherCAT/motordriver/statusnotifier"
	settings "EtherCAT/settings"
	"errors"
	"fmt"
	"math"
	"time"
)

const (
	// The previous 500-count gate could itself exceed the complete indexing
	// scatter allowance (500 counts = 90 arcsec at 20000 counts/degree).
	// Ten counts is 1.8 arcsec at 20000 counts/degree and 0.3 arcsec at
	// 120000 counts/degree.
	positionPulseTolerance = int32(10)

	// Match Mani build: after drive Target Reached is stable, wait briefly,
	// log the final position, then disable PP.
	positionSettleTime = 100 * time.Millisecond
)

// reverseDir writes the direction-reversal polarity parameter via SDO.
// Safe only before ecrt_master_activate — called exclusively from InitMaster.
func reverseDir(masterDevice *MasterDevice) error {
	if IsPDOActive() {
		logger.Warn("[PDO] reverseDir: called after activation — skipping (direction set at startup only)")
		return nil
	}
	logger.Trace("Reverse direction activated for driver", masterDevice.Name)
	operation, err := GetEtherCATOperation("reverse", masterDevice.Device.AddressConfigName)
	if err != nil {
		return err
	}
	runSDOOperation(masterDevice.Master, masterDevice.Position, operation)
	return nil
}

// nonReverseDir writes the non-reversed direction polarity parameter via SDO.
// Safe only before ecrt_master_activate — called exclusively from InitMaster.
func nonReverseDir(masterDevice *MasterDevice) error {
	if IsPDOActive() {
		logger.Warn("[PDO] nonReverseDir: called after activation — skipping (direction set at startup only)")
		return nil
	}
	logger.Trace("Non reverse direction activated for driver", masterDevice.Name)
	operation, err := GetEtherCATOperation("nonreverse", masterDevice.Device.AddressConfigName)
	if err != nil {
		return err
	}
	runSDOOperation(masterDevice.Master, masterDevice.Position, operation)
	return nil
}

// ManualJog starts jogging. Direction can be 1 (clockwise) or -1
// (counter-clockwise). jogFeedOverride is optional so legacy callers continue
// to use the saved Machine Parameters value while the new frontend can supply
// its live speedometer value for this jog only.
func ManualJog(masterDevice *MasterDevice, direction int, jogFeedOverride ...float64) error {
	driverStatus := getCurrentDriverStatus(masterDevice.Device.Name)
	if isMotionBlockedByPotNotLimit(driverStatus, direction) {
		logger.Error("pot/not exceeded, exiting from move command")
		return errors.New("pot/not exceeded, exiting from move command")
	}
	logger.Trace("manual jog, driver: ", masterDevice.Name)
	envSettings := settings.GetDriverSettings(masterDevice.Name)
	jogFeed := float64(envSettings.JogFeed)
	jogFeedSource := "MACHINE_PARAMETER"
	if len(jogFeedOverride) > 0 {
		jogFeed = jogFeedOverride[0]
		jogFeedSource = "MANUAL_UI"
		if jogFeed < 0 || jogFeed > 20 {
			return fmt.Errorf("[JOG] invalid Manual UI JogFeed=%.6f; allowed range 0.00..20.00", jogFeed)
		}
	}

	if err := FastPowerOn(masterDevice); err != nil {
		logger.Error(err)
		statusnotifier.Alarm(err.Error())
		return err
	}

	_, declampErr := hasDeclamped(masterDevice, envSettings)
	if declampErr != nil {
		logger.Error(declampErr)
		statusnotifier.Alarm(declampErr.Error())
		return declampErr
	}

	notifyDriverStatus("motor_running", "true", masterDevice)

	// Calculate target velocity. RPMConst still comes from Sumit's active-drive
	// configuration; only the per-command JogFeed source is selectable.
	rpm := direction * int(float64(masterDevice.Device.RPMConst)*jogFeed)

	if !IsPDOActive() || !masterDevice.PdoJogReady {
		return fmt.Errorf("[JOG] PDO not active or RxPDO not ready — jog is PDO-only in this build")
	}

	// Hand the velocity setpoint to the cyclic task in a single atomic write.
	// Matches old build: SetJogPDOSetpoints(0x000F, 3, rpm) — no software ramp.
	//
	// WHY NO SOFTWARE RAMP (explanation for future developers):
	//   A goroutine that steps velocity every 10ms generates a 100 Hz torque
	//   modulation — squarely in the human hearing range — perceived as a
	//   continuous beep from the motor windings during the entire ramp.
	//   The drive's own 0x6083 profile acceleration ramp handles speed-up
	//   internally and silently.  No code timer needed.
	//
	// VELOCITY UNIT NOTE (Delta ASDA-A2-E, 0x60FF):
	//   0x60FF Target Velocity uses encoder counts/second in Mode 3.
	//   With drive-x-ratio=20000 counts/rev and e.g. 600 RPM target:
	//   targetVel = 600 × 20000 / 60 = 200,000 counts/s.
	//   rpm-const in device-configuration.yml must be set so that
	//   rpm-const × JogFeed produces a value in that range.
	//   If the value logged below is < 1000, the unit is likely wrong
	//   and the motor runs in an unstable near-zero velocity region (noisy).
	logger.Info("[PDO-JOG] Sending to cyclic. targetVel=", rpm,
		" rpm-const=", masterDevice.Device.RPMConst,
		" JogFeed=", jogFeed,
		" source=", jogFeedSource,
		" drive=", masterDevice.Name)

	if err := masterDevice.SetJogPDOSetpoints(0x000F, 3, int32(rpm)); err != nil {
		notifyDriverStatus("motor_running", "false", masterDevice)
		return fmt.Errorf("[JOG] failed to set PDO setpoints: %w", err)
	}
	masterDevice.EnableJogPDO(true)

	// Commit only after the PDO jog command has been accepted and enabled. A
	// rejected jog must not change the direction used by the next position move.
	commitMovementDirection(masterDevice, planMovementDirection(driverStatus, float64(direction)))

	return nil
}

// ppHoldDriver is implemented by drivers that hold position in Profile
// Position (Mode 1) while idle instead of Profile Velocity (Mode 3, vel=0).
// Currently only InovanceSV680N. Panasonic A6 and Delta keep their existing
// StopJog / emergency behaviour unchanged.
type ppHoldDriver interface {
	HoldInPPAfterMotion() bool
}

// usesPPHold reports whether this device's driver opts into PP-hold handling.
func usesPPHold(masterDevice *MasterDevice) bool {
	if masterDevice == nil || masterDevice.Driver == nil {
		return false
	}
	h, ok := masterDevice.Driver.(ppHoldDriver)
	return ok && h.HoldInPPAfterMotion()
}

const (
	// ppHoldStandstillSample: position sampling interval while waiting for the
	// motor to stop after a jog.
	ppHoldStandstillSample = 5 * time.Millisecond
	// ppHoldStandstillCounts: |Δposition| per sample treated as stopped.
	// Units are the drive's USER units (0x6064 after the 0x6091 gear ratio).
	// SV680N runs in 0.0001 deg units (3,600,000 per rev):
	// 8 units / 5 ms = 0.16 deg/s ≈ 0.027 rpm.
	ppHoldStandstillCounts = int32(8)
	// ppHoldStandstillSamples: consecutive quiet samples required (4 × 5 ms = 20 ms).
	ppHoldStandstillSamples = 4
	// ppHoldStandstillTimeout: worst case decel from 300 rpm (41,943,040 counts/s)
	// at 0x6084 = 100,000,000 counts/s² is ~420 ms. 1.5 s leaves margin for a
	// lower 0x6084 or a heavier load.
	ppHoldStandstillTimeout = 1500 * time.Millisecond
	// ppHoldOpModeTimeout: time allowed for 0x6061 to report the new mode.
	ppHoldOpModeTimeout = 100 * time.Millisecond
	// ppHoldRelatchAckTimeout: time allowed for the PP set-point acknowledge (bit12).
	ppHoldRelatchAckTimeout = 200 * time.Millisecond
	// ppHoldInWindowCounts: largest |goal-actual| accepted as "already in
	// position" when bit10 never clears on a PP-hold driver. Must be >= the
	// drive's 0x6067 position window (same user units). SV680N YAML sets
	// 0x6067 = 8 units (0.0008 deg), so 14 units (0.0014 deg) is the most we
	// accept as "already in position" without the drive having to move.
	ppHoldInWindowCounts = int32(14)
	// ppHoldStandstillDone: PP-hold drivers (SV680N) only. If the axis is
	// within ppHoldInWindowCounts of the goal and has not moved more than
	// ppHoldQuietCounts for this long, the move is declared done even if
	// bit10 has not settled. Stops 10-15 unit moves hanging 5-11 s while the
	// rotor creeps the last few units against friction. The drive keeps
	// holding 607A = goal afterwards, so it still finishes the correction.
	ppHoldStandstillDone = 300 * time.Millisecond
	// ppHoldQuietCounts: position change (user units) treated as encoder
	// noise while checking standstill. 2 units = 0.0002 deg.
	ppHoldQuietCounts = int32(2)
)

// ppHoldWaitStandstill blocks until the axis position stops changing or the
// timeout expires. Uses 0x6064 deltas, so no 0x606C mapping is needed.
// Returns true when standstill was confirmed.
func ppHoldWaitStandstill(masterDevice *MasterDevice, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	last := masterDevice.PDOPos.Load()
	quiet := 0
	for time.Now().Before(deadline) {
		if !IsPDOActive() {
			return false
		}
		time.Sleep(ppHoldStandstillSample)
		now := masterDevice.PDOPos.Load()
		diff := now - last
		if diff < 0 {
			diff = -diff
		}
		last = now
		if diff <= ppHoldStandstillCounts {
			quiet++
			if quiet >= ppHoldStandstillSamples {
				return true
			}
		} else {
			quiet = 0
		}
	}
	return false
}

// ppHoldWaitOpMode waits until 0x6061 reports the wanted mode.
// Returns true immediately when 0x6061 is not mapped (nothing to verify).
func ppHoldWaitOpMode(masterDevice *MasterDevice, want int8, timeout time.Duration) bool {
	if !masterDevice.PdoOpModeDisplayReady {
		return true
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if int8(masterDevice.PDOOpModeDisplay.Load()) == want {
			return true
		}
		if !IsPDOActive() {
			return false
		}
		time.Sleep(1 * time.Millisecond)
	}
	return int8(masterDevice.PDOOpModeDisplay.Load()) == want
}

// ppHoldRelatchAtActual latches 607A = current actual position as the Profile
// Position target (one bit4 set-point handshake in Mode 1), then returns the
// device to the cyclic standby branch (Mode 1, bit4 LOW).
//
// Why: after a jog the drive's last latched PP target is the pre-jog position.
// Re-latching the actual position guarantees the PP hold is exactly where the
// jog stopped and that no old target can ever be resumed.
//
// The move length is ~0, so this does NOT wait for bit10 — only for the
// set-point acknowledge — and never calls hasTargetReached (whose Phase 1.5
// would report "motor did not start" for a zero-length move).
func ppHoldRelatchAtActual(masterDevice *MasterDevice) error {
	actual := masterDevice.PDOPos.Load()
	if err := masterDevice.SetTargetPositionPDO(actual); err != nil {
		return err
	}
	masterDevice.EnablePosPDO(true)

	deadline := time.Now().Add(ppHoldRelatchAckTimeout)
	for masterDevice.ppSetpointPending.Load() && time.Now().Before(deadline) {
		if !IsPDOActive() {
			break
		}
		time.Sleep(1 * time.Millisecond)
	}
	acked := !masterDevice.ppSetpointPending.Load()
	masterDevice.EnablePosPDO(false) // back to cyclic standby: Mode 1, bit4 LOW

	if !acked {
		return fmt.Errorf("[PP-HOLD] re-latch set-point not acknowledged within %v (target=%d)",
			ppHoldRelatchAckTimeout, actual)
	}
	logger.Info("[PP-HOLD] re-latched 607A at actual position drive=", masterDevice.Name,
		" target=", actual)
	return nil
}

// StopJog stops jogging smoothly.
//
// Velocity is zeroed on the NEXT cyclic tick (single atomic store); the drive's
// own 0x6084 profile deceleration brings the motor to rest.
//
// PP-hold drivers (usesPPHold, e.g. InovanceSV680N):
//  1. Wait for real standstill (position deltas) instead of a fixed 250 ms.
//     At 300 rpm the 0x6084 ramp needs ~420 ms, so 250 ms switched modes while
//     the motor was still turning.
//  2. Leave jog → cyclic standby in Mode 1, confirm 0x6061 == 1.
//  3. Re-latch 607A = actual so the PP hold is exactly where the jog stopped.
//     If standstill is not reached in time, stay in Mode 3 vel=0
//     (standbyVelHold) — never switch to PP while the motor is moving.
//
// All other drivers (A6, Delta, ...): unchanged — fixed 250 ms then standby.
//
// WHY NO SOFTWARE DECEL RAMP:
//
//	A stepped ramp at 10 ms intervals produces 100 Hz torque modulation that is
//	directly audible as a beep throughout decel. The drive's internal decel
//	ramp is smooth, silent, and hardware-timed.
func StopJog(masterDevice *MasterDevice) error {
	logger.Trace("stop jog, driver: ", masterDevice.Name)

	if !IsPDOActive() || !masterDevice.PdoJogReady {
		logger.Warn("[PDO] StopJog: PDO not active — cannot stop jog safely for driver:", masterDevice.Name)
		return fmt.Errorf("StopJog: PDO not active — jog control is PDO-only in this build")
	}

	// Zero the velocity: the cyclic task writes 0x60FF = 0 on its next 1 ms tick.
	masterDevice.desiredTargetVelocity.Store(0)

	if usesPPHold(masterDevice) {
		stopStart := time.Now()
		stopped := ppHoldWaitStandstill(masterDevice, ppHoldStandstillTimeout)
		if !stopped {
			// Motor still moving (or PDO stopped). Stay in velocity hold —
			// Mode 3 vel=0 — so the drive keeps decelerating on its ramp.
			// The next jog / position command clears standbyVelHold.
			masterDevice.standbyVelHold.Store(true)
			masterDevice.EnableJogPDO(false)
			logger.Warn("[PDO] StopJog: standstill not confirmed within ", ppHoldStandstillTimeout,
				" — holding in Mode 3 vel=0 (no PP re-latch) for driver:", masterDevice.Name,
				" pos=", masterDevice.PDOPos.Load())
		} else {
			// Standstill confirmed: cut jog → cyclic standby writes Mode 1.
			masterDevice.EnableJogPDO(false)
			if !ppHoldWaitOpMode(masterDevice, 1, ppHoldOpModeTimeout) {
				logger.Warn("[PP-HOLD] 0x6061 did not report Mode 1 within ", ppHoldOpModeTimeout,
					" (active=", masterDevice.PDOOpModeDisplay.Load(), ") drive=", masterDevice.Name)
			}
			if err := ppHoldRelatchAtActual(masterDevice); err != nil {
				// Not fatal: the drive is in Mode 1 standby holding its entry
				// position. Log it so the bench test can catch it.
				logger.Warn(err)
			}
			logger.Info("[PDO] StopJog: stopped in ", time.Since(stopStart).Round(time.Millisecond),
				" — PP hold (Mode 1) at pos=", masterDevice.PDOPos.Load(),
				" activeMode=", masterDevice.PDOOpModeDisplay.Load(),
				" driver:", masterDevice.Name)
		}
	} else {
		// Legacy path (A6, Delta, Nidec, Veichi): unchanged.
		// 250 ms gives the drive's own 0x6084 decel ramp time before the
		// cyclic standby branch takes over.
		time.Sleep(250 * time.Millisecond)
		masterDevice.EnableJogPDO(false)
		logger.Info("[PDO] StopJog: velocity zeroed, standby restored for driver:", masterDevice.Name)
	}

	notifyDriverStatus("motor_running", "false", masterDevice)
	envSettings := settings.GetDriverSettings(masterDevice.Name)
	_, clampErr := hasClamped(masterDevice, envSettings)
	if clampErr != nil {
		logger.Error(clampErr)
		statusnotifier.Alarm(clampErr.Error())
		return clampErr
	}
	return nil
}

// hasTargetReached waits for CiA-402 PP completion: sw bit10=1 (Target Reached),
// stable for stableWindow. Uses statusword bits — not position error — because
// the drive runs its own ramp.
//
// HAL change: the per-drive completion condition (A6 requires bit12=0, Delta does
// not) is now delegated to masterDevice.Driver.IsTargetReached(sw). The old
// masterDevice.Device.DriveType string check has been removed.
func hasTargetReached(masterDevice *MasterDevice) error {
	// --- PDO path: Profile Position statusword check ---
	if masterDevice.PdoPosReady && IsPDOActive() {
		const (
			bitTargetReached = uint16(1 << 10) // SW bit10: motion complete, motor at target
			bitFault         = uint16(1 << 3)  // SW bit3:  drive fault
			moveTimeout      = 30 * time.Second

			// stableWindow: bit10 must stay HIGH continuously this long.
			// Time-based so single-sample noise glitches cause no latency penalty.
			stableWindow = 10 * time.Millisecond
		)

		logger.Trace("[PDO-PP] waiting for Target Reached. goal=",
			masterDevice.desiredTargetPosition.Load())

		// ----------------------------------------------------------
		// PHASE 1 — Wait for CiA-402 handshake completion
		// The cyclic task holds CW bit 4 HIGH until the drive replies
		// with SW bit 12 HIGH. Once acknowledged, ppSetpointPending
		// becomes false, and we can safely monitor for completion.
		// ----------------------------------------------------------
		handshakeDeadline := time.Now().Add(2 * time.Second)
		for masterDevice.ppSetpointPending.Load() {
			sw := uint16(masterDevice.PDOStatus.Load() & 0xFFFF)

			if sw&bitFault != 0 {
				return fmt.Errorf("[PDO-PP Phase1] drive fault during handshake: sw=0x%04X err=0x%04X",
					sw, uint16(masterDevice.PDOErr.Load()&0xFFFF))
			}
			if !IsPDOActive() {
				return fmt.Errorf("[PDO-PP Phase1] aborted: PDO cyclic stopped (system reset)")
			}
			if time.Now().After(handshakeDeadline) {
				return fmt.Errorf("[PDO-PP Phase1] handshake timeout: drive did not acknowledge set-point (bit 12)")
			}
			time.Sleep(1 * time.Millisecond)
		}

		logger.Debug("[PDO-PP] drive acknowledged set-point — waiting for bit10 to clear. goal=",
			masterDevice.desiredTargetPosition.Load(), " actual=", masterDevice.PDOPos.Load())

		// PHASE 1.5 — Wait for bit10 to go LOW (confirms new move started).
		//
		// CRITICAL: If bit10 never clears, it means the drive is still reporting
		// "Target Reached" from the PREVIOUS move. The motor physically never
		// started. We MUST NOT fall through to Phase 2 in this case — Phase 2
		// would immediately see bit10=1 (stale) and declare false "target reached",
		// causing FINISH BLOCKED and hanging the executor.
		//
		// If bit10 does not clear within the deadline, return a recoverable error
		// so the caller (doRotate/moveMotorToDegree) can retry the move.
		bit10Cleared := false
		bit10ClearDeadline := time.Now().Add(500 * time.Millisecond)
		for time.Now().Before(bit10ClearDeadline) {
			sw := uint16(masterDevice.PDOStatus.Load() & 0xFFFF)
			if sw&bitFault != 0 {
				return fmt.Errorf("[PDO-PP Phase1.5] drive fault: sw=0x%04X", sw)
			}
			if !IsPDOActive() {
				return fmt.Errorf("[PDO-PP Phase1.5] aborted: PDO cyclic stopped")
			}
			if sw&bitTargetReached == 0 {
				logger.Debug("[PDO-PP] bit10 cleared — motor moving...")
				bit10Cleared = true
				break
			}
			time.Sleep(1 * time.Millisecond)
		}

		// Motor did not start — drive ACKed the set-point but bit10 never went LOW.
		// This is a recoverable condition: the drive ignored the set-point (possibly
		// because it was in a transient "already at target" state). Return a specific
		// error so the caller can re-trigger the set-point and retry.
		if !bit10Cleared {
			goal := masterDevice.desiredTargetPosition.Load()
			actual := masterDevice.PDOPos.Load()
			residual := int64(goal) - int64(actual)
			if residual < 0 {
				residual = -residual
			}
			// PP-hold drivers (SV680N) sit in Mode 1 with bit10=1 while idle.
			// A move shorter than the drive's position window (0x6067) never
			// clears bit10 because the drive is already "at target". That is
			// not a missed set-point — the axis is in position. Accept it,
			// but log the residual so a too-wide 0x6067 stays visible.
			if usesPPHold(masterDevice) && residual <= int64(ppHoldInWindowCounts) {
				logger.Warn("[PDO-PP Phase1.5] bit10 stayed HIGH — already inside drive position window. ",
					"goal=", goal, " actual=", actual, " residual=", residual,
					" (tighten 0x6067 so the drive corrects this) drive=", masterDevice.Name)
				return nil
			}
			return fmt.Errorf("[PDO-PP Phase1.5] motor did not start — drive may have missed set-point. "+
				"goal=%d actual=%d diff=%d. Retrying recommended.",
				goal, actual, goal-actual)
		}

		// ----------------------------------------------------------
		// PHASE 2 — Wait for bit10 stable for stableWindow
		//
		// Use a time-based stability gate: track the wall-clock time
		// when bit10 first went HIGH. If it stays HIGH for stableWindow
		// without interruption, the move is complete.
		//
		// A glitch (bit10 drops to 0 for 1-2ms due to noise) resets
		// stableFrom but does NOT add fixed 50ms penalty — recovery is
		// instant on the next 1ms poll when bit10 returns to 1.
		// ----------------------------------------------------------
		timeout := time.Now().Add(moveTimeout)
		var stableFrom time.Time
		stableStarted := false

		// Fix B: standstill-in-window exit for PP-hold drivers.
		ppHold := usesPPHold(masterDevice)
		quietRef := masterDevice.PDOPos.Load()
		quietFrom := time.Now()

		for {
			sw := uint16(masterDevice.PDOStatus.Load() & 0xFFFF)

			// Abort immediately on drive fault (bit3)
			if sw&bitFault != 0 {
				return fmt.Errorf("[PDO-PP] drive fault during move: sw=0x%04X errCode=0x%04X",
					sw, uint16(masterDevice.PDOErr.Load()&0xFFFF))
			}

			// Exit immediately if PDO was shut down
			if !IsPDOActive() {
				return fmt.Errorf("[PDO-PP] aborted: PDO cyclic stopped (system reset)")
			}

			// Exit immediately if emergency cancelled this move.
			if masterDevice.posMoveAborted.Load() {
				masterDevice.posMoveAborted.Store(false)
				return fmt.Errorf("[PDO-PP] aborted: position move cancelled by emergency")
			}

			// HAL: IsTargetReached is drive-specific.
			//   A6Minas     → bit10=1 AND bit12=0 (strict Set-Point Ack handshake)
			//   DeltaASDA2E → bit10=1 only         (bit12 caused 30s hang on Delta)
			// Uses masterDevice.Driver (per-device) instead of GetMotorDriver() (global)
			// so that in a mixed-axis setup each axis uses its own completion condition.
			isTargetReached := masterDevice.Driver.IsTargetReached(sw)

			if isTargetReached {
				if !stableStarted {
					stableFrom = time.Now()
					stableStarted = true
				} else if time.Since(stableFrom) >= stableWindow {
					goalPulses := masterDevice.desiredTargetPosition.Load()
					actualPulses := masterDevice.PDOPos.Load()
					logger.Trace("[PDO-PP] target reached.",
						"sw=", fmt.Sprintf("0x%04X", sw),
						"goal=", goalPulses,
						"pos=", actualPulses)
					return nil
				}
			} else {
				stableStarted = false // Reset stability timer if it fluctuates
			}

			// Fix B: if the axis is already inside ppHoldInWindowCounts and has
			// stopped, do not wait for bit10. The motor can sit 5-8 units short
			// for several seconds before friction lets it creep into 0x6067.
			if ppHold {
				pos := masterDevice.PDOPos.Load()
				moved := pos - quietRef
				if moved < 0 {
					moved = -moved
				}
				if moved > ppHoldQuietCounts {
					quietRef = pos
					quietFrom = time.Now()
				} else if time.Since(quietFrom) >= ppHoldStandstillDone {
					goal := masterDevice.desiredTargetPosition.Load()
					residual := int64(goal) - int64(pos)
					if residual < 0 {
						residual = -residual
					}
					if residual <= int64(ppHoldInWindowCounts) {
						logger.Warn("[PDO-PP STANDSTILL] bit10 not settled, axis stopped inside window — done. ",
							"goal=", goal, " actual=", pos, " residual=", residual,
							" quiet=", time.Since(quietFrom).Round(time.Millisecond),
							" sw=", fmt.Sprintf("0x%04X", sw), " drive=", masterDevice.Name)
						return nil
					}
				}
			}

			if time.Now().After(timeout) {
				return fmt.Errorf("[PDO-PP] timeout waiting for Target Reached: sw=0x%04X goal=%d pos=%d",
					sw, masterDevice.desiredTargetPosition.Load(), masterDevice.PDOPos.Load())
			}
			time.Sleep(1 * time.Millisecond)
		}
	}

	return fmt.Errorf("hasTargetReached: PDO not active or PdoPosReady=false")
}

// freeRotate will not wait for ECS or will not send fin signal.
func freeRotate(masterDevice *MasterDevice, valueinDegree float64) error {
	logger.Trace("freeRotate to postion, driver", masterDevice.Name, "degree:", valueinDegree)
	err := doRotate(masterDevice, valueinDegree)
	if err != nil {
		return err
	}
	doneDriverAction()
	logger.Trace("freeRotate to postion completed, driver", masterDevice.Name)

	return nil
}

// relBaseMaxDrift: largest |607A - actual| (user units) for which a relative
// move on a PP-hold driver is built from the last commanded target instead
// of the live position. Normal settled error is <= 22 units (0.0022 deg);
// 140 units = 0.014 deg. Anything larger
// means the axis was moved by something else (fault, servo-off, hand) and the
// live position is the only safe base.
const relBaseMaxDrift = int64(140)

// doRotate moves the axis by a relative angle.
//
// Legacy drivers: goal = live position + delta (unchanged behaviour).
//
// PP-hold drivers (SV680N): goal = last commanded target (607A) + delta, and
// the sub-unit fraction is carried to the next step. Reasons:
//   - The axis stops a few units short and then creeps past the target.
//     Building the goal from the live position made each click follow the
//     creep (99 commanded -> 135 moved in the 22-Oct test).
//   - Any sub-unit fraction is carried to the next step, so repeated steps
//     always add up exactly. (With 0.0001 deg units, 0.001 deg = 10 units
//     and there is no fraction; the carry only matters for odd sizes.)
func doRotate(masterDevice *MasterDevice, valueinDegree float64) error {
	deg := helper.RoundFloatTo3(valueinDegree)
	actual := int64(masterDevice.PDOPos.Load())

	var delta, goal64 int64
	if usesPPHold(masterDevice) {
		unitsPerDeg := float64(getPulsesFromDegree(masterDevice, 1000)) / 1000.0
		rem := math.Float64frombits(masterDevice.relRemainder.Load())
		exact := deg*unitsPerDeg + rem
		delta = int64(math.Round(exact))

		base := int64(masterDevice.desiredTargetPosition.Load())
		drift := base - actual
		if drift < 0 {
			drift = -drift
		}
		if drift > relBaseMaxDrift {
			logger.Warn("[PDO-PP REL] last target ", base, " is ", drift,
				" units from actual ", actual, " — using actual as base drive=", masterDevice.Name)
			base = actual
			exact = deg * unitsPerDeg
			delta = int64(math.Round(exact))
		}
		masterDevice.relRemainder.Store(math.Float64bits(exact - float64(delta)))
		goal64 = base + delta
		logger.Info("[PDO-PP REL] deg=", deg, " exactUnits=", exact, " delta=", delta,
			" base(607A)=", base, " actual=", actual, " goal=", goal64)
	} else {
		delta = getPulsesFromDegree(masterDevice, deg)
		goal64 = actual + delta
		logger.Info("[PDO-PP REL] delta=", delta, " actual=", actual, " goal=", goal64)
	}

	if goal64 < int64(-1<<31) || goal64 > int64(1<<31-1) {
		return fmt.Errorf("relative target overflow: actual=%d delta=%d goal=%d", actual, delta, goal64)
	}
	return runPositionGoal(masterDevice, int32(goal64), "REL")
}

// doRotateToAbsoluteGoal sends a precomputed, home-referenced encoder target.
// Unlike doRotate, it never reconstructs the target from the live position.
func doRotateToAbsoluteGoal(masterDevice *MasterDevice, goal int32) error {
	return runPositionGoal(masterDevice, goal, "ABS")
}

// runPositionGoal contains the common PDO motion, settle and clamp sequence.
// Both relative auxiliary moves and fixed absolute indexing targets use the
// same drive handshake and safety behavior.
func runPositionGoal(masterDevice *MasterDevice, goal int32, targetKind string) error {

	if !(IsPDOActive() && masterDevice.PdoPosReady) {
		return fmt.Errorf("PDO position mode not ready — SDO fallback removed")
	}

	logger.Info("[PDO] Starting rotation in PP mode targetKind=", targetKind,
		" goal=", goal)

	// Do not call FastPowerOn here. Once PDO cyclic control is active, the
	// CiA-402 cyclic task owns servo state. FastPowerOn calls pdoStopMotion(),
	// which disables the active PDO motion modes and can introduce a transient
	// standby cycle immediately before the Profile Position command.
	envSettings := settings.GetDriverSettings(masterDevice.Name)
	if _, declampErr := hasDeclamped(masterDevice, envSettings); declampErr != nil {
		logger.Error("[PDO-PP] doRotate: declamp failed, aborting move:", declampErr)
		statusnotifier.Alarm(declampErr.Error())
		return declampErr
	}

	notifyDriverStatus("motor_running", "true", masterDevice)

	// Store the new target before enabling PP output. Enabling first can let
	// the 1 ms cyclic task latch the previous target during this small window.
	if err := masterDevice.SetTargetPositionPDO(goal); err != nil {
		return err
	}
	masterDevice.EnablePosPDO(true)

	if err := hasTargetReached(masterDevice); err != nil {
		masterDevice.EnablePosPDO(false)
		// ADD THIS LINE: Clear the running flag so the user can press Reset!
		notifyDriverStatus("motor_running", "false", masterDevice)
		return err
	}

	time.Sleep(positionSettleTime)
	settledPosition := masterDevice.PDOPos.Load()
	settledResidual := int64(goal) - int64(settledPosition)
	if settledResidual < 0 {
		settledResidual = -settledResidual
	}
	logger.Info("[PDO-PP SETTLED] drive=", masterDevice.Name,
		" targetKind=", targetKind,
		" goal=", goal,
		" actual=", settledPosition,
		" residualPulses=", settledResidual,
		" settleTime=", positionSettleTime)

	// Match Mani build: once the drive has reported Target Reached and the
	// settled position has been logged, disable PP output so the cyclic standby
	// branch takes over on the next PDO tick.
	//
	// For PP-hold drivers (InovanceSV680N, and the A6) standby is Mode 1 with
	// bit4 LOW: the drive keeps holding THIS goal with its position loop. No
	// mode change happens here. For Mode-3 standby drivers (Delta) the drive
	// switches to velocity hold as before.
	masterDevice.EnablePosPDO(false)
	logger.Info("[POST-REACH] phase=PP_DISABLED drive=", masterDevice.Name,
		" goal=", goal, " actual=", masterDevice.PDOPos.Load(),
		" standbyMode=", masterDevice.standbyOpMode())

	notifyDriverStatus("motor_running", "false", masterDevice)

	// FIN is allowed only after the clamp sequence has completed. Do not hide
	// clamp errors: an open/unknown clamp state must fail the movement.
	if _, clampErr := hasClamped(masterDevice, envSettings); clampErr != nil {
		logger.Error("[BRAKE-SEQ] phase=CLAMP_FAILED drive=", masterDevice.Name,
			" error=", clampErr)
		statusnotifier.Alarm(clampErr.Error())
		return clampErr
	}

	logger.Info("[PDO-PP] rotation completed successfully targetKind=", targetKind,
		" goal=", goal, " final=", masterDevice.PDOPos.Load())

	return nil
}
