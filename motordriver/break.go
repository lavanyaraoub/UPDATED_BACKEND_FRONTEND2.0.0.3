package motordriver

import (
	"EtherCAT/logger"
	"fmt"
)

// breakBit is the 0x60FE bit that controls the brake solenoid digital output.
// Bit 1 of 0x60FE:02 drives OUT2 (brake solenoid wire on this machine).
const breakBit = uint32(0x00000002)

// Helper to grab the correct pointer for multi-axis targeting
func getRealDeviceForBrake(name string) *MasterDevice {
	for _, dev := range getMasterDevices() {
		if dev != nil && dev.Name == name {
			return dev
		}
	}
	return nil
}

func breakOn(masterDevice *MasterDevice) error {
	if masterDevice == nil {
		return fmt.Errorf("breakOn: nil device")
	}
	logger.Info("[BRAKE-SEQ] phase=CLAMP_CMD drive=", masterDevice.Name,
		" bit=0x00000002 action=SET")

	if IsPDOActive() {
		if !masterDevice.PdoDigOutReady {
			return fmt.Errorf("breakOn: PdoDigOutReady=false for %s", masterDevice.Name)
		}
		realDev := getRealDeviceForBrake(masterDevice.Name)
		if realDev == nil {
			return fmt.Errorf("breakOn: device %s not found", masterDevice.Name)
		}
		value := PDOUpdateDigitalOutputBits(realDev, breakBit, true)
		logger.Info("[BRAKE-SEQ] phase=CLAMP_CMD_QUEUED drive=", masterDevice.Name,
			" commanded=", fmt.Sprintf("0x%08X", value))
		return nil
	}

	operation, err := GetEtherCATOperation("break_on", masterDevice.Device.AddressConfigName)
	if err != nil {
		return err
	}
	for _, step := range operation.Steps {
		if err := SDODownload(masterDevice.Master, masterDevice.Position, step); err != nil {
			return err
		}
	}
	return nil
}

func breakOff(masterDevice *MasterDevice) error {
	if masterDevice == nil {
		return fmt.Errorf("breakOff: nil device")
	}
	logger.Info("[BRAKE-SEQ] phase=DECLAMP_CMD drive=", masterDevice.Name,
		" bit=0x00000002 action=CLEAR")

	if IsPDOActive() {
		if !masterDevice.PdoDigOutReady {
			return fmt.Errorf("breakOff: PdoDigOutReady=false for %s", masterDevice.Name)
		}
		realDev := getRealDeviceForBrake(masterDevice.Name)
		if realDev == nil {
			return fmt.Errorf("breakOff: device %s not found", masterDevice.Name)
		}
		value := PDOUpdateDigitalOutputBits(realDev, breakBit, false)
		logger.Info("[BRAKE-SEQ] phase=DECLAMP_CMD_QUEUED drive=", masterDevice.Name,
			" commanded=", fmt.Sprintf("0x%08X", value))
		return nil
	}

	operation, err := GetEtherCATOperation("break_off", masterDevice.Device.AddressConfigName)
	if err != nil {
		return err
	}
	for _, step := range operation.Steps {
		if err := SDODownload(masterDevice.Master, masterDevice.Position, step); err != nil {
			return err
		}
	}
	return nil
}