package motordriver

import (
	helper "EtherCAT/helper"
	"EtherCAT/logger"
	settings "EtherCAT/settings"
	"fmt"
	"math"
)

// InitAposCorrection detects a boot-time encoder sign flip for the given drive
// by comparing the current PDO position (first real frame after activation)
// against the HomingApos saved by the last multiturn calibration.
//
// FIX (multi-drive): correction stored in dev.AposCorrection (per-device atomic
// on MasterDevice) instead of the old package-level aposCorrection global.
// The global was shared — on two axes the second call overwrote the first.
func InitAposCorrection(driveName string) {
	// getDeviceByName does not exist in this build — inline lookup via getMasterDevices()
	var dev *MasterDevice
	for _, d := range getMasterDevices() {
		if d.Name == driveName {
			dev = d
			break
		}
	}
	if dev == nil {
		logger.Warn("InitAposCorrection: device not found:", driveName)
		return
	}
	ds := settings.GetDriverSettings(driveName)
	if !ds.HomingValid {
		dev.AposCorrection.Store(0)
		logger.Info("InitAposCorrection: home calibration is not valid — perform multiturn reset and apply Home Offset:", driveName)
		return
	}
	bootApos := int32(dev.PDOPos.Load())
	homingApos := ds.HomingApos
	signFlip := false
	if (bootApos > 0) != (homingApos > 0) {
		magRatio := float64(bootApos) / float64(homingApos)
		if magRatio < 0 {
			magRatio = -magRatio
		}
		if math.Abs(magRatio-1.0) < 0.005 {
			signFlip = true
		}
	}
	if signFlip {
		dev.AposCorrection.Store(bootApos - homingApos)
		logger.Info("InitAposCorrection: sign flip detected — correction applied:",
			"drive=", driveName, "bootApos=", bootApos,
			"HomingApos=", homingApos, "correction=", dev.AposCorrection.Load())
	} else {
		dev.AposCorrection.Store(0)
		logger.Info("InitAposCorrection: no sign flip — no correction needed:", driveName)
	}
}

/*
Functions related to driver angle manipulation. Converting degrees to pulses, get absolute degrees
find degrees based on the current pulse of the driver, etc
*/

// getPulsesFromDegree returns the value to send to driver based on the degree passed by the client
func getPulsesFromDegree(masterDevice *MasterDevice, degree float64) int64 {

	// driverSettings := settings.GetDriverSettings(masterDevice.Name)
	pulse := float64(masterDevice.Device.DriveXRatio) * degree
	// pulse = helper.RoundFloatTo3(pulse + float64(driverSettings.BackLash*float64((settings.BacklashRequired(int(pulse))*masterDevice.Device.DriveXRatio))))

	//this rounding is commented as it cause wrong value if the value is big and thus the rotation is always wrong
	// pulse = helper.RoundFloatTo3(pulse)
	pulseForDisp := fmt.Sprintf("%f", pulse)
	logger.Debug("degree: ", degree, " pulse: ", pulseForDisp, " integer: ", int32(pulse), "drive x ratio:", masterDevice.Device.DriveXRatio)
	//tcpserver.js line# 1307
	// if pulse < 0 {
	// 	pulse = pulse * -1
	// }
	return int64(pulse)
}

// nearestIntegerQuotient returns the integer closest to numerator/denominator.
// It is used to select the equivalent full revolution without converting the
// large absolute encoder counts to a floating-point value.
func nearestIntegerQuotient(numerator, denominator int64) int64 {
	if denominator <= 0 {
		return 0
	}
	if numerator >= 0 {
		return (numerator + denominator/2) / denominator
	}
	return -((-numerator + denominator/2) / denominator)
}

// calculateFixedAbsoluteGoal converts an application-level absolute angle into
// one repeatable encoder target. currentRaw is used only to select which
// equivalent revolution satisfies the signed CW/CCW path; it does not alter
// the calibrated count assigned to the destination.
//
// signedPath is the existing path planner's result:
//   positive = the currently configured CW direction
//   negative = the currently configured CCW direction
//
// directionalBacklash must already be zero for CW or the configured positive
// backlash magnitude for CCW. The same compensation is included in both the
// canonical target and the approximate path target, so it is applied once.
func calculateFixedAbsoluteGoal(
	masterDevice *MasterDevice,
	destination float64,
	signedPath float64,
	pitchError float64,
	directionalBacklash float64,
) (int32, error) {
	if masterDevice == nil {
		return 0, fmt.Errorf("calculateFixedAbsoluteGoal: nil device")
	}
	if math.IsNaN(destination) || math.IsInf(destination, 0) ||
		math.IsNaN(signedPath) || math.IsInf(signedPath, 0) ||
		math.IsNaN(pitchError) || math.IsInf(pitchError, 0) ||
		math.IsNaN(directionalBacklash) || math.IsInf(directionalBacklash, 0) {
		return 0, fmt.Errorf("calculateFixedAbsoluteGoal: non-finite motion value")
	}

	driverSettings := settings.GetDriverSettings(masterDevice.Name)
	if !driverSettings.HomingValid {
		return 0, fmt.Errorf("absolute positioning requires a valid home calibration for %s; perform multiturn reset and apply Home Offset", masterDevice.Name)
	}

	ratio := int64(masterDevice.Device.DriveXRatio)
	if ratio <= 0 {
		return 0, fmt.Errorf("absolute positioning requires a positive drive_x_ratio for %s", masterDevice.Name)
	}
	countsPerRevolution := int64(360) * ratio

	currentRaw := int64(masterDevice.PDOPos.Load())
	homeRaw := int64(driverSettings.HomingApos) + int64(masterDevice.AposCorrection.Load())

	compensatedDestination := destination + pitchError - directionalBacklash
	canonicalTarget := homeRaw + int64(math.Round(compensatedDestination*float64(ratio)))

	// Preserve the old path planner's CW/CCW decision. This approximate goal
	// identifies the desired encoder revolution only; it is never sent to the
	// drive. The final goal always comes from homeRaw + calibrated offset.
	compensatedPath := signedPath + pitchError - directionalBacklash
	approximateGoal := currentRaw + int64(math.Round(compensatedPath*float64(ratio)))
	revolution := nearestIntegerQuotient(
		approximateGoal-canonicalTarget,
		countsPerRevolution,
	)
	absoluteGoal := canonicalTarget + revolution*countsPerRevolution

	const (
		minInt32Target = int64(-1 << 31)
		maxInt32Target = int64(1<<31 - 1)
	)
	if absoluteGoal < minInt32Target || absoluteGoal > maxInt32Target {
		return 0, fmt.Errorf(
			"absolute target overflow for %s: goal=%d allowed=[%d,%d]",
			masterDevice.Name, absoluteGoal, minInt32Target, maxInt32Target,
		)
	}

	logger.Info("[ABS-POS] drive=", masterDevice.Name,
		" home=", homeRaw,
		" current=", currentRaw,
		" destination=", destination,
		" signedPath=", signedPath,
		" pitch=", pitchError,
		" backlash=", directionalBacklash,
		" canonical=", canonicalTarget,
		" revolution=", revolution,
		" goal=", absoluteGoal,
		" delta=", absoluteGoal-currentRaw)

	return int32(absoluteGoal), nil
}

/*
currentPosition returns the angle in degree based on the postion value received from driver.

	driveXRatio: is a config value configured along with the master device in configs/device-configuration.yml
	pos: position received from driver, this will be in pulses
*/
func currentPosition(pos int32, driveXRatio int, driveName string) (float64, float64) {
	driverSettings := settings.GetDriverSettings(driveName)
	// angleWithPitchError := float32(0.00)

	// FIX (multi-drive): use dev.AposCorrection instead of package-level global.
	var correction int32
	for _, d := range getMasterDevices() {
		if d.Name == driveName {
			correction = d.AposCorrection.Load()
			break
		}
	}
	correctedPos := pos - correction
	var drivePosition float64
	if driverSettings.HomingValid {
		// HomingApos already includes the machine-specific Home Offset.
		// Use the same fixed raw reference for display and absolute targets.
		drivePosition = float64(correctedPos) - float64(driverSettings.HomingApos)
	} else {
		// Compatibility display before the first calibrated multiturn reset.
		driveOffset := driverSettings.HomingOffset * float32(driveXRatio)
		drivePosition = float64(correctedPos) - float64(driveOffset)
	}
	drivePosition = drivePosition / float64(driveXRatio)
	drivePosition = math.Mod(float64(drivePosition), 360)
	if drivePosition < 0 {
		drivePosition = drivePosition + 360
	}
	if drivePosition >= 359.999 {
		drivePosition = 0
	}
	drivePositionWithErrorCorrection := (drivePosition) //+ (float64(driverSettings.FactorBacklash) * driverSettings.BackLash)
	return helper.RoundFloat(drivePosition, 3), helper.RoundFloat(drivePositionWithErrorCorrection, 3)
}

//TODO add function to find pitch error like below, this is the value sending to ui
// drivePosition = drivePosition + float32((settings.FactorBacklash() * driverSettings.BackLash)) - angleWithPitchError

/*
getAbsolutePosition move the motor to absolute degree

returns

	move to position for driver
	destination position to communicate to clients to display the destination postion

definition

target position is 30 then motor will rotate to 30degree
irrespective where the current position is
based on getDestinationAngle() in machine_parser.js line# 420
*/
func getAbsolutePosition(currentPos float64, targetPos float64, useShortestPath bool) (float64, float64) {
	return helper.GetAbsolutePosition(currentPos, targetPos, useShortestPath)
}

// /*
// getAbsolutePath get the angle to rotate in absolute path

// for e.g. if current position is 50 and target position is 10 then rotate all the way
// to 360 and go to 10
// */
// func getAbsolutePath(currentPos float64, targetPos float64) float64 {
// 	toMove := math.Mod(currentPos, 360)
// 	toMove = toMove - targetPos
// 	toMove = 360 - toMove
// 	if math.Abs(currentPos) > 360 {
// 		toMove = math.Mod(toMove, 360)
// 	}

// 	if targetPos < 0 {
// 		toMove = toMove - 360
// 		if toMove <= -360 {
// 			toMove = toMove + 360
// 		}
// 	}

// 	return toMove
// }

// /*
// getAbsolutePositionWithShortestPath move to absolute postion but uses shortest path

// for eg. if current position is 50 and target position is 40 then will -10 and move to 40
// */
// func getAbsolutePositionWithShortestPath(currentPos float64, targetPos float64) float64 {
// 	currentPos = math.Mod(currentPos, 360)
// 	modeDiff := math.Mod((targetPos - currentPos), 360)
// 	shortestDistance := 180 - math.Abs(math.Abs(modeDiff)-180)

// 	test := math.Mod((modeDiff + 360), 360)
// 	if test < 180 {
// 		return shortestDistance * 1
// 	}
// 	return shortestDistance * -1
// }

/*
getRelativePosition returns the relative position based on the current position

for e.g. if the current position is 10 degree and ordered 20 degree then
motor will move to 30degree (10+20=30)
based on getDestinationAngle() in machine_parser.js line# 420
*/
func getRelativePosition(currentPos float64, targetPos float64, prevDestinationAngle float64) (float64, float64) {
	// If previous destination is too far from current, use current position instead.
	if math.Abs(currentPos-prevDestinationAngle) > 1.0 { // 1° threshold, tune as needed
		prevDestinationAngle = currentPos
	}
	return helper.GetRelativePosition(currentPos, targetPos, prevDestinationAngle)
}

/*
getPitchError returns the configured pitch error

It’s position calibration done to compensate mechanical error.
The mechanical systems shall be manufactured and assembled to the closest precision possible
before entering the pitch error compensation. Pitch error is a position error measured at equal
intervals of rotary table. In our case we shall keep interval for every 10 degrees. That is 0 degrees
till 360 Degrees shall have a total of 36 intervals. The pitch error on the rotary table shall be
measured on the final output shaft / Rotary table top. The error at different intervals is measured
by external fine measuring equipment.

Whenever the controller is moving or commanded to any position. The commanded value will be
added or subtracted with error defined in the interval chart. The user display of position data will
always show the original position command, all the error data shall be acting only in the backend
calculation. When the controller is being operated the user display shall not physically show
the compensated error.
*/
func getPitchError(driveName string, targetPos float64) float64 {
	index := int(math.Abs(targetPos / 10))
	index = index - 1
	if index < 0 {
		return 0
	}
	if index > 35 {
		return 0
	}
	driverSettings := settings.GetDriverSettings(driveName)
	// CRASH FIX: the two guards above only bound index to the DOCUMENTED
	// valid range (0-35, one per 10-degree interval) — they say nothing
	// about whether PitchError actually HAS that many entries configured.
	// On any rig that hasn't been through pitch-error calibration,
	// PitchError is an empty (or partially populated) slice, and indexing
	// into it directly panics with "index out of range" — which crashes
	// the entire process mid-motion, taking the PDO cyclic task down with
	// it and leaving the drive in an uncontrolled/faulted state.
	//
	// Fix: treat a missing/short PitchError table the same as "no
	// compensation configured for this interval" — return 0, exactly like
	// the existing out-of-documented-range guards above already do.
	if index >= len(driverSettings.PitchError) {
		return 0
	}
	return float64(driverSettings.PitchError[index])
}

// func reversePitchError(currentPos float64) float64 {
// 	index := int(math.Abs(currentPos / 10))
// 	index = index - 1
// 	if index < 0 {
// 		return 0
// 	}
// 	// if index > 35 {
// 	// 	return 0
// 	// }
// 	driverSettings := settings.GetDriverSettings()
// 	return float64(driverSettings.PitchError[index] * -1)
// }
