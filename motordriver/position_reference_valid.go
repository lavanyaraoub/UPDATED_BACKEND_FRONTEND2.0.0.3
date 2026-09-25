package motordriver

import "EtherCAT/settings"

// IsPositionReferenceValid reports whether the drive has a trusted position
// reference from a successful zero/reference calibration.
func IsPositionReferenceValid(driveName string) bool {
	return settings.GetDriverSettings(driveName).HomingValid
}
