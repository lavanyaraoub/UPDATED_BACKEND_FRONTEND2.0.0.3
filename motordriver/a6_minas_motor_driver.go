package motordriver

/**
Specialised implementation for the Panasonic MINAS A6 motor driver.

Key fixes vs the old version:
 1. ioStatusListener reads digital inputs from the PDO buffer (GetLastPDODigitalInputs)
    when PdoDIReady=true, instead of issuing an SDO read after ecrt_master_activate.
    Mixing SDO reads with a running PDO cyclic task can corrupt EtherCAT frames.
 2. isIOOn performs a bounds check on binpos before indexing the string,
    preventing a runtime panic when IntToBinary returns fewer than 8 characters.
 3. stopIOStatChan is now a slice of per-goroutine channels so stopPollIOStat()
    reliably stops ALL listener goroutines, not just one.
 4. hasTargetReached: removed unreachable `return nil` after the infinite loop.
**/

/*
#cgo CFLAGS: -g -Wall -I/opt/etherlab/include -I/home/pi/gosrc/src/EtherCAT
#cgo LDFLAGS: -L/home/pi/gosrc/src/EtherCAT -L/opt/etherlab/lib/ -lethercatinterface -lethercat
#include "ecrt.h"
#include "ethercatinterface.h"
*/
import "C"

import (
	"EtherCAT/settings"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"
	"unsafe"

	ethercatDevice "EtherCAT/ethercatdevicedatatypes"
	logger "EtherCAT/logger"
	"EtherCAT/motordriver/statusnotifier"
)

// A6Minas is the specialised implementation for the Panasonic MINAS A6 driver.
type A6Minas struct{}

// hasTargetReached polls the drive statusword (via SDO) until the target-reached
// bit is set.  This is A6-specific because the CiA-402 Target Reached bit (bit 10)
// and the Set-Point Acknowledge bit (bit 12) require a handshake sequence.
func (a6 A6Minas) hasTargetReached(masterDevice *MasterDevice, action int, immediate int, operation ethercatDevice.Operation) error {
	logger.Trace("A6Minas waiting for target reached")
	firstStep := operation.Steps[0]
	secondStep := operation.Steps[1]

	SDODownload(masterDevice.Master, masterDevice.Position, firstStep)

	for {
		result, _ := SDOUpload2(masterDevice.Master, masterDevice.Position, secondStep)
		sw := uint16(result)
		// bit 10 = Target Reached, bit 12 = Set-Point Acknowledge
		if (sw>>10)&1 == 1 && (sw>>12)&1 == 0 {
			logger.Trace("A6Minas target reached")
			return nil
		}
		if (sw>>10)&1 == 1 && (sw>>12)&1 == 1 {
			// Acknowledge the new set-point
			firstStep.Value = "0x004f"
			SDODownload(masterDevice.Master, masterDevice.Position, firstStep)
		}
	}
	// Note: the loop above only exits via return; this line is never reached
	// but is required to satisfy the Go compiler for the named return type.
}

// potNotEnabled checks whether the POT or NOT input signal is active.
// Not used in the current flow but retained for interface compatibility.
func (a6 A6Minas) potNotEnabled(masterDevice *MasterDevice) (bool, error) {
	inputStatus, err := readInputSignal(masterDevice)
	if err != nil {
		return false, err
	}
	// bit 0 = POT, bit 1 = NOT (active-high in this check)
	return (inputStatus & 0x03) != 0, nil
}

// readDeclampSignal polls until the declamp (DCL) input signal is asserted (bit 7).
func (a6 A6Minas) readDeclampSignal(masterDevice *MasterDevice, declampTiming int) (bool, error) {
	logger.Debug("waiting for declamp status")
	start := time.Now()
	for {
		inputStatus, err := readInputSignal(masterDevice)
		if err != nil {
			break
		}
		if (inputStatus & (1 << 7)) != 0 { // bit 7 = DCL high
			return true, nil
		}
		if time.Since(start).Milliseconds() > int64(declampTiming) {
			break
		}
		time.Sleep(time.Microsecond)
	}
	statusnotifier.DriverError(65379)
	return false, errors.New("declamp failed")
}

// readClampSignal polls until the clamp (CL) input signal is asserted (bit 6).
func (a6 A6Minas) readClampSignal(masterDevice *MasterDevice, clampTiming int) (bool, error) {
	logger.Debug("waiting for clamp status", clampTiming, "ms")
	start := time.Now()
	for {
		inputStatus, err := readInputSignal(masterDevice)
		if err != nil {
			break
		}
		if (inputStatus & (1 << 6)) != 0 { // bit 6 = CL high
			return true, nil
		}
		if time.Since(start).Milliseconds() > int64(clampTiming) {
			break
		}
		time.Sleep(time.Microsecond)
	}
	statusnotifier.DriverError(65378)
	return false, errors.New("clamp failed")
}

func (a6 A6Minas) receivedECS(masterDevice *MasterDevice, operation ethercatDevice.Operation, stopECSChan chan bool) int {
	for {
		select {
		case <-stopECSChan:
			logger.Debug("stopping ECS polling")
			return 2
		default:
			var inputStatus int
			var err error

			// FIX: Read instantly from memory if PDO is running, otherwise fallback to SDO
			if masterDevice.PdoDIReady {
				inputStatus = int(masterDevice.PDODI.Load())
			} else {
				inputStatus, err = readECSSignal(masterDevice)
				if err != nil {
					time.Sleep(10 * time.Millisecond)
					continue
				}
			}

			if (inputStatus & 0x01) != 0 { // bit 0 HIGH = ECS active
				return 1
			}
			time.Sleep(10 * time.Millisecond) // Relaxed polling rate to save CPU
		}
	}
}

func (a6 A6Minas) receivedECSZero(masterDevice *MasterDevice, operation ethercatDevice.Operation, stopECSChan chan bool) int {
	for {
		select {
		case <-stopECSChan:
			logger.Debug("stopping ECS zero polling")
			return 2
		default:
			var inputStatus int
			var err error

			if masterDevice.PdoDIReady {
				inputStatus = int(masterDevice.PDODI.Load())
			} else {
				inputStatus, err = readECSSignal(masterDevice)
				if err != nil {
					time.Sleep(10 * time.Millisecond)
					continue
				}
			}

			if (inputStatus & 0x01) == 0 { // bit 0 LOW = ECS de-asserted
				// Double-check stability for 100ms
				stable := true
				for i := 0; i < 5; i++ {
					time.Sleep(20 * time.Millisecond)
					var checkVal int
					if masterDevice.PdoDIReady {
						checkVal = int(masterDevice.PDODI.Load())
					} else {
						checkVal, _ = readECSSignal(masterDevice)
					}
					if (checkVal & 0x01) != 0 {
						stable = false
						break
					}
				}
				if stable {
					return 1
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func (a6 A6Minas) sendFinishSignal(masterDevice *MasterDevice, operation ethercatDevice.Operation) error {
	if len(operation.Steps) == 0 {
		return nil
	}

	// 1. Fetch the REAL memory pointer so we target the correct axis
	var realDevice *MasterDevice
	for _, dev := range getMasterDevices() {
		if dev.Name == masterDevice.Name {
			realDevice = dev
			break
		}
	}
	if realDevice == nil {
		logger.Warn("[HAL-A6] sendFinishSignal: device not found in global slice")
		return nil
	}

	// 2. Parse the value from the YAML config.
	// configs/a6minas.yml has value: "65536" (decimal string).
	// strconv.ParseUint handles both decimal and hex automatically.
	valStr := operation.Steps[0].Value
	valUint, err := strconv.ParseUint(valStr, 0, 32)
	if err != nil {
		logger.Error("[HAL-A6] Failed to parse finish signal value:", valStr, err)
		return err
	}
	val := uint32(valUint)

	// 3. Assert only the FIN bit(s). Do not replace the complete 0x60FE
	// output word: the brake uses bit 1 and must remain asserted after CL
	// feedback has confirmed that the axis is clamped.
	PDOUpdateDigitalOutputBits(realDevice, val, true)

	// Wait for the duration specified in the user's driver settings
	driverSettings := settings.GetDriverSettings(masterDevice.Name)
	timing := driverSettings.ECSFinTiming
	if timing <= 0 {
		timing = 100 // fallback 100ms
	}
	time.Sleep(time.Duration(timing) * time.Millisecond)

	// 4. Clear only the FIN bit(s), preserving the brake and unrelated outputs.
	PDOUpdateDigitalOutputBits(realDevice, val, false)

	return nil
}

// ioStopChansMu protects ioStopChans against concurrent access between
// pollIOStat (writer) and stopPollIOStat (reader+writer) which are called
// from different goroutines during reset.
var ioStopChansMu sync.Mutex

// ioStopChans holds one stop channel per ioStatusListener goroutine.
// Using a slice of per-goroutine channels ensures stopPollIOStat() stops
// ALL goroutines, not just one random receiver on a shared channel.
var ioStopChans []chan struct{}

// ioPollingWG lets stopPollIOStat confirm every ioStatusListener goroutine
// (across all three driver types, which share this package-level state) has
// actually returned, not just that its stop channel was closed.
var ioPollingWG sync.WaitGroup

// pollIOStat starts one I/O status listener goroutine per device.
func (a6 A6Minas) pollIOStat(availableDevices []*MasterDevice) {
	logger.Debug("starting A6Minas I/O status listener")
	ioStopChansMu.Lock()
	ioStopChans = make([]chan struct{}, 0, len(availableDevices))
	ioStopChansMu.Unlock()
	for _, d := range availableDevices {
		ch := make(chan struct{})
		ioStopChansMu.Lock()
		ioStopChans = append(ioStopChans, ch)
		ioStopChansMu.Unlock()
		ioPollingWG.Add(1)
		go a6.ioStatusListener(d, ch)
	}
}

// stopPollIOStat stops ALL I/O status listener goroutines and blocks until
// they have actually exited (up to a bounded timeout — see waitWithTimeout).
func (a6 A6Minas) stopPollIOStat() {
	ioStopChansMu.Lock()
	for _, ch := range ioStopChans {
		close(ch)
	}
	ioStopChans = nil
	ioStopChansMu.Unlock()
	waitWithTimeout(&ioPollingWG, 2*time.Second, "A6Minas ioStatusListener")
}

// GetCurrentAlarm returns the last alarm string sent to the UI.
func GetCurrentAlarm() string {
	return statusnotifier.GetCurrentAlarm()
}
func GetCurrentErrorCode() int {
	return statusnotifier.GetCurrentErrorCode()
}

// ioStatusListener reads digital inputs from PDO buffer and publishes I/O status.
// Startup guard: waits for sw&0x006F==0x0027 (Op Enabled) before enabling NOT/POT
// checks — prevents false alarms from zero-initialized DI register (all bits=0,
// both active-low limits appear triggered before first real PDO frame arrives).
func (a6 A6Minas) ioStatusListener(masterDev *MasterDevice, stop <-chan struct{}) {
	defer ioPollingWG.Done()
	pdoStabilised := false
	if masterDev.PdoDIReady {
		// FIX (Bug 7): Extended deadline from 2s to 10s.
		//
		// The old 2s deadline fired a "NOT/POT protection disabled" warning
		// on every startup — confirmed in the live log. The root cause is a
		// race between when ioStatusListener starts and when the CiA-402 state
		// machine has walked the drive to Operation Enabled (0x0027).
		//
		// The startup sequence is:
		//   ecrt_master_activate() → ESM PREOP→SAFEOP→OP
		//   CiA-402: Switch On Disabled (0x0040)
		//            → Ready To Switch On (0x0021)  ~50ms
		//            → Switched On (0x0023)          ~50ms
		//            → Operation Enabled (0x0027)    ~200ms
		//   ioStatusListener starts IMMEDIATELY after StartPDOCyclic returns
		//
		// Under normal conditions the drive reaches Op Enabled in ~300-600ms.
		// Under load (Pi running GC, background tasks, USB activity) the
		// scheduler can delay the 1ms cyclic task for hundreds of milliseconds,
		// pushing the total time to Op Enabled beyond 2s.
		//
		// 10s covers the absolute worst-case startup scenario. In the normal
		// case the loop exits as soon as Op Enabled is detected (~300ms) —
		// the extra headroom costs nothing.
		stabiliseDeadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(stabiliseDeadline) {
			sw := uint16(masterDev.PDOStatus.Load() & 0xFFFF)
			if sw&0x006F == 0x0027 { // Operation Enabled
				pdoStabilised = true
				logger.Info("[IO] PDO stabilised, drive in Operation Enabled — enabling NOT/POT protection for:", masterDev.Name)
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
			logger.Warn("[IO] Drive did not reach Operation Enabled within 10s — "+
				"NOT/POT hardware limit protection disabled for this session. "+
				"Check for startup faults (Error 80, cable, drive power):", masterDev.Name)
		}
	} else {
		pdoStabilised = true // SDO path has no startup race
	}

	lastPOT := false
	lastNOT := false
	if masterDev.PdoDIReady {
		initialDI := int(masterDev.PDODI.Load())
		lastNOT = (initialDI & (1 << 2)) == 0
		lastPOT = (initialDI & (1 << 1)) == 0
	}

	for {
		select {
		case <-stop:
			logger.Debug("stopping A6Minas I/O status listener for:", masterDev.Name)
			return
		default:
		}

		var inputStatus int
		var err error

		// Prefer PDO input signal register (0x4F25) when available — no EtherCAT I/O cost.
		if masterDev.PdoDIReady {
			inputStatus = int(masterDev.PDODI.Load())
		} else {
			inputStatus, err = readInputSignal(masterDev)
			if err != nil {
				logger.Error("error reading I/O input signals:", err)
				time.Sleep(time.Millisecond)
				continue
			}
		}

		var ioStat statusnotifier.IOStatus

		// Parse I/O bits directly — eliminates IntToBinary string allocation per poll cycle.
		di := inputStatus
		ioStat.ECS = (di & (1 << 0)) != 0   // bit 0 HIGH = ECS active
		ioStat.POT = (di & (1 << 1)) == 0   // bit 1 LOW  = POT active (active-low)
		ioStat.NOT = (di & (1 << 2)) == 0   // bit 2 LOW  = NOT active (active-low)
		ioStat.HOME = (di & (1 << 3)) == 0  // bit 3 LOW  = HOME active (active-low)
		ioStat.ALMIN = (di & (1 << 4)) == 0 // bit 4 LOW  = ALMIN active (active-low)
		ioStat.CL = (di & (1 << 6)) != 0    // bit 6 HIGH = Clamp
		ioStat.DCL = (di & (1 << 7)) != 0   // bit 7 HIGH = Declamp
		// ALMOUT: true when the drive has an active alarm/fault output.
		// Derived from the drive fault bit (statusword bit 3) so it stays
		// accurate across all alarm sources without a separate hardware input.
		ioStat.ALMOUT = (uint16(masterDev.PDOStatus.Load()&0xFFFF) & 0x0008) != 0

		// ── Bit 5: Hardware RESET input ────────────────────────────────────
		if pdoStabilised && (di&(1<<5)) != 0 {
			logger.Info("[IO] Hardware reset input detected (bit 5 high) — triggering system reset for:", masterDev.Name)
			go performSysReset(true)
		}

		// Driver state-dependent virtual I/O
		driverState := getCurrentDriverStatus(masterDev.Name)
		ioStat.FIN = driverState.isSendingFinSignal
		ioStat.SOLOP = driverState.isDriverOnOff

		statusnotifier.NotifyIOStatus(ioStat)

		// ================================================================
		// POT/NOT HARDWARE EMERGENCY STOP
		// Only active after pdoStabilised=true (drive reached Operation
		// Enabled). This prevents false alarms from the zero-initialized
		// DI register before the drive enters OP state.
		// ================================================================
		if masterDev.Device.StopWhenHWPOTNOT && pdoStabilised {
			if ioStat.NOT && !lastNOT {
				statusnotifier.Alarm("NOT Limit Exceeded")
				notifyDriverStatus("pot_not_exceeded", "NOT", masterDev)
				logger.Error("hardware NOT activated")
				FastPowerOff(masterDev)
				StopJog(masterDev)
			}
			if ioStat.POT && !lastPOT {
				statusnotifier.Alarm("POT Limit Exceeded")
				notifyDriverStatus("pot_not_exceeded", "POT", masterDev)
				logger.Error("hardware POT activated")
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

// SetupPDO registers all PDO entries for the Panasonic MINAS A6.
// Delegates to setupPDOPositionA6 in pdo_setup.go.
func (a6 A6Minas) SetupPDO(dev *MasterDevice) error {
	// Phase 3: use the generic YAML-driven engine.
	if err := setupPDOPositionGeneric(dev); err != nil {
		return err
	}

	// Panasonic MINAS A6/BF PP handshake is more reliable when the app can
	// verify the drive's active operation mode through 0x6061 before watching
	// bit10/bit12 transitions.
	if !dev.PdoOpModeDisplayReady {
		return fmt.Errorf("A6Minas.SetupPDO: 0x6061 Modes of operation display must be mapped as op_mode_disp")
	}
	logger.Info("[PDO] A6: 0x6061 mode display mapped — PP handshake verification enabled")

	// ── A6-specific post-processing ──────────────────────────────────────
	// These cannot be expressed in YAML because they involve IgH API calls
	// that are not part of the PDO domain registration (SDO request objects,
	// pre-asserted digital output mask). They run after the generic engine
	// has set up the domain and populated all Off* fields.

	sc := dev.SlaveConfig // set by setupPDOPositionGeneric
	if sc == nil {
		return fmt.Errorf("A6Minas.SetupPDO: SlaveConfig is nil after generic setup")
	}

	// Async SDO request objects for multiturn reset (0x4D01:00, 0x4D00:01).
	// Must be created before ecrt_master_activate() and after slave config.
	if rc := C.create_mt_sdo_requests(sc, C.int(dev.Position)); rc == 0 {
		dev.MTSdoReady = true
		fmt.Println("[PDO] A6: Multiturn SDO request objects registered")
	} else {
		dev.MTSdoReady = false
		dev.PdoMTReady = false
		fmt.Printf("[WARN] A6: create_mt_sdo_requests failed rc=%d\n", int(rc))
	}

	// Async SDO request for Profile Velocity (0x6081).
	reqPtr := C.create_profile_vel_sdo_request(sc)
	if reqPtr != nil {
		dev.PdoVelSdoReq = reqPtr
		dev.PdoVelSdoReady = true
		fmt.Println("[PDO] A6: Profile Velocity SDO request registered (0x6081)")
	} else {
		dev.PdoVelSdoReady = false
		fmt.Printf("[WARN] A6: create_profile_vel_sdo_request failed\n")
	}

	// Pre-assert the EtherCAT ownership mask (0x60FE:02) for ALL output bits.
	// The A6 ignores 0x60FE:01 (output values) unless 0x60FE:02 has the
	// corresponding bits set. We own all bits so any output write takes effect.
	if dev.PdoDigOutReady {
		dev.desiredDigOutVal.Store(0xFFFFFFFF)  // 0x60FE:02: EtherCAT owns all bits
		dev.desiredDigOutMask.Store(0x00000000) // 0x60FE:01: all outputs LOW initially
	}

	dev.PdoMTReady = false
	return nil
}

// IsTargetReached returns true when a Profile Position move is complete.
// Panasonic A6 requires the strict CiA-402 handshake:
//   - bit10 (Target Reached) must be HIGH
//   - bit12 (Set-Point Acknowledge) must be LOW
func (a6 A6Minas) IsTargetReached(sw uint16) bool {
	const bitTargetReached = uint16(1 << 10)
	const bitSetPointAck = uint16(1 << 12)
	return (sw&bitTargetReached != 0) && (sw&bitSetPointAck == 0)
}

// JogControlword returns the controlword for Profile Velocity (jog) mode.
// Panasonic A6 does not require ramp-bit fixup — return cwBase unchanged.
func (a6 A6Minas) JogControlword(cwBase uint16) uint16 {
	return cwBase
}

// FaultResetControlword returns 0x0080 for Panasonic A6.
// Standard CiA-402: bit 7 alone triggers the fault reset rising edge.
func (a6 A6Minas) FaultResetControlword() uint16 {
	return 0x0080
}

// StandbyOpMode returns Mode 1 (Profile Position) for Panasonic A6 standby.
//
// PP is the primary mode for RTC indexing. Keeping the drive in Mode 1 while
// idle avoids unnecessary Mode 3 <-> Mode 1 transitions between consecutive
// position commands. The cyclic standby branch keeps bit4 LOW and holds the
// current actual position, so no new PP set-point is triggered while idle.
func (a6 A6Minas) StandbyOpMode() int8 {
	return 1
}

// SupportsMultiTurnReset returns true — the Panasonic A6 supports multi-turn
// encoder reset via async SDO objects 0x4D01:00 and 0x4D00:01.
func (a6 A6Minas) SupportsMultiTurnReset() bool {
	return true
}

// ResetMultiTurn executes the Panasonic A6 multi-turn encoder reset.
//
// When PDO is active: uses BLOCKING SDO writes with SDOExclusive (same pattern
// as Delta's P2-71 reset) rather than the async ec_sdo_request_t path.
//
// WHY BLOCKING INSTEAD OF ASYNC:
//
//	The async SDO request for 0x4D00:01 was created in ethercatinterface.c with
//	size=2 (UINT16). The A6 object dictionary defines 0x4D00:01 as UINT32 (4 bytes).
//	This size mismatch causes the drive to return an SDO abort code (EC_REQUEST_ERROR)
//	on every trigger attempt — regardless of inter-step delays or retry counts.
//	Fixing this in the .so requires recompiling libethercatinterface.so separately.
//
//	Using ecrt_master_sdo_download (blocking) instead lets us pass the exact buffer
//	size per call (2 bytes for 0x4D01:00, 4 bytes for 0x4D00:01) with no .so changes.
//	SDOExclusive pauses the cyclic task's ecrt_master_receive so the blocking write
//	has exclusive CoE mailbox access — the same mechanism Delta uses for P2-71.
//
// When PDO is NOT active: uses the YAML-driven blocking SDO path (triggerMultiTurnResetSDO).
//
// The drive MUST be power-cycled after this call for the reset to take effect.
func (a6 A6Minas) ResetMultiTurn(availableDevices []*MasterDevice) error {
	if IsPDOActive() {
		return a6ResetMultiTurnPDO(availableDevices)
	}
	return triggerMultiTurnResetSDO(availableDevices)
}

// a6ResetMultiTurnPDO performs the A6 multi-turn reset via blocking SDO while
// the PDO cyclic is running. Uses SDOExclusive to pause ecrt_master_receive
// per-step so the blocking ecrt_master_sdo_download has exclusive mailbox access.
func a6ResetMultiTurnPDO(availableDevices []*MasterDevice) error {
	const (
		servoOffTimeout = 3 * time.Second
		interStepDelay  = 300 * time.Millisecond // A6 needs settle time between SDO steps
		sdoDrainDelay   = 5 * time.Millisecond   // let any in-flight ecrt_master_receive complete
		pollInterval    = 10 * time.Millisecond
	)

	for _, d := range availableDevices {
		if d == nil {
			continue
		}

		// Error 40 (absolute-system-down) needs one extra recovery stage after
		// the absolute encoder clear: a CiA-402 fault reset followed by positive
		// hardware verification. A normal multiturn reset must retain its
		// existing behaviour, so snapshot the reason before the reset and branch
		// only for Error 40.
		wasError40 := normalizeErrorCode(int(d.PDOErr.Load()&0xFFFF)) == 40

		logger.Info("[MT-PDO] Starting A6 multi-turn reset (blocking SDO) for:", d.Name)

		// ── Phase 1: Disable servo ────────────────────────────────────────────
		// pdoMTResetActive makes the cyclic standby branch write CW=0x0006,
		// walking the drive from Op Enabled → Ready To Switch On (servo OFF).
		d.EnableJogPDO(false)
		d.EnablePosPDO(false)
		d.desiredTargetVelocity.Store(0)
		_ = d.SetTargetPositionPDO(d.PDOPos.Load())
		d.pdoMTResetActive.Store(true)

		deadline := time.Now().Add(servoOffTimeout)
		for {
			rawSW := uint16(d.PDOStatus.Load() & 0xFFFF)
			sw := rawSW & 0x006F
			// A normal coordinate-zero reset must wait for Ready To Switch On
			// (0x0021), where the power stage is fully de-energized. Error 40
			// already places the drive in Fault with the servo power removed, so
			// that fault state is also safe for the encoder-clear SDO sequence.
			//
			// Switched On (0x0023) is NOT sufficient. A6 returns SDO abort code
			// 0x08000022 ("data cannot be transferred in present device state") when
			// triggered at 0x0023 — the drive's power stage is still partially active.
			//
			// CW=0x0006 (Shutdown) transitions: Op Enabled → Ready To Switch On.
			// The drive may pass briefly through 0x0023 during this transition.
			// Accepting 0x0023 fired the SDO too early — hence the abort.
			if sw == 0x0021 || (wasError40 && rawSW&0x0008 != 0) {
				break
			}
			if time.Now().After(deadline) {
				d.pdoMTResetActive.Store(false)
				return fmt.Errorf("[MT-PDO] timeout waiting for Ready To Switch On sw=0x%04X",
					uint16(d.PDOStatus.Load()))
			}
			time.Sleep(pollInterval)
		}
		// Add stabilization time after reaching Ready To Switch On.
		// The A6 power stage may need additional de-energization time beyond
		// the state machine transition before it accepts 0x4D00:01 writes.
		time.Sleep(200 * time.Millisecond)
		logger.Info("[MT-PDO] Servo OFF (Ready To Switch On) —",
			fmt.Sprintf("sw=0x%04X", uint16(d.PDOStatus.Load())),
			"— executing SDO sequence")

		// ── Phase 2: Three blocking SDO writes ───────────────────────────────
		//
		// Object sizes (from A6 MINAS object dictionary):
		//   0x4D01:00 — UINT16 (2 bytes) — function selection register
		//   0x4D00:01 — UINT32 (4 bytes) — special function trigger register
		//
		// Sequence:
		//   Step 0: select multi-turn clear (0x4D01:00 = 0x0031)
		//   Step 1: trigger reset           (0x4D00:01 bit9 HIGH = 0x0200)
		//   Step 2: clear trigger           (0x4D00:01 = 0x0000)
		type mtStep struct {
			name string
			addr uint16
			sub  uint8
			val  uint32
			size C.size_t // 2 = U16, 4 = U32
		}
		seq := []mtStep{
			{"0x4D01:00=0x0031 (func select)", 0x4D01, 0x00, 0x0031, 2},
			{"0x4D00:01=0x0200 (trigger)", 0x4D00, 0x01, 0x0200, 4},
			{"0x4D00:01=0x0000 (clear)", 0x4D00, 0x01, 0x0000, 4},
		}

		for i, s := range seq {
			// Give drive settle time before each step.
			// Without this the A6 rejects the trigger even after function select ACK.
			time.Sleep(interStepDelay)

			// Pause the cyclic task — gives this goroutine exclusive mailbox access.
			d.SDOExclusive.Store(true)
			time.Sleep(sdoDrainDelay) // drain any ecrt_master_receive already in flight

			var abortCode C.uint32_t
			val := C.uint32_t(s.val) // holds the value; sdo_download reads s.size bytes from it
			rc := C.sdo_download(
				d.Master,
				C.uint16_t(d.Position),
				C.uint16_t(s.addr),
				C.uint8_t(s.sub),
				(*C.uint8_t)(unsafe.Pointer(&val)),
				s.size,
				&abortCode,
			)
			d.SDOExclusive.Store(false)

			if rc < 0 {
				logger.Error(fmt.Sprintf(
					"[MT-PDO] step %d FAILED: %s rc=%d abortCode=0x%08X",
					i, s.name, int(rc), uint32(abortCode)))
				d.pdoMTResetActive.Store(false)
				return fmt.Errorf("[MT-PDO] step %d failed: %s rc=%d abortCode=0x%08X",
					i, s.name, int(rc), uint32(abortCode))
			}
			logger.Info(fmt.Sprintf("[MT-PDO] step %d OK: %s", i, s.name))
		}

		// ── Phase 3: Re-enable servo ─────────────────────────────────────────
		d.pdoMTResetActive.Store(false)

		if wasError40 {
			if err := recoverA6Error40AfterMultiTurn(d); err != nil {
				return err
			}
			logger.Info("[MT-PDO] Multi-turn reset complete; Error 40 cleared and drive restored:",
				d.Name,
				fmt.Sprintf("sw=0x%04X", uint16(d.PDOStatus.Load()&0xFFFF)),
				"error=", uint16(d.PDOErr.Load()&0xFFFF),
				"pos=", d.PDOPos.Load())
			continue
		}

		logger.Info("[MT-PDO] Multi-turn reset complete for:", d.Name,
			fmt.Sprintf("sw=0x%04X", uint16(d.PDOStatus.Load())),
			"— *** POWER CYCLE THE DRIVE to take effect ***")
	}
	return nil
}

// recoverA6Error40AfterMultiTurn completes the Error 40 recovery after the
// Panasonic absolute-encoder clear sequence has succeeded.
//
// Success is never inferred from a software cache write. The function waits
// for fresh PDO feedback and requires all of the following to remain true for
// a stability window:
//   - 0x603F error code is zero
//   - statusword fault bit (bit 3) is clear
//   - CiA-402 state is Operation Enabled (sw & 0x006F == 0x0027)
func recoverA6Error40AfterMultiTurn(d *MasterDevice) error {
	const (
		encoderClearSettle = 500 * time.Millisecond
		recoveryTimeout    = 6 * time.Second
		stableWindow       = 250 * time.Millisecond
		pollInterval       = 20 * time.Millisecond
	)

	if d == nil {
		return errors.New("[MT-PDO] Error 40 recovery called with nil device")
	}

	time.Sleep(encoderClearSettle)

	logger.Info("[MT-PDO] Error 40 detected before multiturn reset; requesting CiA-402 fault reset for:", d.Name)
	if !PDOFaultReset([]*MasterDevice{d}) {
		return fmt.Errorf("[MT-PDO] encoder clear completed but CiA-402 fault reset failed for %s: sw=0x%04X error=%d",
			d.Name,
			uint16(d.PDOStatus.Load()&0xFFFF),
			normalizeErrorCode(int(d.PDOErr.Load()&0xFFFF)))
	}

	deadline := time.Now().Add(recoveryTimeout)
	var stableFrom time.Time

	for time.Now().Before(deadline) {
		sw := uint16(d.PDOStatus.Load() & 0xFFFF)
		errCode := normalizeErrorCode(int(d.PDOErr.Load() & 0xFFFF))

		faultClear := sw&0x0008 == 0
		operationEnabled := sw&0x006F == 0x0027
		errorClear := errCode == 0

		if faultClear && operationEnabled && errorClear {
			if stableFrom.IsZero() {
				stableFrom = time.Now()
			} else if time.Since(stableFrom) >= stableWindow {
				return nil
			}
		} else {
			stableFrom = time.Time{}
		}

		time.Sleep(pollInterval)
	}

	return fmt.Errorf("[MT-PDO] Error 40 recovery incomplete for %s: sw=0x%04X error=%d; check encoder connection and power-cycle the drive",
		d.Name,
		uint16(d.PDOStatus.Load()&0xFFFF),
		normalizeErrorCode(int(d.PDOErr.Load()&0xFFFF)))
}
