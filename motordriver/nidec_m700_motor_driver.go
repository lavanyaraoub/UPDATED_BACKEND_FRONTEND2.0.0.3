package motordriver

/*
Nidec / Control Techniques M700 IMotorDriver implementation.

Features:
 1. Fully compliant with standard CiA-402.
 2. Digital inputs (POT, NOT, HOME) read from standard 0x60FD (active-HIGH).
 3. Digital outputs (FINISH SIGNAL) mapped to 0x60FE:01 Bit 16.
 4. Multi-turn reset supported via standard SDO operations (configurable via YAML).
*/

import (
	ethercatDevice "EtherCAT/ethercatdevicedatatypes"
	logger "EtherCAT/logger"
	"EtherCAT/motordriver/statusnotifier"
	settings "EtherCAT/settings"
	"fmt"
	"sync"
	"time"
)

// NidecM700 is the IMotorDriver implementation for the Nidec M700 drive.
type NidecM700 struct{}

// hasTargetReached polls the SDO statusword bit 10 (Target Reached).
// Used only pre-activation; post-activation uses the PDO path.
func (n NidecM700) hasTargetReached(masterDevice *MasterDevice, action int, immediate int, operation ethercatDevice.Operation) error {
	logger.Trace("NidecM700 waiting for target reached")
	if len(operation.Steps) < 2 {
		return fmt.Errorf("NidecM700.hasTargetReached requires 2 steps in operation config")
	}
	firstStep := operation.Steps[0]
	secondStep := operation.Steps[1]

	SDODownload(masterDevice.Master, masterDevice.Position, firstStep)

	for {
		result, _ := SDOUpload2(masterDevice.Master, masterDevice.Position, secondStep)
		sw := uint16(result)
		if (sw>>10)&1 == 1 {
			logger.Trace("NidecM700 target reached")
			return nil
		}
		time.Sleep(1 * time.Millisecond)
	}
}

// potNotEnabled returns true if either POT (bit 1) or NOT (bit 0) of 0x60FD is asserted.
func (n NidecM700) potNotEnabled(masterDevice *MasterDevice) (bool, error) {
	di := uint32(masterDevice.PDODI.Load()) // per-device read
	not := (di & (1 << 0)) != 0             // bit 0: Negative limit switch
	pot := (di & (1 << 1)) != 0             // bit 1: Positive limit switch
	return pot || not, nil
}

// readDeclampSignal is a no-op for Nidec standard configuration.
func (n NidecM700) readDeclampSignal(masterDevice *MasterDevice, declampTiming int) (bool, error) {
	return true, nil
}

// readClampSignal is a no-op for Nidec standard configuration.
func (n NidecM700) readClampSignal(masterDevice *MasterDevice, clampTiming int) (bool, error) {
	return true, nil
}

// receivedECS is a no-op for Nidec standard configuration.
func (n NidecM700) receivedECS(masterDevice *MasterDevice, operation ethercatDevice.Operation, stopECSChat chan bool) int {
	return 1
}

// receivedECSZero is a no-op for Nidec standard configuration.
func (n NidecM700) receivedECSZero(masterDevice *MasterDevice, operation ethercatDevice.Operation, stopECSChat chan bool) int {
	return 1
}

// sendFinishSignal toggles the Nidec digital output for the finish signal.
// Uses Bit 16 (0x00010000) of 0x60FE:01 which corresponds to Digital Output 1.
func (n NidecM700) sendFinishSignal(masterDevice *MasterDevice, operation ethercatDevice.Operation) error {
	if !masterDevice.PdoDigOutReady {
		logger.Warn("[HAL-Nidec] sendFinishSignal: PdoDigOutReady=false — skipping finish signal")
		return nil
	}

	var realDevice *MasterDevice
	for _, dev := range getMasterDevices() {
		if dev.Name == masterDevice.Name {
			realDevice = dev
			break
		}
	}
	if realDevice == nil {
		logger.Warn("[HAL-Nidec] sendFinishSignal: device not found in global slice")
		return nil
	}

	driverSettings := settings.GetDriverSettings(masterDevice.Name)

	// Assert Output (Set Bit 16 HIGH)
	PDOSetDigitalOutput([]*MasterDevice{realDevice}, 0, 0x00010000)

	// Wait
	time.Sleep(time.Duration(driverSettings.ECSFinTiming) * time.Millisecond)

	// Release Output (Set Bit 16 LOW)
	PDOSetDigitalOutput([]*MasterDevice{realDevice}, 0, 0)

	return nil
}

// nidecIOStopChansMu protects the stop channels slice.
var nidecIOStopChansMu sync.Mutex
var nidecIOStopChans []chan struct{}

// pollIOStat starts one I/O status listener goroutine per device.
func (n NidecM700) pollIOStat(availableDevices []*MasterDevice) {
	logger.Debug("starting NidecM700 I/O status listener")
	nidecIOStopChansMu.Lock()
	nidecIOStopChans = make([]chan struct{}, 0, len(availableDevices))
	nidecIOStopChansMu.Unlock()

	for _, dev := range availableDevices {
		ch := make(chan struct{})
		nidecIOStopChansMu.Lock()
		nidecIOStopChans = append(nidecIOStopChans, ch)
		nidecIOStopChansMu.Unlock()
		ioPollingWG.Add(1)
		go n.nidecIOStatusListener(dev, ch)
	}
}

// stopPollIOStat stops ALL Nidec I/O status listener goroutines and blocks
// until they have actually exited (up to a bounded timeout).
func (n NidecM700) stopPollIOStat() {
	nidecIOStopChansMu.Lock()
	for _, ch := range nidecIOStopChans {
		close(ch)
	}
	nidecIOStopChans = nil
	nidecIOStopChansMu.Unlock()
	waitWithTimeout(&ioPollingWG, 2*time.Second, "NidecM700 ioStatusListener")
}

// nidecIOStatusListener reads 0x60FD from the PDO buffer and publishes I/O status.
func (n NidecM700) nidecIOStatusListener(masterDev *MasterDevice, stop <-chan struct{}) {
	defer ioPollingWG.Done()
	pdoStabilised := false
	if masterDev.PdoDIReady {
		stabiliseDeadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(stabiliseDeadline) {
			sw := uint16(masterDev.PDOStatus.Load() & 0xFFFF)
			if sw&0x006F == 0x0027 { // Operation Enabled
				pdoStabilised = true
				logger.Info("[IO] Nidec PDO stabilised, drive in Operation Enabled — enabling NOT/POT protection for:", masterDev.Name)
				break
			}
			select {
			case <-stop:
				return
			default:
				time.Sleep(20 * time.Millisecond)
			}
		}
	} else {
		pdoStabilised = true
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
			logger.Debug("stopping NidecM700 I/O status listener for:", masterDev.Name)
			return
		default:
		}

		di := uint32(masterDev.PDODI.Load()) // per-device read

		var ioStat statusnotifier.IOStatus

		// CiA-402 standard active-HIGH limit switches
		ioStat.NOT = (di & (1 << 0)) != 0
		ioStat.POT = (di & (1 << 1)) != 0
		ioStat.HOME = (di & (1 << 2)) != 0
		// ALMOUT: drive fault bit (statusword bit 3)
		ioStat.ALMOUT = (uint16(masterDev.PDOStatus.Load()&0xFFFF) & 0x0008) != 0

		driverState := getCurrentDriverStatus(masterDev.Name)
		ioStat.FIN = driverState.isSendingFinSignal
		ioStat.SOLOP = driverState.isDriverOnOff

		statusnotifier.NotifyIOStatus(ioStat)

		if masterDev.Device.StopWhenHWPOTNOT && pdoStabilised {
			if ioStat.NOT && !lastNOT {
				statusnotifier.Alarm("NOT Limit Exceeded")
				notifyDriverStatus("pot_not_exceeded", "NOT", masterDev)
				logger.Error("Nidec hardware NOT activated")
				FastPowerOff(masterDev)
				StopJog(masterDev)
			}
			if ioStat.POT && !lastPOT {
				statusnotifier.Alarm("POT Limit Exceeded")
				notifyDriverStatus("pot_not_exceeded", "POT", masterDev)
				logger.Error("Nidec hardware POT activated")
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

// SetupPDO utilizes the generic YAML-driven C engine.
func (n NidecM700) SetupPDO(dev *MasterDevice) error {
	return setupPDOPositionGeneric(dev)
}

// IsTargetReached returns true when a Profile Position move is complete.
// Nidec M700 uses standard CiA-402: bit 10 is sufficient.
func (n NidecM700) IsTargetReached(sw uint16) bool {
	const bitTargetReached = uint16(1 << 10)
	return sw&bitTargetReached != 0
}

// JogControlword returns the controlword for jog operations.
// Ensures the Halt bit (8) is cleared so motion isn't blocked.
func (n NidecM700) JogControlword(cwBase uint16) uint16 {
	return cwBase & ^uint16(0x0100)
}

// FaultResetControlword returns 0x008F.
// Ensures the drive stays enabled (0x0F) while asserting the fault reset bit (0x80).
func (n NidecM700) FaultResetControlword() uint16 {
	return 0x008F
}

// StandbyOpMode returns Mode 8 (Cyclic Synchronous Position) for Nidec standby.
// Mode 8 keeps the drive strictly synced to the master's generated trajectory
// and bypasses the internal drive profile generators to prevent 0xFF01 trips.
func (n NidecM700) StandbyOpMode() int8 {
	return 8
}

// ResetMultiTurn executes the Nidec multi-turn encoder reset sequence.
// Reads the "resetMultiTurn" block from M700.yml and executes the SDOs.
func (n NidecM700) ResetMultiTurn(availableDevices []*MasterDevice) error {
	for _, device := range availableDevices {
		if device == nil {
			continue
		}
		operation, err := GetEtherCATOperation("resetMultiTurn", device.Device.AddressConfigName)
		if err != nil {
			return fmt.Errorf("[MT-NIDEC] no resetMultiTurn config for %s: %w", device.Name, err)
		}

		// If no steps are defined in YAML, return successfully.
		if len(operation.Steps) == 0 {
			logger.Info("[MT-NIDEC] No multi-turn reset steps configured for:", device.Name)
			continue
		}

		logger.Info("[MT-NIDEC] Starting absolute encoder reset for:", device.Name)

		device.EnableJogPDO(false)
		device.EnablePosPDO(false)
		device.desiredTargetVelocity.Store(0)

		for i, step := range operation.Steps {
			if step.Action == "read" {
				val, _ := SDOUpload2(device.Master, device.Position, step)
				logger.Debug("[MT-NIDEC] read:", step.Name, "val:", val)
			} else {
				sdoErr := SDODownload(device.Master, device.Position, step)
				if sdoErr != nil {
					logger.Warn("[MT-NIDEC] write failed at step", i, ":", step.Name, sdoErr)
					return sdoErr
				}
				logger.Info("[MT-NIDEC] OK:", step.Name)
			}
			time.Sleep(500 * time.Millisecond) // Give drive time to process NVM writes
		}

		logger.Info("[MT-NIDEC] Reset writes complete for:", device.Name)
		logger.Info("[MT-NIDEC] *** POWER CYCLE THE DRIVE to complete the absolute encoder reset ***")
	}
	return nil
}