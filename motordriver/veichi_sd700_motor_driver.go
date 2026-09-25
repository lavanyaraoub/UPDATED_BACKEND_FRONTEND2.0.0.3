package motordriver

/*
Veichi SD700 IMotorDriver implementation.

The Veichi SD700 is structurally very similar to the Delta ASDA-A2-E:
  - CiA-402 compliant servo drive (Profile 402)
  - Standard 0x60FD digital inputs: POT (bit 1) and NOT (bit 0), active-HIGH
  - No vendor-specific clamp/declamp or ECS signals
  - No multiturn reset via standard CiA-402 objects
  - JogControlword: same ramp-bit fix as Delta (bits 4,5,6 must be set)
  - No dig-out PDO in the layout used here — sendFinishSignal is a no-op

Key differences from Delta ASDA-A2-E:
  - PDO layout uses 0x1601 (RxPdo) / 0x1a01 (TxPdo) — no dig_out_val (0x60FE)
  - No error_code (0x603F) in the default TxPdo mapping — read via SDO only
  - VendorID: 0x00850104  ProductCode: 0x01030507

If the Veichi SD700 behaves differently in practice (e.g. different JogControlword
bits, different target-reached logic), update the methods below and this comment.
*/

import (
	ethercatDevice "EtherCAT/ethercatdevicedatatypes"
	logger "EtherCAT/logger"
	"EtherCAT/motordriver/statusnotifier"
	"sync"
	"time"
)

// VeichiSD700 is the IMotorDriver implementation for the Veichi SD700 EtherCAT drive.
type VeichiSD700 struct{}

// ============================================================
// Legacy SDO-based methods (retained for interface compatibility)
// ============================================================

// hasTargetReached polls the PDO statusword bit 10 (Target Reached).
// The Veichi SD700 follows CiA-402 — bit 10 alone is sufficient, same as Delta.
func (d VeichiSD700) hasTargetReached(masterDevice *MasterDevice, action int, immediate int, operation ethercatDevice.Operation) error {
	logger.Trace("VeichiSD700 waiting for target reached")
	firstStep := operation.Steps[0]
	secondStep := operation.Steps[1]

	SDODownload(masterDevice.Master, masterDevice.Position, firstStep)

	for {
		result, _ := SDOUpload2(masterDevice.Master, masterDevice.Position, secondStep)
		sw := uint16(result)
		if (sw>>10)&1 == 1 {
			logger.Trace("VeichiSD700 target reached")
			return nil
		}
	}
}

// potNotEnabled returns true if either POT (bit 1) or NOT (bit 0) of 0x60FD is
// asserted. The Veichi SD700 uses the standard CiA-402 active-HIGH convention,
// identical to the Delta ASDA-A2-E.
//
//	bit 0 = NOT (Negative Over-Travel): 1 = limit switch triggered
//	bit 1 = POT (Positive Over-Travel): 1 = limit switch triggered
//
// NOTE: the PDO layout in veichi_sd700.yml (TxPdo 0x1a01) does not include
// 0x60FD, so PDODI will always read 0 unless the layout is switched to 0x1a00
// (see VEICHI_INTEGRATION.md). This method is still correct either way.
func (d VeichiSD700) potNotEnabled(masterDevice *MasterDevice) (bool, error) {
	di := uint32(masterDevice.PDODI.Load())
	not := (di & (1 << 0)) != 0 // bit 0: Negative limit switch (active-HIGH)
	pot := (di & (1 << 1)) != 0 // bit 1: Positive limit switch (active-HIGH)
	return pot || not, nil
}

// readDeclampSignal is a no-op for Veichi SD700 — no declamp output in PDO.
// Returns true immediately so callers do not block.
func (d VeichiSD700) readDeclampSignal(masterDevice *MasterDevice, declampTiming int) (bool, error) {
	return true, nil
}

// readClampSignal is a no-op for Veichi SD700 — no clamp output in PDO.
// Returns true immediately so callers do not block.
func (d VeichiSD700) readClampSignal(masterDevice *MasterDevice, clampTiming int) (bool, error) {
	return true, nil
}

// receivedECS is a no-op for Veichi SD700 — no ECS input in 0x60FD.
// Returns 1 (signal received) immediately so callers do not block.
func (d VeichiSD700) receivedECS(masterDevice *MasterDevice, operation ethercatDevice.Operation, stopECSChat chan bool) int {
	return 1
}

// receivedECSZero is a no-op for Veichi SD700.
// Returns 1 immediately so callers do not block.
func (d VeichiSD700) receivedECSZero(masterDevice *MasterDevice, operation ethercatDevice.Operation, stopECSChat chan bool) int {
	return 1
}

// sendFinishSignal is a no-op for Veichi SD700 — the PDO layout used here has
// no digital-output entry (0x60FE). Returns nil immediately so callers are not
// interrupted, matching Delta's non-critical/graceful behaviour.
func (d VeichiSD700) sendFinishSignal(masterDevice *MasterDevice, operation ethercatDevice.Operation) error {
	logger.Debug("[HAL-Veichi] sendFinishSignal: no dig-out PDO mapped — skipping (non-critical)")
	return nil
}

// veichiIOStopChansMu protects veichiIOStopChans.
var veichiIOStopChansMu sync.Mutex

// veichiIOStopChans holds one stop channel per veichiIOStatusListener goroutine.
var veichiIOStopChans []chan struct{}

// pollIOStat starts one I/O status listener goroutine per device.
func (d VeichiSD700) pollIOStat(availableDevices []*MasterDevice) {
	logger.Debug("starting VeichiSD700 I/O status listener")
	veichiIOStopChansMu.Lock()
	veichiIOStopChans = make([]chan struct{}, 0, len(availableDevices))
	veichiIOStopChansMu.Unlock()
	for _, dev := range availableDevices {
		ch := make(chan struct{})
		veichiIOStopChansMu.Lock()
		veichiIOStopChans = append(veichiIOStopChans, ch)
		veichiIOStopChansMu.Unlock()
		ioPollingWG.Add(1)
		go d.veichiIOStatusListener(dev, ch)
	}
}

// stopPollIOStat stops ALL Veichi I/O status listener goroutines and blocks
// until they have actually exited (up to a bounded timeout).
func (d VeichiSD700) stopPollIOStat() {
	veichiIOStopChansMu.Lock()
	for _, ch := range veichiIOStopChans {
		close(ch)
	}
	veichiIOStopChans = nil
	veichiIOStopChansMu.Unlock()
	waitWithTimeout(&ioPollingWG, 2*time.Second, "VeichiSD700 ioStatusListener")
}

// veichiIOStatusListener reads 0x60FD from the PDO buffer and publishes I/O status.
// Structurally identical to deltaIOStatusListener — same CiA-402 bit layout.
//
// Startup guard: waits for sw&0x006F==0x0027 (Operation Enabled) before enabling
// POT/NOT hardware checks, up to 10s, to avoid false alarms during the CiA-402
// state machine walk to Op Enabled.
func (d VeichiSD700) veichiIOStatusListener(masterDev *MasterDevice, stop <-chan struct{}) {
	defer ioPollingWG.Done()
	pdoStabilised := false
	if masterDev.PdoDIReady {
		stabiliseDeadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(stabiliseDeadline) {
			sw := uint16(masterDev.PDOStatus.Load() & 0xFFFF)
			if sw&0x006F == 0x0027 { // Operation Enabled
				pdoStabilised = true
				logger.Info("[IO] Veichi SD700 PDO stabilised, drive in Operation Enabled — enabling NOT/POT protection for:", masterDev.Name)
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
			logger.Warn("[IO] Veichi SD700 drive did not reach Operation Enabled within 10s — " +
				"NOT/POT hardware limit protection disabled for this session. " +
				"Check for startup faults (cable, drive power):" + masterDev.Name)
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
			logger.Debug("stopping VeichiSD700 I/O status listener for:", masterDev.Name)
			return
		default:
		}

		// Read 0x60FD from the PDO buffer (updated every 1ms by the cyclic task).
		// Veichi SD700 CiA-402 active-HIGH convention:
		//   bit 0 = NOT (Negative Over-Travel): 1 = limit active
		//   bit 1 = POT (Positive Over-Travel): 1 = limit active
		//   bit 2 = HOME switch:                1 = home active
		di := uint32(masterDev.PDODI.Load())

		var ioStat statusnotifier.IOStatus

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
				logger.Error("Veichi SD700 hardware NOT activated")
				FastPowerOff(masterDev)
				StopJog(masterDev)
			}
			if ioStat.POT && !lastPOT {
				statusnotifier.Alarm("POT Limit Exceeded")
				notifyDriverStatus("pot_not_exceeded", "POT", masterDev)
				logger.Error("Veichi SD700 hardware POT activated")
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
// PDO setup
// ============================================================

// SetupPDO registers all PDO entries for the Veichi SD700.
// Uses the generic YAML-driven engine — no drive-specific SDO objects needed.
func (d VeichiSD700) SetupPDO(dev *MasterDevice) error {
	return setupPDOPositionGeneric(dev)
}

// ============================================================
// PP target-reached logic
// ============================================================

// IsTargetReached returns true when bit 10 (Target Reached) of the statusword is set.
// The Veichi SD700 follows the CiA-402 standard — bit 10 alone is sufficient,
// same as Delta ASDA-A2-E.
func (d VeichiSD700) IsTargetReached(sw uint16) bool {
	const bitTargetReached = uint16(1 << 10)
	return sw&bitTargetReached != 0
}

// ============================================================
// Jog controlword fixup
// ============================================================

// JogControlword applies Veichi SD700-specific bit adjustments for Profile Velocity mode.
//
// Same fix as Delta ASDA-A2-E: ramp bits 4, 5, 6 (0x0070) must be set for the
// drive to accept the velocity setpoint in Profile Velocity mode (0x6060=0x03).
// Without them, the drive ignores 0x60FF and the motor does not move.
//
// If jog behaves differently on the Veichi (e.g. moves erratically), check
// whether 0x0070 is correct for this drive or if only the Halt bit (0x0100)
// needs to be cleared.
func (d VeichiSD700) JogControlword(cwBase uint16) uint16 {
	// OR in ramp bits 4,5,6 — drive accepts the velocity setpoint.
	// Clear Halt bit 8 — motion is not suppressed.
	return (cwBase | 0x0070) & ^uint16(0x0100)
}

// ============================================================
// Fault reset controlword
// ============================================================

// FaultResetControlword returns 0x008F for Veichi SD700.
// Bit 7 (fault reset) + bits 0-3 (enable bits) set simultaneously,
// matching the faultReset YAML operation (0x8F = 10001111b).
// Adjust if the Veichi requires a different sequence (e.g. 0x0080 then 0x000F).
func (d VeichiSD700) FaultResetControlword() uint16 {
	return 0x008F
}

// ============================================================
// Standby operation mode
// ============================================================

// StandbyOpMode returns Mode 3 (Profile Velocity, vel=0) for Veichi SD700 standby.
// This holds position silently without PID windup or audible beeping.
// If the Veichi does not support Mode 3, change to 8 (CSP) and set target_pos
// to current position instead.
func (d VeichiSD700) StandbyOpMode() int8 {
	return 3
}

// ============================================================
// Multi-turn encoder reset
// ============================================================

// ResetMultiTurn is a no-op for Veichi SD700 — the drive has no standard
// CiA-402 multiturn reset mechanism. Returns ErrNotSupported immediately.
// If Veichi adds a vendor-specific reset in a future firmware, implement it here.
func (d VeichiSD700) ResetMultiTurn(availableDevices []*MasterDevice) error {
	logger.Warn("[MT-VEICHI] ResetMultiTurn called but Veichi SD700 does not support multiturn reset")
	return ErrNotSupported
}

// init registers the VeichiSD700 driver in the factory at package startup.
// No changes to motor_driver_factory.go are needed — RegisterDriver handles it.
//
// The drive-type string "veichi_sd700" must match what IdentifyDrive() returns
// in auto_discovery.go.
func init() {
	RegisterDriver("veichi_sd700", func() IMotorDriver { return &VeichiSD700{} })
}

// Compile-time check: VeichiSD700 must implement IMotorDriver.
var _ IMotorDriver = VeichiSD700{}