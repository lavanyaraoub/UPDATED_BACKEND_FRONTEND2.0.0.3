package motordriver

import logger "EtherCAT/logger"

// configureDriver sends the "configure" SDO sequence to the drive before
// ecrt_master_activate is called.
//
// ERROR HANDLING POLICY:
//
//	Individual step failures are logged as warnings but do NOT abort the
//	startup sequence. The drive may reject certain objects depending on its
//	firmware version, current state, or parameter protection settings.
//	A rejected non-critical parameter (e.g. acceleration profile) should not
//	prevent the drive from running.
//
//	If the operation itself cannot be found in the config, that IS a fatal
//	error — it means the address config file is missing or malformed.
//
// DELTA ASDA-A2-E NOTE:
//
//	0x6083 (profile acceleration) and 0x6084 (profile deceleration) have
//	been removed from the Delta configure YAML because this drive's firmware
//	rejects CoE SDO writes to those objects with an I/O error. Acceleration
//	and deceleration are controlled via the Delta ASDA Soft servo tuning
//	software (parameters P1-34 and P1-35) and persist in the drive's NVM.
func configureDriver(device *MasterDevice) error {
	operation, err := GetEtherCATOperation("configure", device.Device.AddressConfigName)
	if err != nil {
		return err
	}
	logger.Info("configuring master driver: ", device.Name)
	for _, step := range operation.Steps {
		if err := SDODownload(device.Master, device.Position, step); err != nil {
			// Log the failure but continue — a single rejected parameter
			// must not abort the entire initialisation sequence.
			logger.Warn("[CONFIG] Step \""+step.Name+"\" failed (drive may not support this object at current state) — continuing:", err)
		}
	}
	return nil
}
