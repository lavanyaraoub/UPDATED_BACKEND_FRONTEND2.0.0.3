package motordriver

/*
Delta ASDA-A2-E IMotorDriver implementation.

Key differences from A6Minas:
 1. Digital inputs come from standard CiA-402 object 0x60FD (not vendor 0x4F25).
 2. POT (bit 1) and NOT (bit 0) in 0x60FD are active-HIGH (bit=1 means limit active).
 3. No vendor-specific clamp/declamp or ECS signals — those methods are no-ops.
 4. Multiturn reset uses blocking SDO writes to P2-08 and P2-71 (not vendor async objects).

Fixes applied in this version:
 [Bug 1] JogControlword: ramp bits 4,5,6 (0x0070) were never OR-ed in.
         Without them the Delta drive ignores the velocity setpoint — motor
         does not move or moves erratically during jog. Fixed by applying
         (cwBase | 0x0070) & ^uint16(0x0100).
 [Bug 2] deltaIOStatusListener: stabilisation deadline was 2s — same race
         condition the A6 had before it was fixed to 10s. On a Pi under load
         the CiA-402 walk to Operation Enabled can take >2s, causing a false
         "NOT/POT protection disabled" warning and leaving hardware limit
         switch protection inactive for the session. Fixed to 10s.
*/

import (
	ethercatDevice "EtherCAT/ethercatdevicedatatypes"
	logger "EtherCAT/logger"
	"EtherCAT/motordriver/statusnotifier"
	settings "EtherCAT/settings"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// pdoSDOExclusive pauses the PDO cyclic's ecrt_master_receive calls so that a
// blocking ecrt_master_sdo_download from another goroutine has exclusive access
// to the IgH CoE mailbox. Without this, the cyclic consumes the drive's delayed
// mailbox response (e.g. for EEPROM-backed parameters like P2-71) before the
// blocking SDO caller can see it — causing EIO on every attempt.
//
// Set true immediately before the blocking SDO write, clear immediately after.
// The cyclic yields for 1ms per tick while this flag is set.
var pdoSDOExclusive atomic.Bool

// DeltaASDA2E is the IMotorDriver implementation for the Delta ASDA-A2-E drive.
type DeltaASDA2E struct{}

// hasTargetReached polls the PDO statusword bit 10 (Target Reached) until set.
// The Delta drive uses standard CiA-402 — bit 10 alone is sufficient; no
// Set-Point Acknowledge handshake is required (that is A6-specific behaviour).
func (d DeltaASDA2E) hasTargetReached(masterDevice *MasterDevice, action int, immediate int, operation ethercatDevice.Operation) error {
	logger.Trace("DeltaASDA2E waiting for target reached")
	firstStep := operation.Steps[0]
	secondStep := operation.Steps[1]

	SDODownload(masterDevice.Master, masterDevice.Position, firstStep)

	for {
		result, _ := SDOUpload2(masterDevice.Master, masterDevice.Position, secondStep)
		sw := uint16(result)
		if (sw>>10)&1 == 1 {
			logger.Trace("DeltaASDA2E target reached")
			return nil
		}
	}
}

// potNotEnabled returns true if either POT (bit 1) or NOT (bit 0) of 0x60FD is
// asserted. Delta ASDA-A2-E uses the standard CiA-402 active-HIGH convention:
//
//	bit 0 = NOT (Negative Over-Travel): 1 = limit switch triggered
//	bit 1 = POT (Positive Over-Travel): 1 = limit switch triggered
//
// PREVIOUS BUG: this unconditionally returned false, silently disabling all
// hardware over-travel protection. Any POT/NOT trip was invisible to the software,
// allowing the motor to continue past physical limit switches.
//
// FIX: Read 0x60FD from the PDO buffer (updated every 1ms by the cyclic task).
func (d DeltaASDA2E) potNotEnabled(masterDevice *MasterDevice) (bool, error) {
	di := uint32(masterDevice.PDODI.Load()) // per-device read, not masterDevices[0]
	not := (di & (1 << 0)) != 0             // bit 0: Negative limit switch (active-HIGH)
	pot := (di & (1 << 1)) != 0             // bit 1: Positive limit switch (active-HIGH)
	return pot || not, nil
}

// readDeclampSignal is a no-op for Delta — no declamp output mapped in PDO.
// Returns true immediately so callers do not block.
func (d DeltaASDA2E) readDeclampSignal(masterDevice *MasterDevice, declampTiming int) (bool, error) {
	return true, nil
}

// readClampSignal is a no-op for Delta — no clamp output mapped in PDO.
// Returns true immediately so callers do not block.
func (d DeltaASDA2E) readClampSignal(masterDevice *MasterDevice, clampTiming int) (bool, error) {
	return true, nil
}

// receivedECS is a no-op for Delta — no ECS input in standard 0x60FD.
// Returns 1 (signal received) immediately so callers do not block.
func (d DeltaASDA2E) receivedECS(masterDevice *MasterDevice, operation ethercatDevice.Operation, stopECSChat chan bool) int {
	return 1
}

// receivedECSZero is a no-op for Delta.
// Returns 1 immediately so callers do not block.
func (d DeltaASDA2E) receivedECSZero(masterDevice *MasterDevice, operation ethercatDevice.Operation, stopECSChat chan bool) int {
	return 1
}

// sendFinishSignal toggles the Delta digital output for the finish signal.
//
// FIX (Bug 9): Previously returned errors.New("Delta DO PDO not ready") when
// PdoDigOutReady=false, which propagated as an alarm and blocked the jog path.
// The finish signal is non-critical for Delta — if DO is not ready, log a
// warning and return nil so callers are not interrupted.
func (d DeltaASDA2E) sendFinishSignal(masterDevice *MasterDevice, operation ethercatDevice.Operation) error {
	if !masterDevice.PdoDigOutReady {
		logger.Warn("[HAL-Delta] sendFinishSignal: PdoDigOutReady=false — skipping (non-critical)")
		return nil
	}

	// 1. Fetch the REAL memory pointer from the global slice
	// so the cyclic task actually sees the state change.
	var realDevice *MasterDevice
	for _, dev := range getMasterDevices() {
		if dev.Name == masterDevice.Name {
			realDevice = dev
			break
		}
	}
	if realDevice == nil {
		logger.Warn("[HAL-Delta] sendFinishSignal: device not found in global slice — skipping")
		return nil
	}

	// 2. Fetch the dynamic timing from the config
	driverSettings := settings.GetDriverSettings(masterDevice.Name)

	// 3. Assert the Output (0x00010000 sets Bit 16 / DO1 high)
	PDOSetDigitalOutput([]*MasterDevice{realDevice}, 0, 0x00010000)

	// Wait using the configured timing
	time.Sleep(time.Duration(driverSettings.ECSFinTiming) * time.Millisecond)

	// 4. Release the Output
	PDOSetDigitalOutput([]*MasterDevice{realDevice}, 0, 0)

	return nil
}

// deltaIOStopChansMu protects deltaIOStopChans.
var deltaIOStopChansMu sync.Mutex

// deltaIOStopChans holds one stop channel per deltaIOStatusListener goroutine.
var deltaIOStopChans []chan struct{}

// pollIOStat starts one I/O status listener goroutine per device.
func (d DeltaASDA2E) pollIOStat(availableDevices []*MasterDevice) {
	logger.Debug("starting DeltaASDA2E I/O status listener")
	deltaIOStopChansMu.Lock()
	deltaIOStopChans = make([]chan struct{}, 0, len(availableDevices))
	deltaIOStopChansMu.Unlock()
	for _, dev := range availableDevices {
		ch := make(chan struct{})
		deltaIOStopChansMu.Lock()
		deltaIOStopChans = append(deltaIOStopChans, ch)
		deltaIOStopChansMu.Unlock()
		ioPollingWG.Add(1)
		go d.deltaIOStatusListener(dev, ch)
	}
}

// stopPollIOStat stops ALL Delta I/O status listener goroutines and blocks
// until they have actually exited (up to a bounded timeout).
func (d DeltaASDA2E) stopPollIOStat() {
	deltaIOStopChansMu.Lock()
	for _, ch := range deltaIOStopChans {
		close(ch)
	}
	deltaIOStopChans = nil
	deltaIOStopChansMu.Unlock()
	waitWithTimeout(&ioPollingWG, 2*time.Second, "DeltaASDA2E ioStatusListener")
}

// deltaIOStatusListener reads 0x60FD from the PDO buffer and publishes I/O status.
//
// Startup guard: waits for sw&0x006F==0x0027 (Operation Enabled) before enabling
// POT/NOT hardware checks — prevents false alarms from zero-initialized DI register.
//
// FIX (Bug 2): Stabilisation deadline extended from 2s to 10s.
//
//	The old 2s deadline triggered a false "NOT/POT protection disabled" warning
//	on every startup under Pi load — identical to the race the A6 listener had
//	before its own fix. The CiA-402 state machine walk to Operation Enabled:
//	  ecrt_master_activate() → ESM PREOP→SAFEOP→OP
//	  Switch On Disabled (0x0040) → Ready To Switch On (0x0021)  ~50ms
//	  → Switched On (0x0023)                                      ~50ms
//	  → Operation Enabled (0x0027)                                ~200ms
//	  deltaIOStatusListener starts immediately after StartPDOCyclic returns
//
//	Under normal conditions the drive reaches Op Enabled in ~300–600ms.
//	Under load (Pi running GC, background tasks, USB activity) the scheduler
//	can delay the 1ms cyclic task for hundreds of milliseconds, pushing the
//	total time beyond 2s. 10s covers the absolute worst-case scenario.
//	In the normal case the loop exits as soon as Op Enabled is detected (~300ms)
//	— the extra headroom costs nothing.
//
// Delta 0x60FD active-HIGH convention (CiA-402 standard):
//
//	bit 0 — Negative limit switch (NOT) : 1 = limit active
//	bit 1 — Positive limit switch (POT) : 1 = limit active
//	bit 2 — Home switch                 : 1 = home active
func (d DeltaASDA2E) deltaIOStatusListener(masterDev *MasterDevice, stop <-chan struct{}) {
	defer ioPollingWG.Done()
	pdoStabilised := false
	if masterDev.PdoDIReady {
		// FIX (Bug 2): Extended deadline from 2s to 10s — see function-level comment.
		stabiliseDeadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(stabiliseDeadline) {
			sw := uint16(masterDev.PDOStatus.Load() & 0xFFFF)
			if sw&0x006F == 0x0027 { // Operation Enabled
				pdoStabilised = true
				logger.Info("[IO] Delta PDO stabilised, drive in Operation Enabled — enabling NOT/POT protection for:", masterDev.Name)
				break
			}
			select {
			case <-stop:
				return
			default:
				time.Sleep(20 * time.Millisecond)
			}
		}
		if !pdoStabilised {
			// This should not happen under normal conditions with a 10s window.
			// If it does, the drive likely failed to reach Op Enabled at all
			// (hardware fault, cable issue, or Error 80 at startup).
			// NOT/POT protection remains disabled — motion is still allowed
			// but hardware limit switch protection will not trigger automatically.
			logger.Warn("[IO] Delta drive did not reach Operation Enabled within 10s — " +
				"NOT/POT hardware limit protection disabled for this session. " +
				"Check for startup faults (Error 80, cable, drive power):" + masterDev.Name)
		}
	} else {
		pdoStabilised = true // SDO path has no startup race
	}

	lastPOT := false
	lastNOT := false
	if masterDev.PdoDIReady {
		initialDI := uint32(masterDev.PDODI.Load())
		lastNOT = (initialDI & (1 << 0)) != 0
		lastPOT = (initialDI & (1 << 1)) != 0
	}

	for {
		select {
		case <-stop:
			logger.Debug("stopping DeltaASDA2E I/O status listener for:", masterDev.Name)
			return
		default:
		}

		// Read 0x60FD from the PDO buffer (updated every 1ms by the cyclic task).
		//
		// PREVIOUS BUG: di was commented out and ioStat.NOT/POT/HOME were hardcoded
		// false, disabling ALL hardware limit-switch detection in the IO listener.
		// This means the deltaIOStatusListener was never actually reporting real
		// hardware state — the emergency stop block below was permanently dead.
		//
		// Per-device read from masterDev.PDODI — correct on multi-axis systems.
		// Previously used GetLastPDODigitalInputs() which always read masterDevices[0].
		di := uint32(masterDev.PDODI.Load())

		var ioStat statusnotifier.IOStatus

		// Delta 0x60FD active-HIGH (CiA-402 standard):
		//   bit 0 = NOT (Negative Over-Travel): 1 = limit active
		//   bit 1 = POT (Positive Over-Travel): 1 = limit active
		//   bit 2 = HOME switch:                1 = home active
		ioStat.NOT = (di & (1 << 0)) != 0
		ioStat.POT = (di & (1 << 1)) != 0
		ioStat.HOME = (di & (1 << 2)) != 0
		// ALMOUT: drive fault bit (statusword bit 3)
		ioStat.ALMOUT = (uint16(masterDev.PDOStatus.Load()&0xFFFF) & 0x0008) != 0

		// Driver state-dependent virtual I/O
		driverState := getCurrentDriverStatus(masterDev.Name)
		ioStat.FIN = driverState.isSendingFinSignal
		ioStat.SOLOP = driverState.isDriverOnOff

		statusnotifier.NotifyIOStatus(ioStat)

		// Hardware POT/NOT emergency stop — only after PDO stabilises.
		if masterDev.Device.StopWhenHWPOTNOT && pdoStabilised {
			if ioStat.NOT && !lastNOT {
				statusnotifier.Alarm("NOT Limit Exceeded")
				notifyDriverStatus("pot_not_exceeded", "NOT", masterDev)
				logger.Error("Delta hardware NOT activated")
				FastPowerOff(masterDev)
				StopJog(masterDev)
			}
			if ioStat.POT && !lastPOT {
				statusnotifier.Alarm("POT Limit Exceeded")
				notifyDriverStatus("pot_not_exceeded", "POT", masterDev)
				logger.Error("Delta hardware POT activated")
				FastPowerOff(masterDev)
				StopJog(masterDev)
			}
		}
		lastNOT = ioStat.NOT
		lastPOT = ioStat.POT

		interval := 1000 // default 1 ms
		if masterDev.Device.IOPollingInterval > 0 {
			interval = masterDev.Device.IOPollingInterval
		}
		time.Sleep(time.Duration(interval) * time.Microsecond)
	}
}

// ============================================================
// IMotorDriver implementation
// ============================================================

// SetupPDO registers all PDO entries for the Delta ASDA-A2-E.
// Phase 3: calls setupPDOPositionGeneric() — Delta has no post-processing.
func (d DeltaASDA2E) SetupPDO(dev *MasterDevice) error {
	// Phase 3: use the generic YAML-driven engine.
	// Delta has no drive-specific SDO request objects (no multiturn async SDO,
	// no profile velocity SDO request, no dig-out mask pre-assertion).
	// The generic engine handles everything — this method is a thin wrapper.
	return setupPDOPositionGeneric(dev)
}

// IsTargetReached returns true when a Profile Position move is complete.
// Delta ASDA-A2-E: bit10 alone is sufficient.
// Bit12 (Set-Point Acknowledge) clears inconsistently on this drive —
// including it caused a 30-second hang on every PP move.
func (d DeltaASDA2E) IsTargetReached(sw uint16) bool {
	const bitTargetReached = uint16(1 << 10)
	return sw&bitTargetReached != 0
}

// JogControlword applies Delta-specific bit adjustments for Profile Velocity mode.
//
// FIX (Bug 1): Ramp bits 4, 5, 6 (0x0070) were previously never OR-ed into
// cwBase. The interface comment documented them as required, but the
// implementation only cleared the Halt bit (bit 8). Without ramp bits set,
// the Delta ASDA-A2-E ignores the velocity setpoint written to 0x60FF — the
// drive either refuses to move or moves erratically during every jog command.
//
// Correct behaviour for Delta ASDA-A2-E in Profile Velocity mode:
//   - Ramp bits 4, 5, 6 HIGH (0x0070): drive accepts the velocity setpoint.
//   - Halt bit 8 LOW (clear 0x0100):   drive does not suppress motion.
func (d DeltaASDA2E) JogControlword(cwBase uint16) uint16 {
	// OR in ramp bits 4,5,6 so the drive accepts the velocity setpoint,
	// then clear Halt bit 8 so motion is not suppressed.
	return (cwBase | 0x0070) & ^uint16(0x0100)
}

// FaultResetControlword returns 0x008F for Delta ASDA-A2-E.
// Delta requires bit 7 (fault reset) AND bits 0-3 (enable bits) set simultaneously.
// Using only 0x0080 leaves the drive permanently stuck in fault state (SW bit3
// never clears) — this was the cause of false "Hardware Emergency" alarms.
// Value matches the Delta YAML faultReset operation (0x8F = 143 = 10001111b).
func (d DeltaASDA2E) FaultResetControlword() uint16 {
	return 0x008F
}

// StandbyOpMode returns Mode 3 (Profile Velocity, vel=0) for Delta standby.
//
// Mode 3 vel=0 holds position silently via the velocity loop.
// No PID integrator windup, no audible beeping, no correction-torque pulse
// on mode transitions.
func (d DeltaASDA2E) StandbyOpMode() int8 {
	return 3
}

// SupportsMultiTurnReset returns true — the Delta ASDA-A2-E supports absolute
// encoder reset via drive parameter writes P2-08=271 and P2-71=1 through CoE SDO.
// The drive must be power-cycled after the reset to take effect.
func (d DeltaASDA2E) SupportsMultiTurnReset() bool {
	return true
}

// ResetMultiTurn resets the Delta ASDA-A2-E absolute encoder.
//
// The Delta does not support the Panasonic 0x4D00/0x4D01 async SDO objects.
// Instead it uses two parameter writes via CoE SDO:
//
//	P2-08 = 271  →  unlock write protection on P2-71
//	P2-71 = 1    →  set current position as new absolute origin
//
// When PDO is active: blocking SDO writes are safe because the PDO cyclic task
// (ecrt_master_receive on every 1ms tick) services the CoE mailbox — the writes
// will be acknowledged without deadlock. The cyclic keeps running.
//
// When PDO is not active: blocking SDO writes are used directly.
//
// The drive MUST be power-cycled after this call for the reset to take effect.
func (d DeltaASDA2E) ResetMultiTurn(availableDevices []*MasterDevice) error {
	for _, device := range availableDevices {
		if device == nil {
			continue
		}
		operation, err := GetEtherCATOperation("resetMultiTurn", device.Device.AddressConfigName)
		if err != nil {
			return fmt.Errorf("[MT-DELTA] no resetMultiTurn config for %s: %w", device.Name, err)
		}

		logger.Info("[MT-DELTA] Starting absolute encoder reset for:", device.Name)

		// PHASE 1: Disable Servo and Halt Motion
		device.EnableJogPDO(false)
		device.EnablePosPDO(false)
		device.desiredTargetVelocity.Store(0)
		device.pdoMTResetActive.Store(true)

		// PHASE 2: Wait for Safe State (Servo OFF)
		logger.Info("[MT-DELTA] Waiting for drive to reach safe state (Ready to Switch On)...")
		deadline := time.Now().Add(4 * time.Second)
		safe := false
		for time.Now().Before(deadline) {
			sw := uint16(device.PDOStatus.Load() & 0xFFFF)

			// Pulse fault reset to clear any blocking alarms that might prevent writes
			if (sw & 0x0008) != 0 {
				device.pdoFaultResetActive.Store(true)
				time.Sleep(100 * time.Millisecond)
				device.pdoFaultResetActive.Store(false)
			}

			// 0x21=Ready to Switch On, 0x40=Switch On Disabled, 0x08=Fault (Servo is physically off during fault)
			state := sw & 0x006F
			if state == 0x0021 || state == 0x0040 || (sw&0x0008) != 0 {
				logger.Info(fmt.Sprintf("[MT-DELTA] Safe state reached: 0x%04X", sw))
				safe = true
				break
			}
			time.Sleep(100 * time.Millisecond)
		}

		if !safe {
			device.pdoMTResetActive.Store(false)
			return fmt.Errorf("[MT-DELTA] timeout: drive failed to reach Servo OFF state (sw=0x%04X)", uint16(device.PDOStatus.Load()&0xFFFF))
		}

		// PHASE 3: Execute SDO Writes
		// Each write uses pdoSDOExclusive to pause the PDO cyclic's
		// ecrt_master_receive, giving the blocking SDO exclusive mailbox access.
		//
		// Why this is needed for P2-71 specifically:
		//   P2-71 triggers an internal EEPROM write in the drive. The drive
		//   takes >80ms to respond (the IgH default SDO timeout). With the cyclic
		//   running (ecrt_master_receive every 1ms), the cyclic consumes the
		//   delayed mailbox response before ecrt_master_sdo_download can see it
		//   — causing EIO on every attempt. Pausing the cyclic gives the blocking
		//   call exclusive ownership of the mailbox for its entire duration.
		//
		// Why P2-08 worked without this:
		//   P2-08 is a plain register write — drive responds in ~116ms which
		//   happens to complete before enough cyclic interference builds up.
		//
		// Inter-step delay is 2000ms:
		//   P2-08 (unlock) must fully commit before P2-71 (trigger) is accepted.
		//   1000ms was not sufficient in testing. 2000ms provides a safe margin.
		const minInterStepDelay = 2000 * time.Millisecond

		for i, step := range operation.Steps {
			time.Sleep(minInterStepDelay)

			if step.Action == "read" {
				// No exclusive mode needed for reads — they are fast
				val, _ := SDOUpload2(device.Master, device.Position, step)
				logger.Debug("[MT-DELTA] read:", step.Name, "val:", val)
			} else {
				// Pause the PDO cyclic for exclusive mailbox access
				pdoSDOExclusive.Store(true)
				time.Sleep(5 * time.Millisecond) // drain any in-flight ecrt_master_receive

				sdoErr := SDODownload(device.Master, device.Position, step)

				pdoSDOExclusive.Store(false)

				if sdoErr != nil {
					logger.Warn("[MT-DELTA] write failed at step", i, ":", step.Name, sdoErr)
					device.pdoMTResetActive.Store(false)
					return sdoErr
				}
				logger.Info("[MT-DELTA] OK:", step.Name)
			}
		}

		// PHASE 4: Cleanup
		device.pdoMTResetActive.Store(false)
		logger.Info("[MT-DELTA] Reset writes complete for:", device.Name)
		logger.Info("[MT-DELTA] *** POWER CYCLE THE DRIVE to complete the absolute encoder reset ***")
	}
	return nil
}