package motordriver

/*
#cgo CFLAGS: -g -Wall -I/opt/etherlab/include -I${SRCDIR}/..
#cgo LDFLAGS: -L${SRCDIR}/.. -L/opt/etherlab/lib/ -lethercatinterface -lethercat
#include "ecrt.h"
#include "ethercatinterface.h"
*/
import "C"

import (
	"EtherCAT/channels"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	ethercatDevice "EtherCAT/ethercatdevicedatatypes"
	logger "EtherCAT/logger"
	"EtherCAT/motordriver/statusnotifier"
	"EtherCAT/settings"
)

type SD710 struct {
	device   *MasterDevice
	finPulse atomic.Bool
	// 0 = available, 1 = latched, 3 = reset recovery (motion still blocked).
	safetyState     atomic.Uint32
	safetyNotify    atomic.Bool
	resetArmed      atomic.Bool
	ioStopMu        sync.Mutex
	ioStops         []chan struct{}
	ioWG            sync.WaitGroup
	safetyTripCause atomic.Value // string
	ppCompletionLastLog atomic.Int64
}

const (
	// BENCH TEST ONLY: X2/X3 are internal VCSDSoft inputs. Reset may clear
	// those software commands. Do not use this profile with a physical E-stop.
	sd710VCSDSoftTestMode = true

	sd710TargetReachedBit = uint16(1 << 10)
	sd710SetPointAckBit   = uint16(1 << 12)

	// LIVE-VERIFIED Rev21 0x60FD physical X-terminal bitmap.
	// X1=ECS, X2=emergency, X3=reset, X4=CL, X5=DCL.
	// VCSDSoft test profile: X2 ON means emergency active.
	sd710X1ECS       = uint32(1 << 0)
	sd710X2Emergency = uint32(1 << 1)
	sd710X3Reset     = uint32(1 << 2)
	sd710X4CL        = uint32(1 << 3)
	sd710X5DCL       = uint32(1 << 4)

	sd710PositionTolerance = int32(16)
	sd710SolenoidY1Mask    = uint16(1 << 0)
	sd710FinishY2Mask      = uint16(1 << 1)

	// UI is slower than the 1 ms motion PDO by design.
	sd710UIRefresh   = 50 * time.Millisecond
	sd710UIHeartbeat = 500 * time.Millisecond

	// SD710-only runtime safety monitors. These are software guards, not a
	// substitute for the drive-side EtherCAT SyncManager watchdog/STO.
	// Two consecutive bad samples at 5 ms = about 10 ms debounce.
	sd710SafetyPoll       = 5 * time.Millisecond
	sd710SafetyBadSamples = 2
	sd710LinkPoll         = 20 * time.Millisecond
)

func (d *SD710) hasTargetReached(masterDevice *MasterDevice, action int, immediate int, operation ethercatDevice.Operation) error {
	if masterDevice == nil {
		return errors.New("SD710 target reached: nil device")
	}
	if err := d.sd710SafetyLatchedError(); err != nil {
		return err
	}

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		sw := uint16(masterDevice.PDOStatus.Load() & 0xFFFF)
		if sw&0x0008 != 0 {
			return fmt.Errorf("SD710 fault while waiting for target reached: sw=0x%04X error=0x%04X", sw, uint16(masterDevice.PDOErr.Load()&0xFFFF))
		}
		if d.IsTargetReached(sw) {
			return nil
		}
		time.Sleep(2 * time.Millisecond)
	}

	return fmt.Errorf("SD710 target-reached timeout: sw=0x%04X position=%d", uint16(masterDevice.PDOStatus.Load()&0xFFFF), masterDevice.PDOPos.Load())
}

func (d *SD710) potNotEnabled(masterDevice *MasterDevice) (bool, error) {
	if masterDevice == nil {
		return false, errors.New("SD710 potNotEnabled: nil device")
	}
	return false, nil // X2/X3 are not overtravel inputs on this machine.
}

// sd710SafetyTrip immediately removes every software motion request and latches
// this SD710 against new motion until a RESET/forced-servo-cycle is observed.
//
// IMPORTANT: this can stop the drive only while EtherCAT communication still
// exists. If the Ethernet cable is physically removed, the drive-side SM2
// watchdog must stop the servo because no new controlword can cross a broken link.
// Called by the PDO task too: no mailbox operations, logging or channel waits.
func (d *SD710) sd710SafetyTrip(dev *MasterDevice, cause string, alarmCode int) {
	if dev == nil {
		return
	}
	prior := d.safetyState.Swap(1)
	d.clearSD710Motion(dev)
	dev.desiredControlWord.Store(0x0006)
	if prior != 1 {
		d.safetyTripCause.Store(cause)
		d.safetyNotify.Store(true)
	}
}

func (d *SD710) clearSD710Motion(dev *MasterDevice) {
	dev.desiredTargetVelocity.Store(0)
	dev.EnableJogPDO(false)
	dev.EnablePosPDO(false)
	dev.ppSetpointPending.Store(false)
	dev.posMoveAborted.Store(true)
	dev.desiredTargetPosition.Store(dev.PDOPos.Load())
}

func sd710EmergencyReleased(dev *MasterDevice) bool {
	if dev == nil || !dev.PdoDIReady {
		return false
	}
	active := uint32(dev.PDODI.Load())&sd710X2Emergency != 0
	if sd710VCSDSoftTestMode {
		return !active // VCSDSoft: ON = emergency, OFF = inactive.
	}
	return active // Original physical raw-input profile: OFF = pressed.
}

// Called only by the system-reset worker, never by the 1 ms PDO loop.
// The software latch stays set while the native emergency input is cleared.
func (d *SD710) sd710PrepareReset(dev *MasterDevice) error {
	if dev == nil || !dev.PdoDIReady || !IsPDOActive() {
		return errors.New("SD710 reset refused: live PDO input is unavailable")
	}
	if !sd710VCSDSoftTestMode {
		if !sd710EmergencyReleased(dev) {
			return errors.New("SD710 reset refused: release the physical emergency first")
		}
		return nil
	}

	d.sd710SafetyTrip(dev, "VCSDSoft test reset in progress", 0)
	var before, after uint16
	err := sd710WithSDOExclusive(dev, func() error {
		read := func(subindex int, name string) (uint16, error) {
			return sd710UploadU16WithRetry(dev, 0x2006, subindex, name)
		}
		// Refuse auto-clear if either terminal has been switched to hardware.
		for _, input := range []struct {
			subindex int
			expected uint16
			name     string
		}{{0x03, 0x1019, "Pn602/X2"}, {0x04, 0x1004, "Pn603/X3"}} {
			value, err := read(input.subindex, input.name)
			if err != nil {
				return fmt.Errorf("%s readback failed: %w", input.name, err)
			}
			if value != input.expected {
				return fmt.Errorf("%s=0x%04X; test reset requires 0x%04X", input.name, value, input.expected)
			}
		}
		var err error
		before, err = read(0x31, "Pn630 software inputs before reset")
		if err != nil {
			return err
		}
		// Clear only X2 and X3; preserve ECS, CL, DCL and every other bit.
		after = before &^ uint16(sd710X2Emergency|sd710X3Reset)
		if after != before {
			step := ethercatDevice.Step{Name: "Pn630 clear VCSDSoft X2/X3",
				Address: 0x2006, SubIndex: 0x31, Value: fmt.Sprintf("%d", after),
				DataType: "U16", Action: "write"}
			if err := SDODownload(dev.Master, dev.Position, step); err != nil {
				return err
			}
		}
		value, err := read(0x31, "Pn630 software inputs after reset")
		if err != nil {
			return err
		}
		if value != after {
			return fmt.Errorf("Pn630 verification failed: expected 0x%04X got 0x%04X", after, value)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("SD710 software test reset failed: %w", err)
	}
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if !IsPDOActive() || !dev.PdoDIReady {
			return errors.New("SD710 test reset lost PDO input")
		}
		if uint32(dev.PDODI.Load())&(sd710X2Emergency|sd710X3Reset) == 0 {
			logger.Info(fmt.Sprintf("[SD710-TEST-RESET] drive=%s Pn630 0x%04X -> 0x%04X; X2/X3 OFF confirmed, motion remains latched until recovery completes", dev.Name, before, after))
			return nil
		}
		time.Sleep(time.Millisecond)
	}
	return errors.New("SD710 test reset failed: X2/X3 did not become OFF in PDO feedback")
}

// Invoked after every PDO input update. Inhibit has priority over all motion.
func (d *SD710) sd710CyclicInhibit(dev *MasterDevice) bool {
	if !sd710EmergencyReleased(dev) {
		d.sd710SafetyTrip(dev, "X2 external emergency pressed or input unavailable", 0)
	}
	state := d.safetyState.Load()
	if state != 0 {
		d.clearSD710Motion(dev)
	}
	// Recovery permits only the common servo/fault-reset state machine.
	// Jog/PP requests were cleared above, even during recovery.
	return state == 1
}

// Require an observed release before accepting a reset press, including startup.
func (d *SD710) sd710ResetEdge(di uint32) bool {
	if di&sd710X3Reset == 0 {
		d.resetArmed.Store(true)
		return false
	}
	return d.resetArmed.Swap(false)
}

func (d *SD710) sd710BeginReset(dev *MasterDevice) bool {
	if !sd710EmergencyReleased(dev) {
		return false
	}
	d.safetyState.Store(3)
	d.safetyNotify.Store(false)
	d.clearSD710Motion(dev)
	return true
}

func (d *SD710) sd710FinishReset(dev *MasterDevice, success bool) bool {
	if success && sd710EmergencyReleased(dev) &&
		d.safetyState.CompareAndSwap(3, 0) {
		return true
	}
	d.sd710SafetyTrip(dev, "Reset failed or emergency changed during recovery", 0)
	return false
}

func (d *SD710) sd710SafetyLatchedError() error {
	if d.safetyState.Load() == 0 {
		return nil
	}
	if v := d.safetyTripCause.Load(); v != nil {
		if cause, ok := v.(string); ok && cause != "" {
			return fmt.Errorf("SD710 motion inhibited until RESET: %s", cause)
		}
	}
	return errors.New("SD710 motion inhibited until RESET")
}

func (d *SD710) sd710MotionCommanded(masterDevice *MasterDevice) bool {
	if masterDevice == nil {
		return false
	}
	return masterDevice.IsJogEnabled() ||
		masterDevice.IsPosEnabled() ||
		masterDevice.desiredTargetVelocity.Load() != 0
}

// sd710SafetyMonitor is the software-side runtime interlock.
// The SOL output is the initiator. CL/DCL are responders.
// Motion is accepted only in the stable released state SOL=1, CL=0, DCL=1.
func (d *SD710) sd710SafetyMonitor(masterDevice *MasterDevice, stop <-chan struct{}) {
	if masterDevice == nil {
		return
	}

	ticker := time.NewTicker(sd710SafetyPoll)
	defer ticker.Stop()

	lastMotion := false
	lastLoggedDI := uint32(0xFFFFFFFF)
	lastClampMode := -1
	lastModeFeedbackLog := time.Time{}

	// Delay timers. The operator setting ClampDeclampTiming is used for BOTH
	// CL/DCL modes:
	//
	//   cl_dl=0: SOL is permanently ON and responders get the configured time
	//            to become CL=0,DCL=1.
	//
	//   cl_dl=1: common clamp/declamp sequencing calls readClampSignal /
	//            readDeclampSignal, which use the same configured timeout.
	//
	// During active motion, an unsafe CL/DCL state removes motion immediately
	// for safety, but the CL/DCL alarm/latch is raised only if the bad feedback
	// remains present for the configured CL/DL delay.
	var case1FeedbackBadSince time.Time
	var motionFeedbackBadSince time.Time
	var case2IdleBadSince time.Time

	logger.Info(fmt.Sprintf(
		"[SD710-SAFETY] monitor START drive=%s poll=%s; CL/DCL feedback faults use operator ClampDeclampTiming",
		masterDevice.Name, sd710SafetyPoll,
	))

	for {
		select {
		case <-stop:
			logger.Info(fmt.Sprintf("[SD710-SAFETY] monitor STOP drive=%s", masterDevice.Name))
			return
		case <-ticker.C:
		}

		if d.safetyNotify.Swap(false) {
			logger.Error("[SD710-SAFETY] ", d.sd710SafetyLatchedError())
			notifyDriverStatus("motor_running", "false", masterDevice)
			statusnotifier.Alarm(fmt.Sprint(d.sd710SafetyLatchedError()))
			go func() {
				channels.WriteCommandExecInput("stop_prog_exec", "")
				channels.WriteCommandExecInput("reset", "")
			}()
		}
		sw := uint16(masterDevice.PDOStatus.Load() & 0xFFFF)
		motion := d.sd710MotionCommanded(masterDevice)

		cfg := settings.GetDriverSettings(masterDevice.Name)
		clampMode := cfg.ClampDeclamp
		delayMs := cfg.ClampDeclampTiming
		if delayMs <= 0 {
			delayMs = 3000
		}
		delay := time.Duration(delayMs) * time.Millisecond

		if clampMode != 0 && clampMode != 1 {
			d.sd710SafetyTrip(
				masterDevice,
				fmt.Sprintf("invalid cl_dl=%d during runtime; only 0 or 1 supported", clampMode),
				0,
			)
			continue
		}

		if d.safetyState.Load() == 0 && clampMode != lastClampMode {
			logger.Info(fmt.Sprintf(
				"[SD710-MODE] drive=%s runtime mode transition old=%d new=%d motion=%t CLDL_DELAY=%dms",
				masterDevice.Name, lastClampMode, clampMode, motion, delayMs,
			))

			if lastClampMode != -1 && motion {
				d.sd710SafetyTrip(
					masterDevice,
					fmt.Sprintf("operator changed cl_dl from %d to %d while motion was active", lastClampMode, clampMode),
					0,
				)
				continue
			}

			if err := sd710ApplyClampModeOutput(masterDevice, clampMode, "runtime cl_dl mode change"); err != nil {
				d.sd710SafetyTrip(
					masterDevice,
					fmt.Sprintf("failed to apply cl_dl=%d output policy: %v", clampMode, err),
					0,
				)
				continue
			}

			lastClampMode = clampMode
			lastModeFeedbackLog = time.Time{}
			case1FeedbackBadSince = time.Time{}
			motionFeedbackBadSince = time.Time{}
			case2IdleBadSince = time.Time{}
		}

		// Drive fault is NOT delayed by CL/DL timing.
		if sw&0x0008 != 0 {
			d.sd710SafetyTrip(masterDevice, fmt.Sprintf(
				"drive fault sw=0x%04X err=0x%04X",
				sw, uint16(masterDevice.PDOErr.Load()&0xFFFF)), 0)
			continue
		}

		if d.safetyState.Load() != 0 {
			masterDevice.desiredTargetVelocity.Store(0)
			masterDevice.EnableJogPDO(false)
			masterDevice.EnablePosPDO(false)
			masterDevice.ppSetpointPending.Store(false)
			masterDevice.desiredControlWord.Store(0x0006)
			continue
		}

		if !masterDevice.PdoDIReady {
			if motion {
				d.sd710SafetyTrip(masterDevice, "0x60FD digital-input PDO became unavailable during motion", 0)
			}
			continue
		}

		di := uint32(masterDevice.PDODI.Load())
		ecs := di&sd710X1ECS != 0
		pot := false       // X2 is external emergency, not POT.
		notActive := false // X3 is reset, not NOT.
		cl := di&sd710X4CL != 0
		dcl := di&sd710X5DCL != 0
		solOn := sd710SolenoidCommanded(masterDevice.Name)

		if motion != lastMotion {
			if motion {
				logger.Info(fmt.Sprintf(
					"[SD710-SEQUENCE] phase=MOTION_ACTIVE drive=%s SOL=%t CL=%t DCL=%t raw60FD=0x%08X jog=%t posMode=%t cmdVel=%d sw=0x%04X CLDL_DELAY=%dms",
					masterDevice.Name, solOn, cl, dcl, di,
					masterDevice.IsJogEnabled(), masterDevice.IsPosEnabled(),
					masterDevice.desiredTargetVelocity.Load(), sw, delayMs))
			} else {
				logger.Info(fmt.Sprintf(
					"[SD710-SEQUENCE] phase=MOTION_COMMAND_CLEARED drive=%s SOL=%t CL=%t DCL=%t raw60FD=0x%08X sw=0x%04X",
					masterDevice.Name, solOn, cl, dcl, di, sw))
			}
			lastMotion = motion
		}

		if di != lastLoggedDI {
			logger.Info(fmt.Sprintf(
				"[SD710-SAFETY-IO] drive=%s raw60FD=0x%08X ECS=%t POT=%t NOT=%t CL=%t DCL=%t SOL=%t motion=%t",
				masterDevice.Name, di, ecs, pot, notActive, cl, dcl, solOn, motion))
			lastLoggedDI = di
		}

		if motion {
			case1FeedbackBadSince = time.Time{}
			case2IdleBadSince = time.Time{}

			// Stable released state required for every type of motion.
			releasedOK := solOn && !cl && dcl && sw&0x006F == 0x0027
			if releasedOK {
				if !motionFeedbackBadSince.IsZero() {
					logger.Info(fmt.Sprintf(
						"[SD710-CLDL-DELAY] drive=%s motion feedback RECOVERED before timeout; SOL=1 CL=0 DCL=1 elapsed=%dms limit=%dms",
						masterDevice.Name,
						time.Since(motionFeedbackBadSince).Milliseconds(),
						delayMs,
					))
				}
				motionFeedbackBadSince = time.Time{}
				continue
			}

			// Safety action is immediate: remove motion as soon as responder
			// feedback is unsafe. The configured CL/DL delay controls when the
			// alarm/fault is raised, not how long the motor is allowed to keep moving.
			if motionFeedbackBadSince.IsZero() {
				motionFeedbackBadSince = time.Now()
				logger.Warn(fmt.Sprintf(
					"[SD710-CLDL-DELAY] drive=%s unsafe feedback detected DURING MOTION; stopping motion now and starting timeout=%dms; expected SOL=1 CL=0 DCL=1 got SOL=%t CL=%t DCL=%t sw=0x%04X raw60FD=0x%08X",
					masterDevice.Name, delayMs, solOn, cl, dcl, sw, di,
				))

				masterDevice.desiredTargetVelocity.Store(0)
				masterDevice.EnableJogPDO(false)
				masterDevice.EnablePosPDO(false)
				masterDevice.ppSetpointPending.Store(false)
				masterDevice.desiredTargetPosition.Store(masterDevice.PDOPos.Load())
				masterDevice.desiredControlWord.Store(0x0006)
				continue
			}

			elapsed := time.Since(motionFeedbackBadSince)
			if elapsed >= delay {
				d.sd710SafetyTrip(masterDevice, fmt.Sprintf(
					"CL/DCL feedback timeout after motion stop: waited %dms; expected SOL=1 CL=0 DCL=1 got SOL=%t CL=%t DCL=%t sw=0x%04X raw60FD=0x%08X",
					delayMs, solOn, cl, dcl, sw, di), 65379)
				motionFeedbackBadSince = time.Time{}
				continue
			}

			if lastModeFeedbackLog.IsZero() || time.Since(lastModeFeedbackLog) >= time.Second {
				logger.Warn(fmt.Sprintf(
					"[SD710-CLDL-DELAY] drive=%s waiting after motion stop elapsed=%dms remaining=%dms expected SOL=1 CL=0 DCL=1 got SOL=%t CL=%t DCL=%t",
					masterDevice.Name,
					elapsed.Milliseconds(),
					(delay - elapsed).Milliseconds(),
					solOn, cl, dcl,
				))
				lastModeFeedbackLog = time.Now()
			}
			continue
		}

		// No active motion command.
		motionFeedbackBadSince = time.Time{}

		if clampMode == 0 {
			// CASE1: permanent RELEASE.
			// SOL remains ON and feedback gets the full configured CL/DL delay
			// to reach CL=0,DCL=1 before a Declamp fault is raised.
			case1OK := solOn && !cl && dcl
			if case1OK {
				if !case1FeedbackBadSince.IsZero() {
					logger.Info(fmt.Sprintf(
						"[SD710-CLDL-DELAY] drive=%s CASE1 RELEASE CONFIRMED before timeout elapsed=%dms limit=%dms SOL=1 CL=0 DCL=1",
						masterDevice.Name,
						time.Since(case1FeedbackBadSince).Milliseconds(),
						delayMs,
					))
				}
				case1FeedbackBadSince = time.Time{}
				continue
			}

			if case1FeedbackBadSince.IsZero() {
				case1FeedbackBadSince = time.Now()
				logger.Warn(fmt.Sprintf(
					"[SD710-CLDL-DELAY] drive=%s CASE1 waiting for permanent release; timeout=%dms expected SOL=1 CL=0 DCL=1 got SOL=%t CL=%t DCL=%t raw60FD=0x%08X",
					masterDevice.Name, delayMs, solOn, cl, dcl, di,
				))
			}

			elapsed := time.Since(case1FeedbackBadSince)
			if elapsed >= delay {
				d.sd710SafetyTrip(masterDevice, fmt.Sprintf(
					"CASE1 declamp timeout: waited %dms for SOL=1 CL=0 DCL=1; got SOL=%t CL=%t DCL=%t raw60FD=0x%08X",
					delayMs, solOn, cl, dcl, di), 65379)
				case1FeedbackBadSince = time.Time{}
				continue
			}

			if lastModeFeedbackLog.IsZero() || time.Since(lastModeFeedbackLog) >= time.Second {
				logger.Warn(fmt.Sprintf(
					"[SD710-CLDL-DELAY] drive=%s CASE1 WAIT elapsed=%dms remaining=%dms expected SOL=1 CL=0 DCL=1 got SOL=%t CL=%t DCL=%t",
					masterDevice.Name,
					elapsed.Milliseconds(),
					(delay - elapsed).Milliseconds(),
					solOn, cl, dcl,
				))
				lastModeFeedbackLog = time.Now()
			}
			continue
		}

		// CASE2 idle: SOL=0, CL=1, DCL=0.
		// Normally common readClampSignal() already performs this timed wait.
		// This monitor also uses the same delay so it never raises an immediate
		// duplicate CL/DCL fault during the mechanical clamp transition.
		case2IdleOK := !solOn && cl && !dcl
		if case2IdleOK {
			if !case2IdleBadSince.IsZero() {
				logger.Info(fmt.Sprintf(
					"[SD710-CLDL-DELAY] drive=%s CASE2 CLAMP CONFIRMED before timeout elapsed=%dms limit=%dms SOL=0 CL=1 DCL=0",
					masterDevice.Name,
					time.Since(case2IdleBadSince).Milliseconds(),
					delayMs,
				))
			}
			case2IdleBadSince = time.Time{}
			continue
		}

		if case2IdleBadSince.IsZero() {
			case2IdleBadSince = time.Now()
			logger.Warn(fmt.Sprintf(
				"[SD710-CLDL-DELAY] drive=%s CASE2 idle waiting for clamp; timeout=%dms expected SOL=0 CL=1 DCL=0 got SOL=%t CL=%t DCL=%t raw60FD=0x%08X",
				masterDevice.Name, delayMs, solOn, cl, dcl, di,
			))
		}

		elapsed := time.Since(case2IdleBadSince)
		if elapsed >= delay {
			d.sd710SafetyTrip(masterDevice, fmt.Sprintf(
				"CASE2 clamp timeout: waited %dms for SOL=0 CL=1 DCL=0; got SOL=%t CL=%t DCL=%t raw60FD=0x%08X",
				delayMs, solOn, cl, dcl, di), 65378)
			case2IdleBadSince = time.Time{}
			continue
		}

		if lastModeFeedbackLog.IsZero() || time.Since(lastModeFeedbackLog) >= time.Second {
			logger.Warn(fmt.Sprintf(
				"[SD710-CLDL-DELAY] drive=%s CASE2 WAIT elapsed=%dms remaining=%dms expected SOL=0 CL=1 DCL=0 got SOL=%t CL=%t DCL=%t",
				masterDevice.Name,
				elapsed.Milliseconds(),
				(delay - elapsed).Milliseconds(),
				solOn, cl, dcl,
			))
			lastModeFeedbackLog = time.Now()
		}
	}
}

// sd710LinkMonitor observes the IgH master's number of responding slaves.
// It provides immediate application logging/latching on cable loss. It cannot
// itself stop a drive after the physical cable has been unplugged; the SD710
// ESC/SM2 process-data watchdog must provide that hardware-side fail-safe.
func (d *SD710) sd710LinkMonitor(masterDevice *MasterDevice, stop <-chan struct{}) {
	if masterDevice == nil || masterDevice.Master == nil {
		return
	}

	ticker := time.NewTicker(sd710LinkPoll)
	defer ticker.Stop()
	lastResponding := ^uint(0)

	logger.Info(fmt.Sprintf(
		"[SD710-LINK] monitor START drive=%s poll=%s; zero responding slaves will latch all software motion",
		masterDevice.Name, sd710LinkPoll,
	))

	for {
		select {
		case <-stop:
			logger.Info(fmt.Sprintf("[SD710-LINK] monitor STOP drive=%s", masterDevice.Name))
			return
		case <-ticker.C:
		}

		var state C.ec_master_state_t
		C.ecrt_master_state(masterDevice.Master, &state)
		responding := uint(state.slaves_responding)

		if responding != lastResponding {
			logger.Info(fmt.Sprintf(
				"[SD710-LINK] drive=%s slaves_responding=%d motion=%t sw=0x%04X pos=%d",
				masterDevice.Name, responding, d.sd710MotionCommanded(masterDevice),
				uint16(masterDevice.PDOStatus.Load()&0xFFFF), masterDevice.PDOPos.Load()))
			lastResponding = responding
		}

		if responding == 0 {
			d.sd710SafetyTrip(masterDevice,
				"EtherCAT link/master lost: slaves_responding=0; software commands cleared (drive-side SM2 watchdog must stop physical motion)", 0)
		}
	}
}

func (d *SD710) readDeclampSignal(masterDevice *MasterDevice, declampTiming int) (bool, error) {
	if masterDevice == nil {
		return false, errors.New("SD710 DCL: nil device")
	}
	if !masterDevice.PdoDIReady {
		return false, errors.New("SD710 DCL: 0x60FD PDO not ready")
	}
	if declampTiming <= 0 {
		declampTiming = 3000
	}

	// Machine truth table for DECLAMPED state:
	// SOL O/P=1 -> CL=0, DCL=1.
	if !sd710SolenoidCommanded(masterDevice.Name) {
		return false, errors.New("SD710 declamp feedback check requested while SOL O/P=0; expected SOL O/P=1")
	}

	logger.Info(fmt.Sprintf(
		"[SD710-CLAMP] phase=WAIT_DECLAMP drive=%s expected SOL=1 CL=0 DCL=1 timeout=%dms",
		masterDevice.Name, declampTiming,
	))

	deadline := time.Now().Add(time.Duration(declampTiming) * time.Millisecond)
	for time.Now().Before(deadline) {
		di := uint32(masterDevice.PDODI.Load())
		cl := di&sd710X4CL != 0
		dcl := di&sd710X5DCL != 0

		// Only the final valid state is accepted. CL=1,DCL=1, CL=1,DCL=0,
		// and CL=0,DCL=0 are all treated as "not yet declamped" until the
		// operator-configured CL/DL delay expires.
		if cl && dcl {
			logger.Warn(fmt.Sprintf(
				"[SD710-CLDL-DELAY] drive=%s WAIT_DECLAMP contradictory CL=1 DCL=1; still waiting within timeout=%dms raw60FD=0x%08X",
				masterDevice.Name, declampTiming, di,
			))
		}

		if !cl && dcl {
			logger.Info(fmt.Sprintf(
				"[SD710-CLAMP] phase=DECLAMP_CONFIRMED drive=%s SOL=1 CL=0 DCL=1 raw60FD=0x%08X",
				masterDevice.Name, di,
			))
			return true, nil
		}

		// CL=1,DCL=0 is still clamped; CL=0,DCL=0 is transition/unknown.
		time.Sleep(10 * time.Millisecond)
	}

	di := uint32(masterDevice.PDODI.Load())
	cause := fmt.Sprintf(
		"declamp failed: expected SOL=1 CL=0 DCL=1; raw60FD=0x%08X CL=%t DCL=%t",
		di, di&sd710X4CL != 0, di&sd710X5DCL != 0,
	)
	d.sd710SafetyTrip(masterDevice, cause, 65379)
	return false, errors.New(cause)
}

func (d *SD710) readClampSignal(masterDevice *MasterDevice, clampTiming int) (bool, error) {
	if masterDevice == nil {
		return false, errors.New("SD710 CL: nil device")
	}
	if !masterDevice.PdoDIReady {
		return false, errors.New("SD710 CL: 0x60FD PDO not ready")
	}
	if clampTiming <= 0 {
		clampTiming = 3000
	}

	// Machine truth table for CLAMPED state:
	// SOL O/P=0 -> CL=1, DCL=0.
	if sd710SolenoidCommanded(masterDevice.Name) {
		return false, errors.New("SD710 clamp feedback check requested while SOL O/P=1; expected SOL O/P=0")
	}

	logger.Info(fmt.Sprintf(
		"[SD710-CLAMP] phase=WAIT_CLAMP drive=%s expected SOL=0 CL=1 DCL=0 timeout=%dms",
		masterDevice.Name, clampTiming,
	))

	deadline := time.Now().Add(time.Duration(clampTiming) * time.Millisecond)
	for time.Now().Before(deadline) {
		di := uint32(masterDevice.PDODI.Load())
		cl := di&sd710X4CL != 0
		dcl := di&sd710X5DCL != 0

		// Only the final valid state is accepted. CL=1,DCL=1, CL=0,DCL=1,
		// and CL=0,DCL=0 are all treated as "not yet clamped" until the
		// operator-configured CL/DL delay expires.
		if cl && dcl {
			logger.Warn(fmt.Sprintf(
				"[SD710-CLDL-DELAY] drive=%s WAIT_CLAMP contradictory CL=1 DCL=1; still waiting within timeout=%dms raw60FD=0x%08X",
				masterDevice.Name, clampTiming, di,
			))
		}

		if cl && !dcl {
			logger.Info(fmt.Sprintf(
				"[SD710-CLAMP] phase=CLAMP_CONFIRMED drive=%s SOL=0 CL=1 DCL=0 raw60FD=0x%08X",
				masterDevice.Name, di,
			))
			return true, nil
		}

		// CL=0,DCL=1 is still declamped; CL=0,DCL=0 is transition/unknown.
		time.Sleep(10 * time.Millisecond)
	}

	di := uint32(masterDevice.PDODI.Load())
	cause := fmt.Sprintf(
		"clamp failed: expected SOL=0 CL=1 DCL=0; raw60FD=0x%08X CL=%t DCL=%t",
		di, di&sd710X4CL != 0, di&sd710X5DCL != 0,
	)
	d.sd710SafetyTrip(masterDevice, cause, 65378)
	return false, errors.New(cause)
}

func (d *SD710) readECSX1(masterDevice *MasterDevice) (bool, error) {
	if masterDevice == nil {
		return false, errors.New("SD710 ECS: nil device")
	}
	if !masterDevice.PdoDIReady {
		return false, errors.New("SD710 ECS: 0x60FD PDO not ready")
	}
	di := uint32(masterDevice.PDODI.Load())
	return di&sd710X1ECS != 0, nil
}

func (d *SD710) receivedECS(masterDevice *MasterDevice, operation ethercatDevice.Operation, stopECSChat chan bool) int {
	for {
		select {
		case <-stopECSChat:
			logger.Debug("[SD710-ECS] stopping ECS polling")
			return 2
		default:
		}

		active, err := d.readECSX1(masterDevice)
		if err != nil {
			logger.Warn("[SD710-ECS] PDO read unavailable:", err)
			time.Sleep(10 * time.Millisecond)
			continue
		}
		if active {
			stable := true
			for i := 0; i < 3; i++ {
				time.Sleep(10 * time.Millisecond)
				check, checkErr := d.readECSX1(masterDevice)
				if checkErr != nil || !check {
					stable = false
					break
				}
			}
			if stable {
				logger.Info("[SD710-ECS] X1 ECS asserted and stable from 0x60FD.bit0")
				return 1
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (d *SD710) receivedECSZero(masterDevice *MasterDevice, operation ethercatDevice.Operation, stopECSChat chan bool) int {
	for {
		select {
		case <-stopECSChat:
			logger.Debug("[SD710-ECS] stopping ECS-zero polling")
			return 2
		default:
		}

		active, err := d.readECSX1(masterDevice)
		if err != nil {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		if !active {
			stable := true
			for i := 0; i < 5; i++ {
				time.Sleep(20 * time.Millisecond)
				check, checkErr := d.readECSX1(masterDevice)
				if checkErr != nil || check {
					stable = false
					break
				}
			}
			if stable {
				logger.Info("[SD710-ECS] X1 ECS de-asserted and stable for 100ms from 0x60FD.bit0")
				return 1
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func sd710WithSDOExclusive(device *MasterDevice, fn func() error) error {
	if device == nil {
		return errors.New("SD710 SDO: nil device")
	}
	sd710MailboxMu.Lock()
	defer sd710MailboxMu.Unlock()
	device.SDOExclusive.Store(true)
	time.Sleep(5 * time.Millisecond)
	defer device.SDOExclusive.Store(false)
	return fn()
}

func sd710ReadU16(device *MasterDevice, address, subindex int, name string) (uint16, error) {
	var value uint16
	err := sd710WithSDOExclusive(device, func() error {
		var err error
		value, err = sd710UploadU16WithRetry(device, address, subindex, name)
		return err
	})
	return value, err
}

// Caller holds sd710MailboxMu through sd710WithSDOExclusive.
// The shared SDOUpload wrapper formats errno as text, so EINTR must be
// recognized from that existing error message. Never retry EIO or an SDO abort.
func sd710UploadU16WithRetry(device *MasterDevice, address, subindex int, name string) (uint16, error) {
	if device == nil {
		return 0, errors.New("SD710 read: nil device")
	}
	step := ethercatDevice.Step{Name: name, Address: address, SubIndex: subindex,
		Value: "0", DataType: "U16", Action: "read"}
	const maxAttempts = 5
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		value, err := SDOUpload2(device.Master, device.Position, step)
		if err == nil {
			return uint16(value), nil
		}
		if !strings.Contains(err.Error(), "Interrupted system call") || attempt == maxAttempts {
			return 0, fmt.Errorf("SD710 read %s (0x%04X:%02X), attempt %d: %w", name, address, subindex, attempt, err)
		}
		logger.Warn(fmt.Sprintf("[SD710-SDO-RETRY] %s interrupted; retry %d/%d", name, attempt+1, maxAttempts))
		time.Sleep(10 * time.Millisecond)
	}
	return 0, errors.New("SD710 read retry exhausted")
}

func sd710WriteU16(device *MasterDevice, address, subindex int, value uint16, name string) error {
	return sd710WithSDOExclusive(device, func() error {
		step := ethercatDevice.Step{Name: name, Address: address, SubIndex: subindex, Value: fmt.Sprintf("%d", value), DataType: "U16", Action: "write"}
		return SDODownload(device.Master, device.Position, step)
	})
}

// sd710Pn631Shadow holds the application-owned Y1/Y2 software output state.
// Bit0 = Y1 SOL O/P, Bit1 = Y2 FIN.  Writes are serialized so FIN never
// accidentally clears SOL, and a SOL transition never destroys an active FIN pulse.
func sd710Pn631State(deviceName string) uint16 {
	if v, ok := sd710Pn631Shadow.Load(deviceName); ok {
		return uint16(v.(*atomic.Uint32).Load() & 0xFFFF)
	}
	return 0
}

func sd710GetPn631Shadow(deviceName string) *atomic.Uint32 {
	if v, ok := sd710Pn631Shadow.Load(deviceName); ok {
		return v.(*atomic.Uint32)
	}
	state := &atomic.Uint32{}
	actual, _ := sd710Pn631Shadow.LoadOrStore(deviceName, state)
	return actual.(*atomic.Uint32)
}

func sd710UpdatePn631Bits(device *MasterDevice, setMask, clearMask uint16, reason string) (uint16, error) {
	if device == nil {
		return 0, errors.New("SD710 Pn631 update: nil device")
	}

	sd710OutputMu.Lock()
	defer sd710OutputMu.Unlock()

	shadow := sd710GetPn631Shadow(device.Name)
	oldValue := uint16(shadow.Load() & 0xFFFF)
	newValue := (oldValue | setMask) &^ clearMask

	if newValue == oldValue {
		return newValue, nil
	}

	if err := sd710WriteU16(device, 0x2006, 0x32, newValue, reason); err != nil {
		return oldValue, err
	}
	shadow.Store(uint32(newValue))
	return newValue, nil
}

func sd710SolenoidCommanded(deviceName string) bool {
	return sd710Pn631State(deviceName)&sd710SolenoidY1Mask != 0
}

// sd710ApplyClampModeOutput applies the operator's two-mode CL/DCL setting.
//
// cl_dl=0 (CASE1): permanent release mode.
//
//	SOL is the initiator and is held HIGH continuously.
//	Expected physical responder feedback is CL=0, DCL=1.
//	Motion does not command SOL OFF at the end.
//
// cl_dl=1 (CASE2): normal interlocked clamp/declamp mode.
//
//	Idle: SOL=0 -> expect CL=1,DCL=0.
//	Before motion: SOL=1 -> wait for CL=0,DCL=1 -> motion allowed.
//	After motion: SOL=0 -> wait for CL=1,DCL=0.
func sd710ApplyClampModeOutput(device *MasterDevice, clampMode int, reason string) error {
	if device == nil {
		return errors.New("SD710 clamp-mode apply: nil device")
	}

	switch clampMode {
	case 0:
		value, err := sd710UpdatePn631Bits(
			device,
			sd710SolenoidY1Mask,
			0,
			reason+" / CASE1 force SOL ON",
		)
		if err != nil {
			return err
		}
		logger.Info(fmt.Sprintf(
			"[SD710-MODE] drive=%s CASE1 APPLIED cl_dl=0 SOL=1 Pn631=0x%04X expected CL=0 DCL=1",
			device.Name, value,
		))
		return nil

	case 1:
		value, err := sd710UpdatePn631Bits(
			device,
			0,
			sd710SolenoidY1Mask,
			reason+" / CASE2 idle SOL OFF",
		)
		if err != nil {
			return err
		}
		logger.Info(fmt.Sprintf(
			"[SD710-MODE] drive=%s CASE2 IDLE APPLIED cl_dl=1 SOL=0 Pn631=0x%04X expected CL=1 DCL=0",
			device.Name, value,
		))
		return nil

	default:
		return fmt.Errorf("SD710 invalid cl_dl=%d; expected 0 or 1", clampMode)
	}
}

// trySD710BreakCommand is the only hook break.go needs.
// handled=false leaves every non-SD710 drive on the existing generic 0x60FE path.
//
// SD710 machine truth table:
//
//	SOL O/P=0 -> CLAMP command   -> expect CL=1, DCL=0
//	SOL O/P=1 -> DECLAMP command -> expect CL=0, DCL=1
func trySD710BreakCommand(device *MasterDevice, solenoidOn bool) (handled bool, err error) {
	if device == nil || device.Device.AddressConfigName != "sd710" {
		return false, nil
	}

	clampMode := settings.GetDriverSettings(device.Name).ClampDeclamp
	if clampMode == 0 && !solenoidOn {
		logger.Info(fmt.Sprintf(
			"[SD710-MODE] drive=%s CASE1 cl_dl=0: CLAMP/SOL-OFF request ignored; SOL must remain HIGH continuously",
			device.Name,
		))
		solenoidOn = true
	}

	if solenoidOn {
		if drv, ok := device.Driver.(*SD710); ok {
			if err := drv.sd710SafetyLatchedError(); err != nil {
				return true, err
			}
		}
	}

	logger.Info(fmt.Sprintf(
		"[SD710-SEQUENCE] phase=SOL_COMMAND drive=%s requestedSOL=%t action=%s",
		device.Name, solenoidOn, map[bool]string{true: "DECLAMP", false: "CLAMP"}[solenoidOn],
	))

	var value uint16
	if solenoidOn {
		value, err = sd710UpdatePn631Bits(device, sd710SolenoidY1Mask, 0, "Pn631 Y1 SOL ON / DECLAMP")
	} else {
		value, err = sd710UpdatePn631Bits(device, 0, sd710SolenoidY1Mask, "Pn631 Y1 SOL OFF / CLAMP")
	}
	if err != nil {
		return true, fmt.Errorf("SD710 SOL O/P command failed: %w", err)
	}

	logger.Info(fmt.Sprintf(
		"[SD710-SOL] drive=%s SOL_OP=%t Pn631=0x%04X expectedCL=%t expectedDCL=%t",
		device.Name, solenoidOn, value, !solenoidOn, solenoidOn,
	))
	logger.Info(fmt.Sprintf(
		"[SD710-SEQUENCE] phase=SOL_COMMAND_CONFIRMED drive=%s SOL=%t; now waiting for responder feedback CL=%t DCL=%t",
		device.Name, solenoidOn, !solenoidOn, solenoidOn,
	))
	return true, nil
}

func (d *SD710) sendFinishSignal(masterDevice *MasterDevice, operation ethercatDevice.Operation) error {
	if masterDevice == nil {
		return errors.New("SD710 sendFinishSignal: nil device")
	}

	if err := d.sd710SafetyLatchedError(); err != nil {
		return err
	}
	// Preserve Y1 SOL state while pulsing Y2 FIN.
	d.finPulse.Store(true)
	onValue, err := sd710UpdatePn631Bits(masterDevice, sd710FinishY2Mask, 0, "Pn631 Y2 FIN ON")
	if err != nil {
		d.finPulse.Store(false)
		return fmt.Errorf("[SD710-FIN] write Pn631 ON failed: %w", err)
	}
	logger.Info(fmt.Sprintf(
		"[SD710-FIN] UI ON + Y2 ON drive=%s Pn631=0x%04X SOL_preserved=%t",
		masterDevice.Name, onValue, onValue&sd710SolenoidY1Mask != 0,
	))

	delayMs := settings.GetDriverSettings(masterDevice.Name).ECSFinTiming
	if delayMs <= 0 {
		delayMs = 500
	}
	deadline := time.Now().Add(time.Duration(delayMs) * time.Millisecond)
	for time.Now().Before(deadline) && d.safetyState.Load() == 0 {
		time.Sleep(5 * time.Millisecond)
	}

	offValue, err := sd710UpdatePn631Bits(masterDevice, 0, sd710FinishY2Mask, "Pn631 Y2 FIN OFF")
	if err != nil {
		d.finPulse.Store(false)
		return fmt.Errorf("[SD710-FIN] write Pn631 OFF failed: %w", err)
	}

	d.finPulse.Store(false)
	logger.Info(fmt.Sprintf(
		"[SD710-FIN] UI OFF + Y2 OFF drive=%s Pn631=0x%04X SOL_preserved=%t",
		masterDevice.Name, offValue, offValue&sd710SolenoidY1Mask != 0,
	))
	if err := d.sd710SafetyLatchedError(); err != nil {
		return err
	}
	return nil
}

var (
	sd710MailboxMu   sync.Mutex
	sd710OutputMu    sync.Mutex
	sd710Pn631Shadow sync.Map // map[string]*atomic.Uint32
)

func (d *SD710) pollIOStat(availableDevices []*MasterDevice) {
	d.ioStopMu.Lock()
	if len(d.ioStops) != 0 {
		d.ioStopMu.Unlock()
		return
	}
	d.ioStops = make([]chan struct{}, 0, len(availableDevices))
	d.ioStopMu.Unlock()

	for _, dev := range availableDevices {
		if dev == nil {
			continue
		}
		stop := make(chan struct{})
		d.ioStopMu.Lock()
		d.ioStops = append(d.ioStops, stop)
		d.ioStopMu.Unlock()

		d.ioWG.Add(3)
		go d.ioStatusListener(dev, stop)
		go func(md *MasterDevice, ch <-chan struct{}) {
			defer d.ioWG.Done()
			d.sd710SafetyMonitor(md, ch)
		}(dev, stop)
		go func(md *MasterDevice, ch <-chan struct{}) {
			defer d.ioWG.Done()
			d.sd710LinkMonitor(md, ch)
		}(dev, stop)
	}
}

func (d *SD710) stopPollIOStat() {
	d.ioStopMu.Lock()
	for _, stop := range d.ioStops {
		close(stop)
	}
	d.ioStops = nil
	d.ioStopMu.Unlock()
	waitWithTimeout(&d.ioWG, 2*time.Second, "SD710 ioStatusListener")
}

func (d *SD710) ioStatusListener(masterDev *MasterDevice, stop <-chan struct{}) {
	defer d.ioWG.Done()

	logger.Info(fmt.Sprintf("[SD710-PDO-IO] drive=%s 100%% mailbox-free I/O: X1..X5 from 0x60FD PDO; UI outputs from logical/native states; no Un100/Un101/0x60FE runtime monitoring", masterDev.Name))

	if !masterDev.PdoDIReady {
		logger.Warn(fmt.Sprintf("[SD710-PDO-IO] drive=%s 0x60FD digital input PDO is not ready", masterDev.Name))
	}

	var (
		haveLast                                   bool
		lastECS, lastPOT, lastNOT, lastCL, lastDCL bool
		lastFIN, lastServoReady, lastSolOp         bool
		lastNotify                                 time.Time
		lastDI                                     uint32 = 0xFFFFFFFF
	)

	for {
		select {
		case <-stop:
			return
		default:
		}

		di := uint32(masterDev.PDODI.Load())
		if masterDev.PdoDIReady {
			if d.sd710ResetEdge(di) {
				if (sd710VCSDSoftTestMode || sd710EmergencyReleased(masterDev)) && !systemResetInProgress.Load() {
					go performSysReset(true)
				} else {
					logger.Warn("[SD710-RESET] ignored: emergency active or reset already running")
				}
			}
		}
		sw := uint16(masterDev.PDOStatus.Load() & 0xFFFF)

		ecs := masterDev.PdoDIReady && di&sd710X1ECS != 0
		pot := false
		notActive := false
		cl := masterDev.PdoDIReady && di&sd710X4CL != 0
		dcl := masterDev.PdoDIReady && di&sd710X5DCL != 0
		fin := d.finPulse.Load()
		servoReady := sw&0x006F == 0x0027
		solOp := sd710SolenoidCommanded(masterDev.Name)

		if di != lastDI {
			logger.Info(fmt.Sprintf("[SD710-PDO-IN] drive=%s raw60FD=0x%08X X1/ECS=%t X2/emergencyReleased=%t X3/reset=%t X4/CL=%t X5/DCL=%t testMode=%t", masterDev.Name, di, ecs, sd710EmergencyReleased(masterDev), di&sd710X3Reset != 0, cl, dcl, sd710VCSDSoftTestMode))
			lastDI = di
		}

		changed := !haveLast || ecs != lastECS || pot != lastPOT || notActive != lastNOT || cl != lastCL || dcl != lastDCL || fin != lastFIN || servoReady != lastServoReady || solOp != lastSolOp
		heartbeat := lastNotify.IsZero() || time.Since(lastNotify) >= sd710UIHeartbeat
		activeOutputRefresh := fin || solOp

		if changed || heartbeat || activeOutputRefresh {
			statusnotifier.NotifyIOStatus(statusnotifier.IOStatus{
				DriveName: masterDev.Name,
				ECS:       ecs,
				POT:       pot,
				NOT:       notActive,
				HOME:      false,
				ALMIN:     false,
				CL:        cl,
				DCL:       dcl,
				ALMOUT:    servoReady,
				FIN:       fin,
				SOLOP:     solOp,
			})

			if changed {
				logger.Info(fmt.Sprintf("[SD710-UI-IO] drive=%s ECS=%t POT=%t NOT=%t CL=%t DCL=%t SOL_OP=%t FIN=%t ALM_OUT/SERVO_READY=%t source=PDO/logical", masterDev.Name, ecs, pot, notActive, cl, dcl, solOp, fin, servoReady))
			}

			lastNotify = time.Now()
			haveLast = true
			lastECS, lastPOT, lastNOT, lastCL, lastDCL = ecs, pot, notActive, cl, dcl
			lastFIN, lastServoReady, lastSolOp = fin, servoReady, solOp
		}

		select {
		case <-stop:
			return
		case <-time.After(sd710UIRefresh):
		}
	}
}

func (d *SD710) SetupPDO(dev *MasterDevice) error {
	if dev == nil {
		return errors.New("SD710.SetupPDO: nil device")
	}

	d.device = dev

	// Verify the selected input profile before PDO activation.
	for _, input := range []struct {
		subindex  int
		name      string
		testValue uint16
	}{
		{0x02, "Pn601/X1", 0x0000},
		{0x03, "Pn602/X2", 0x1019},
		{0x04, "Pn603/X3", 0x1004},
		{0x05, "Pn604/X4", 0x0000},
		{0x06, "Pn605/X5", 0x1000},
	} {
		expected := uint16(0)
		if sd710VCSDSoftTestMode {
			expected = input.testValue
		}
		value, err := sd710ReadU16(dev, 0x2006, input.subindex, input.name)
		if err != nil {
			return fmt.Errorf("SD710 input configuration readback failed for %s: %w", input.name, err)
		}
		if value != expected {
			return fmt.Errorf("SD710 input configuration mismatch: %s=0x%04X; expected 0x%04X (VCSDSoftTestMode=%t)", input.name, value, expected, sd710VCSDSoftTestMode)
		}
	}
	logger.Warn(fmt.Sprintf("[SD710-INPUT-PROFILE] drive=%s VCSDSoftTestMode=%t; test profile permits software X2/X3 clear ONLY on explicit reset", dev.Name, sd710VCSDSoftTestMode))
	cfg := settings.GetDriverSettings(dev.Name)
	logger.Info(fmt.Sprintf(
		"[SD710-SETTINGS-CHECK] drive=%s cl_dl=%d cldl_timing=%d jog_feed=%d",
		dev.Name,
		cfg.ClampDeclamp,
		cfg.ClampDeclampTiming,
		cfg.JogFeed,
	))
	if cfg.ClampDeclamp != 0 && cfg.ClampDeclamp != 1 {
		logger.Error(fmt.Sprintf(
			"[SD710-MODE] drive=%s invalid cl_dl=%d; only 0 or 1 is supported",
			dev.Name, cfg.ClampDeclamp,
		))
		return fmt.Errorf("SD710 invalid cl_dl=%d; expected 0 or 1", cfg.ClampDeclamp)
	}
	if cfg.ClampDeclamp == 0 {
		logger.Info(fmt.Sprintf(
			"[SD710-MODE] drive=%s cl_dl=0 CASE1: permanent RELEASE mode; SOL initiator forced ON, expected responders CL=0 DCL=1; no clamp cycle around motion",
			dev.Name,
		))
	} else {
		logger.Info(fmt.Sprintf(
			"[SD710-MODE] drive=%s cl_dl=1 CASE2: interlocked CLAMP/DECLAMP mode; idle SOL=0 CL=1 DCL=0; before every motion SOL->1 then wait CL=0 DCL=1",
			dev.Name,
		))
	}
	logger.Info("[SD710-SETUP] BEGIN generic YAML-driven PDO setup")
	logger.Info("[SD710-STARTUP] SDO-safe profile active: retained motion parameters are not rewritten at every boot; YAML faultReset is intentionally empty")
	logger.Info("[SD710-REV] HOME removed; X1=ECS X2=emergency X3=reset(rising-edge) X4=CL X5=DCL from 0x60FD bits0..4; VCSDSoft test uses X2 ON=pressed")
	logger.Info("[SD710-REV] clamp truth table enforced: SOL=0 -> CL=1,DCL=0; SOL=1 -> CL=0,DCL=1; CL=DCL=1 is invalid")
	logger.Info("[SD710-REV] Y1=software SOL O/P via Pn611=0x1007/Pn631.bit0; Y2=FIN via Pn612=0x1002/Pn631.bit1; Y3=native Servo Ready")
	logger.Info("[SD710-REV] runtime input feedback is mailbox-free from 0x60FD; 0x60FE is not used for SD710 SOL/FIN logic")
	logger.Info(fmt.Sprintf("[SD710-SAFETY] enabled: CASE1 cl_dl=0 holds SOL=1 permanently and waits up to %dms for CL=0,DCL=1; CASE2 cl_dl=1 uses the same %dms CL/DL timeout for clamp/declamp; motion is stopped immediately on unsafe feedback and fault is raised if feedback stays bad until timeout", cfg.ClampDeclampTiming, cfg.ClampDeclampTiming))
	logger.Info("[SD710-SAFETY] cable-pull note: application monitor can detect/latch link loss, but physical stop after cable removal depends on the SD710 drive-side SM2 process-data watchdog")
	sd710GetPn631Shadow(dev.Name).Store(0)

	if err := setupPDOPositionGeneric(dev); err != nil {
		return fmt.Errorf("SD710.SetupPDO: generic PDO setup failed: %w", err)
	}
	if !dev.PdoReady || !dev.PdoRxReady {
		return fmt.Errorf("SD710.SetupPDO: PDO registration incomplete: PdoReady=%t PdoRxReady=%t", dev.PdoReady, dev.PdoRxReady)
	}

	// Apply the operator-selected CL/DCL mode while we are still in the
	// pre-activation setup phase. This is what makes CASE1 immediately hold
	// the brake/solenoid release output HIGH without relying on common
	// clamp_declamp.go, which intentionally bypasses sequencing when cl_dl=0.
	if err := sd710ApplyClampModeOutput(dev, cfg.ClampDeclamp, "SD710 startup clamp mode"); err != nil {
		return fmt.Errorf("SD710.SetupPDO: failed to apply cl_dl=%d output policy: %w", cfg.ClampDeclamp, err)
	}
	sc := dev.SlaveConfig
	if sc == nil {
		return errors.New("SD710.SetupPDO: SlaveConfig is nil after generic setup")
	}

	reqPtr := C.create_profile_vel_sdo_request(sc)
	if reqPtr != nil {
		dev.PdoVelSdoReq = reqPtr
		dev.PdoVelSdoReady = true
		logger.Info("[PDO] SD710: Profile Velocity SDO request registered (0x6081)")
	} else {
		dev.PdoVelSdoReq = nil
		dev.PdoVelSdoReady = false
		logger.Warn("[PDO] SD710: create_profile_vel_sdo_request failed")
	}

	logger.Info(fmt.Sprintf("[SD710-SETUP] READY PdoReady=%t RxReady=%t StatusReady=%t ErrorReady=%t DIReady=%t DOCommandReady=%t OpModeDisplayReady=%t MTResetObject=0x2000:80", dev.PdoReady, dev.PdoRxReady, dev.PdoStatusReady, dev.PdoErrorReady, dev.PdoDIReady, dev.PdoDigOutReady, dev.PdoOpModeDisplayReady))
	return nil
}

// SD710 application tolerance: 0.0005 degrees.
// Matches the calculation in your working EtherCATFRD build.
func sd710TargetPositionTolerance(device *MasterDevice) int32 {
	if device == nil || device.Device.DriveXRatio <= 0 {
		return 10
	}

	tolerance := int32(
		float64(device.Device.DriveXRatio)*0.0005 + 0.5,
	)
	if tolerance < 1 {
		return 1
	}
	return tolerance
}

func (d *SD710) IsTargetReached(sw uint16) bool {
    if d.device == nil || d.safetyState.Load() != 0 {
        return false
    }

    if !IsPDOActive() || d.device.posMoveAborted.Load() {
        return false
    }

    if sw&0x0008 != 0 {
        return false
    }

    if sw&sd710TargetReachedBit == 0 ||
        sw&sd710SetPointAckBit != 0 {
        return false
    }

    if !d.device.PdoOpModeDisplayReady ||
        int8(d.device.PDOOpModeDisplay.Load()) != 1 {
        return false
    }

    goal := int64(d.device.desiredTargetPosition.Load())
    actual := int64(d.device.PDOPos.Load())

    diff := goal - actual
    if diff < 0 {
        diff = -diff
    }

    tolerance := int64(sd710TargetPositionTolerance(d.device))

    if diff > tolerance {
        now := time.Now().UnixNano()
        last := d.ppCompletionLastLog.Load()

        if now-last >= int64(500*time.Millisecond) &&
            d.ppCompletionLastLog.CompareAndSwap(last, now) {

            logger.Info(fmt.Sprintf(
                "[SD710-PP-CHECK] drive=%s sw=0x%04X "+
                    "mode=%d goal=%d pdoTarget=%d actual=%d "+
                    "error=%d tolerance=%d completion=BLOCKED",
                d.device.Name,
                sw,
                d.device.PDOOpModeDisplay.Load(),
                goal,
                d.device.currentTargetPosition.Load(),
                actual,
                diff,
                tolerance,
            ))
        }

        return false
    }

    return true
}

func (d *SD710) JogControlword(cwBase uint16) uint16 {
	if d.safetyState.Load() != 0 {
		return 0x0006
	}

	// CASE1 bench/manual-I/O guard:
	// SOL is permanently ON, but the responder DCL must still be ON before
	// enabling Profile Velocity. This is especially useful while X4/X5 are
	// being forced manually from VCSDSoft.
	if d.device != nil {
		cfg := settings.GetDriverSettings(d.device.Name)
		if cfg.ClampDeclamp == 0 && d.device.PdoDIReady {
			di := uint32(d.device.PDODI.Load())
			cl := di&sd710X4CL != 0
			dcl := di&sd710X5DCL != 0
			if cl || !dcl || !sd710SolenoidCommanded(d.device.Name) {
				return 0x0006
			}
		}
	}

	return cwBase &^ uint16(0x0100)
}
func (d *SD710) FaultResetControlword() uint16 { return 0x0080 }
func (d *SD710) StandbyOpMode() int8           { return 1 }
func (d *SD710) SupportsMultiTurnReset() bool  { return true }

func (d *SD710) ResetMultiTurn(availableDevices []*MasterDevice) error {
	for _, device := range availableDevices {
		if device == nil {
			continue
		}
		operation, err := GetEtherCATOperation("resetMultiTurn", device.Device.AddressConfigName)
		if err != nil {
			return fmt.Errorf("[MT-SD710] resetMultiTurn config missing for %s: %w", device.Name, err)
		}
		if len(operation.Steps) != 3 {
			return fmt.Errorf("[MT-SD710] resetMultiTurn must contain exactly verify-read, Pn07F=1 write, post-read")
		}
		if operation.Steps[0].Action != "read" || operation.Steps[1].Action != "write" || operation.Steps[2].Action != "read" {
			return fmt.Errorf("[MT-SD710] safety guard: resetMultiTurn sequence must be read -> write -> read")
		}
		for i, step := range operation.Steps {
			if step.Address != 0x2000 || step.SubIndex != 0x80 || step.DataType != "U16" {
				return fmt.Errorf("[MT-SD710] safety guard: step %d must target Pn07F at 0x2000:0x80 U16", i)
			}
		}
		if operation.Steps[1].Value != "1" {
			return fmt.Errorf("[MT-SD710] safety guard: Pn07F reset write must be value 1")
		}

		logger.Info("[MT-SD710] Starting absolute encoder multi-turn reset for:", device.Name)
		device.EnableJogPDO(false)
		device.EnablePosPDO(false)
		device.desiredTargetVelocity.Store(0)
		_ = device.SetTargetPositionPDO(device.PDOPos.Load())
		device.pdoMTResetActive.Store(true)

		cleanup := func() {
			device.SDOExclusive.Store(false)
			device.pdoFaultResetActive.Store(false)
			device.pdoMTResetActive.Store(false)
		}
		defer cleanup()

		deadline := time.Now().Add(5 * time.Second)
		safe := false
		for time.Now().Before(deadline) {
			sw := uint16(device.PDOStatus.Load() & 0xFFFF)
			if sw&0x0008 != 0 {
				device.pdoFaultResetActive.Store(true)
				time.Sleep(100 * time.Millisecond)
				device.pdoFaultResetActive.Store(false)
				time.Sleep(200 * time.Millisecond)
				continue
			}
			if sw&0x006F == 0x0021 {
				safe = true
				logger.Info(fmt.Sprintf("[MT-SD710] Servo OFF confirmed (Ready To Switch On): sw=0x%04X", sw))
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if !safe {
			return fmt.Errorf("[MT-SD710] timeout waiting for Servo-OFF Ready To Switch On, sw=0x%04X; NO Pn07F write issued", uint16(device.PDOStatus.Load()&0xFFFF))
		}

		time.Sleep(200 * time.Millisecond)
		before, err := sd710ReadU16(device, 0x2000, 0x80, "Pn07F pre-reset verification")
		if err != nil {
			return fmt.Errorf("[MT-SD710] Pn07F pre-read failed; NO reset write issued: %w", err)
		}
		logger.Info(fmt.Sprintf("[MT-SD710] Pn07F pre-read OK value=0x%04X", before))
		if err := sd710WriteU16(device, 0x2000, 0x80, 1, "Pn07F=1 absolute encoder multi-turn clear"); err != nil {
			return fmt.Errorf("[MT-SD710] Pn07F=1 write failed: %w", err)
		}
		logger.Info("[MT-SD710] Pn07F=1 write accepted")
		time.Sleep(300 * time.Millisecond)
		after, err := sd710ReadU16(device, 0x2000, 0x80, "Pn07F post-reset verification")
		if err != nil {
			return fmt.Errorf("[MT-SD710] Pn07F post-read failed after reset command: %w", err)
		}
		if after != 0 {
			logger.Warn(fmt.Sprintf("[MT-SD710] Pn07F post-read is 0x%04X, expected auto-clear to 0x0000", after))
		} else {
			logger.Info("[MT-SD710] Pn07F auto-clear confirmed (0x0000)")
		}
		device.pdoMTResetActive.Store(false)

		enableDeadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(enableDeadline) {
			if uint16(device.PDOStatus.Load()&0x006F) == 0x0027 {
				logger.Info(fmt.Sprintf("[MT-SD710] Multi-turn reset complete; drive re-enabled sw=0x%04X pos=%d", uint16(device.PDOStatus.Load()&0xFFFF), device.PDOPos.Load()))
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		logger.Warn("[MT-SD710] Multi-turn reference was cleared. Run Zero Reference before normal positioning:", device.Name)
	}
	return nil
}

func (d *SD710) CleanupAfterJog(device *MasterDevice) error {
	if device == nil {
		return errors.New("SD710 CleanupAfterJog: nil device")
	}
	device.desiredTargetVelocity.Store(0)
	time.Sleep(50 * time.Millisecond)
	device.EnableJogPDO(false)
	device.EnablePosPDO(false)
	device.desiredControlWord.Store(0x000F)
	device.ppSetpointPending.Store(false)
	device.desiredOpMode.Store(1)
	time.Sleep(200 * time.Millisecond)
	if sw := uint16(device.PDOStatus.Load() & 0xFFFF); sw&0x0008 != 0 {
		return fmt.Errorf("SD710 CleanupAfterJog: drive fault sw=0x%04X err=0x%04X", sw, uint16(device.PDOErr.Load()&0xFFFF))
	}
	return nil
}

func init() { RegisterDriver("sd710", func() IMotorDriver { return &SD710{} }) }
