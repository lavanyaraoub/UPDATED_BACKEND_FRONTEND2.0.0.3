package motordriver

import (
	logger "EtherCAT/logger"
)

// readDigitalInputs reads the 0x4F25 input signal register (all 32 bits).
//
// PDO path (preferred, always used after activation): returns the atomic buffer
// value updated every 1ms by the cyclic task — zero latency, no mailbox.
//
// SDO fallback (pre-activation only): executes the named EtherCAT operation from
// the device address config. If PDO is active but PdoDIReady=false (0x4F25
// registration failed at startup), returns an error rather than attempting a
// mailbox SDO read that could block.
//
// Callers must mask the returned value for their specific bit:
//
//	bit 0 — ECS (External Command Signal)
//	bit 1 — POT (Positive Over Travel, active-low)
//	bit 2 — NOT (Negative Over Travel, active-low)
//	bit 3 — HOME sensor (active-low)
//	bit 4 — ALMIN (alarm minor, active-low)
//	bit 5 — hard reset input
//	bit 6 — CL  (Clamp)
//	bit 7 — DCL (Declamp)
func readDigitalInputs(masterDevice *MasterDevice, sdoOperationName string) (int, error) {
	if masterDevice.PdoDIReady {
		// Per-device read — correct on multi-axis systems.
		// Previously called GetLastPDODigitalInputs() which always read masterDevices[0].
		return int(masterDevice.PDODI.Load()), nil
	}
	if IsPDOActive() {
		// SDO reads use the EtherCAT mailbox — separate from the PDO domain.
		// Safe to call while PDO is active; does not race with the cyclic task.
		logger.Warn("[PDO] readDigitalInputs: PdoDIReady=false — SDO fallback for:", sdoOperationName,
			"(0x4F25 not in PDO map; check pdo_setup logs)")
	}
	operation, err := GetEtherCATOperation(sdoOperationName, masterDevice.Device.AddressConfigName)
	if err != nil {
		return 0, err
	}
	for _, step := range operation.Steps {
		if step.Action == "write" {
			SDODownload(masterDevice.Master, masterDevice.Position, step)
		} else {
			result, err2 := SDOUpload2(masterDevice.Master, masterDevice.Position, step)
			return result, err2
		}
	}
	return 0, nil
}

// readInputSignal reads all digital inputs using the "readinputsignal" SDO operation.
func readInputSignal(masterDevice *MasterDevice) (int, error) {
	return readDigitalInputs(masterDevice, "readinputsignal")
}

// readECSSignal reads all digital inputs using the "ecs" SDO operation.
func readECSSignal(masterDevice *MasterDevice) (int, error) {
	return readDigitalInputs(masterDevice, "ecs")
}

// hardResetInput reads all digital inputs using the "hard_reset" SDO operation.
func hardResetInput(masterDevice *MasterDevice) (int, error) {
	return readDigitalInputs(masterDevice, "hard_reset")
}
