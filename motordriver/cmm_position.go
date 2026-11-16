package motordriver

import "fmt"

// GetActualPositionDeg returns the actual table angle in degrees for the named
// drive. It uses the same conversion as RefreshCurrentPosition (and therefore
// the same angle the UI shows), but:
//   - it does not broadcast anything to the UI, and
//   - it reads PDO only, never SDO, so it is safe to call from a network
//     goroutine while a program is running.
//
// Added for the CMM Ethernet API (package cmmlink).
func GetActualPositionDeg(driveName string) (float64, error) {
	for _, dev := range getMasterDevices() {
		if dev.Name != driveName {
			continue
		}
		if !dev.PdoReady {
			return 0, fmt.Errorf("PDO not ready for drive %s", driveName)
		}
		deg, _ := currentPosition(dev.PDOPos.Load(), dev.Device.DriveXRatio, dev.Name)
		return deg, nil
	}
	return 0, fmt.Errorf("drive %s not found", driveName)
}
