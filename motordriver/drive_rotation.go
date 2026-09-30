package motordriver



import (

    helper "EtherCAT/helper"

    logger "EtherCAT/logger"

    "EtherCAT/motordriver/statusnotifier"

    settings "EtherCAT/settings"

    "errors"

    "fmt"

    "time"

)



const (

    // Used by zero_reference.go to skip a redundant move near machine zero.

    positionPulseTolerance = int32(10)

    // Give the drive and mechanics time to settle before PP is disabled,

    // clamping is evaluated and FIN allows the measuring system to capture.

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



// ManualJog start jogging. Direction param can be 1 or -1.

// The optional jogFeedOverride keeps compatibility with newer frontend callers

// that pass the live jog speed as the third argument.

func ManualJog(masterDevice *MasterDevice, direction int, jogFeedOverride ...float64) error {

    driverStatus := getCurrentDriverStatus(masterDevice.Device.Name)

    if driverStatus.potNotExceeded {

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



    // BACKLASH FIX: Notify the direction state machine with the actual jog direction

    // instead of bypassing it with set_backlash=0.

    //

    // PREVIOUS BUG: "set_backlash=0" wrote directly to driverStatus.backlash but

    // did NOT update driverStatus.direction. After jog the direction field still

    // held whatever the last programmatic move had set. When the next programmatic

    // move came in, setDirection() compared against this stale direction — often

    // seeing no change — and applied zero backlash even though the motor had just

    // jogged in the opposite direction, leaving gear play untaken.

    //

    // FIX: Send rotation_direction with the real jog direction. The existing state

    // machine in driver_status_keeper.go handles all four cases correctly:

    //   CW  → CCW : backlash = backlashInSetting  (direction reversal, take up play)

    //   CCW → CCW : backlash = backlashInSetting  (continuing CCW, always compensate)

    //   CW  → CW  : backlash = 0                  (same direction, no play)

    //   CCW → CW  : backlash = 0                  (reversing back, drive takes its own)

    //

    // After jog ends, driverStatus.direction holds the actual last direction of

    // travel. The next programmatic setDirection() call will compare correctly

    // and apply the right compensation on the first move after any jog.

    notifyDriverStatusWithWait("rotation_direction", fmt.Sprintf("%d", direction), masterDevice)

    if !(IsPDOActive() && masterDevice.PdoReady) {

        if err := FastPowerOn(masterDevice); err != nil {

            logger.Error(err)

            statusnotifier.Alarm(err.Error())

            return err

        }

    } else {

        logger.Info("[PDO] FastPowerOn skipped; PDO cyclic is active")

    }



    _, declampErr := hasDeclamped(masterDevice, envSettings)

    if declampErr != nil {

        logger.Error(declampErr)

        statusnotifier.Alarm(declampErr.Error())

        return declampErr

    }



    notifyDriverStatus("motor_running", "true", masterDevice)



    // Calculate target velocity (RPM)

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

        "(rpm-const=", masterDevice.Device.RPMConst,

        "JogFeed=", jogFeed,

        "source=", jogFeedSource, ")")



    _ = masterDevice.SetJogPDOSetpoints(0x000F, 3, int32(rpm))

    masterDevice.EnableJogPDO(true)



    return nil

}



// StopJog stop jogging smoothly

func StopJog(masterDevice *MasterDevice) error {

    logger.Trace("stop jog, driver: ", masterDevice.Name)



    if !IsPDOActive() || !masterDevice.PdoJogReady {

        logger.Warn("[PDO] StopJog: PDO not active — cannot stop jog safely for driver:", masterDevice.Name)

        return fmt.Errorf("StopJog: PDO not active — jog control is PDO-only in this build")

    }



    // Zero the velocity in a single atomic store, matching the original SDO build's

    // procedure: one write to 0x60FF = 0. The drive's 0x6084 profile deceleration

    // (set at startup in configureDriver) brings it to a smooth stop internally.

    //

    // WHY NO SOFTWARE DECEL RAMP:

    //   Same reason as ManualJog — a stepped ramp at 10ms intervals produces

    //   100 Hz torque modulation that is directly audible as a beep throughout decel.

    //   The drive's internal decel ramp is smooth, silent, and hardware-timed.

    //

    // 250ms wait: matches the middle build's proven approach. Gives the drive's

    // own 0x6084 decel ramp time to bring the motor fully to rest before

    // the cyclic standby branch takes over.

    masterDevice.desiredTargetVelocity.Store(0)

    time.Sleep(250 * time.Millisecond)



    // Cut jog mode — cyclic standby (Mode 3 vel=0) takes over immediately.

    // No hardware mode switch: drive stays in Mode 3 throughout.

    masterDevice.EnableJogPDO(false)

    logger.Info("[PDO] StopJog: velocity zeroed, standby restored for driver:", masterDevice.Name)



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



        swAfterAck := uint16(masterDevice.PDOStatus.Load() & 0xFFFF)

        logger.Info("[PP-ACK] drive=", masterDevice.Name,

            " sw=", fmt.Sprintf("0x%04X", swAfterAck),

            " pds=", fmt.Sprintf("0x%02X", swAfterAck&0x006F),

            " bit10=", (swAfterAck&(1<<10)) != 0,

            " bit12=", (swAfterAck&(1<<12)) != 0,

            " goal=", masterDevice.desiredTargetPosition.Load(),

            " actual=", masterDevice.PDOPos.Load())



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



        // Diagnostic only: periodically capture the exact CiA-402 state while

        // waiting for completion. This does NOT change motion behaviour.

        lastPPDiag := time.Time{}



        for {

            sw := uint16(masterDevice.PDOStatus.Load() & 0xFFFF)



            if lastPPDiag.IsZero() || time.Since(lastPPDiag) >= 250*time.Millisecond {

                goal := masterDevice.desiredTargetPosition.Load()

                actual := masterDevice.PDOPos.Load()

                diff := int64(goal) - int64(actual)

                if diff < 0 {

                    diff = -diff

                }



                bit10 := (sw & (1 << 10)) != 0

                bit12 := (sw & (1 << 12)) != 0

                fault := (sw & (1 << 3)) != 0

                pds := sw & 0x006F



                logger.Info("[PP-TRANSITION] drive=", masterDevice.Name,

                    " sw=", fmt.Sprintf("0x%04X", sw),

                    " pds=", fmt.Sprintf("0x%02X", pds),

                    " bit10=", bit10,

                    " bit12=", bit12,

                    " fault=", fault,

                    " ppPending=", masterDevice.ppSetpointPending.Load(),

                    " goal=", goal,

                    " actual=", actual,

                    " diff=", diff)



                lastPPDiag = time.Now()

            }



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



// doRotate function which power on and rotate to a degree, it waits for declamping and clamping.

func doRotate(masterDevice *MasterDevice, valueinDegree float64) error {

    delta := getPulsesFromDegree(masterDevice, helper.RoundFloatTo3(valueinDegree))

    actual := int64(masterDevice.PDOPos.Load())

    goal64 := actual + delta

    if goal64 < int64(-1<<31) || goal64 > int64(1<<31-1) {

        return fmt.Errorf("relative target overflow: actual=%d delta=%d goal=%d", actual, delta, goal64)

    }



    logger.Info("[PDO-PP REL] delta=", delta, " actual=", actual, " goal=", goal64)

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

        // Clear the running flag so the user can press Reset.

        notifyDriverStatus("motor_running", "false", masterDevice)

        return err

    }



    // DIAGNOSTIC: capture the raw 0x6064 position while PP is still enabled.

    // This proves whether the drive itself is holding the exact target before

    // we hand control back to the standby branch. No motion behaviour is changed.



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



    // Disable PP mode — cyclic standby (Mode 3 vel=0) takes over on the next tick.

    // IMPORTANT: we now log the raw position immediately after this handoff.

    // If the position drifts only after EnablePosPDO(false), the standby handoff

    // is the root cause of the post-move displacement.

    masterDevice.EnablePosPDO(false)

    logger.Info("[POST-REACH] phase=PP_DISABLED drive=", masterDevice.Name,

        " goal=", goal, " actual=", masterDevice.PDOPos.Load())



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



// logPostReachWindow is a temporary diagnostic helper.

// It samples the raw PDO actual position (0x6064) and statusword after the drive

// reports Target Reached. This lets us see exactly whether position changes while

// PP is still active or only after control is handed to standby.

func logPostReachWindow(masterDevice *MasterDevice, goal int32, targetKind, phase string, samples int, interval time.Duration) {

    for i := 0; i < samples; i++ {

        sw := uint16(masterDevice.PDOStatus.Load() & 0xFFFF)

        actual := masterDevice.PDOPos.Load()

        diff := int64(goal) - int64(actual)

        if diff < 0 {

            diff = -diff

        }



        logger.Info("[POST-REACH] phase=", phase,

            " sample=", i,

            " drive=", masterDevice.Name,

            " targetKind=", targetKind,

            " sw=", fmt.Sprintf("0x%04X", sw),

            " pds=", fmt.Sprintf("0x%02X", sw&0x006F),

            " bit10=", (sw&(1<<10)) != 0,

            " bit12=", (sw&(1<<12)) != 0,

            " posEnabled=", masterDevice.IsPosEnabled(),

            " jogEnabled=", masterDevice.IsJogEnabled(),

            " goal=", goal,

            " actual=", actual,

            " diff=", diff)



        time.Sleep(interval)

    }

}
