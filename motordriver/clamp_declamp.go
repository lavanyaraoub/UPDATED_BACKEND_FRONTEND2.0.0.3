package motordriver

import (
	logger "EtherCAT/logger"
	settings "EtherCAT/settings"
	"errors"
	"fmt"
	"time"
)

// waitForServoOff waits until the CiA-402 power stage is de-energised.
// FastPowerOff is asynchronous while PDO is active, so polling CL before
// this state is reached can produce a false clamp timeout.
func waitForServoOff(dev *MasterDevice, timeout time.Duration) bool {
	if !IsPDOActive() {
		return true
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		sw := uint16(dev.PDOStatus.Load() & 0xFFFF)
		state := sw & 0x006F
		if state == 0x0021 || state == 0x0040 {
			logger.Info("[BRAKE-SEQ] phase=SERVO_OFF_CONFIRMED drive=", dev.Name,
				" sw=", fmt.Sprintf("0x%04X", sw))
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}

	sw := uint16(dev.PDOStatus.Load() & 0xFFFF)
	logger.Warn("[BRAKE-SEQ] phase=SERVO_OFF_TIMEOUT drive=", dev.Name,
		" sw=", fmt.Sprintf("0x%04X", sw),
		" — continuing to CL feedback check")
	return false
}

// waitForServoOn waits until the drive is Operation Enabled before the brake
// is released and before a position/velocity command is allowed to start.
func waitForServoOn(dev *MasterDevice, timeout time.Duration) bool {
	if !IsPDOActive() {
		return true
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		sw := uint16(dev.PDOStatus.Load() & 0xFFFF)
		if sw&0x006F == 0x0027 {
			logger.Info("[BRAKE-SEQ] phase=SERVO_ON_CONFIRMED drive=", dev.Name,
				" sw=", fmt.Sprintf("0x%04X", sw))
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}

	sw := uint16(dev.PDOStatus.Load() & 0xFFFF)
	logger.Warn("[BRAKE-SEQ] phase=SERVO_ON_TIMEOUT drive=", dev.Name,
		" sw=", fmt.Sprintf("0x%04X", sw),
		" — continuing to DCL feedback check")
	return false
}

// hasDeclamped verifies whether declamping happened.
//
// FIX (Bug 2a): Changed signature from (MasterDevice) to (*MasterDevice).
//
// The old value-copy signature had two problems:
//  1. masterDevice.Driver is an interface pointer. When the struct is copied,
//     the interface value is copied correctly — but any method that inspects
//     the pointer identity of the device (e.g. to match against masterDevices[])
//     would see a different address. More importantly it signals intent wrongly:
//     this function reads live PDO state from the device which must be the
//     real struct, not a snapshot.
//  2. The GetMotorDriver() fallback returned the global driver — always the
//     last drive type configured via SetMotorDriver(). On a mixed-axis setup
//     (A6 on Axis A, Delta on Axis B) this routed Axis A's declamp signal
//     read through Delta's bit-mapping, giving wrong HIGH/LOW results.
//
// FIX (Bug 2b): Replace GetMotorDriver() fallback with a hard error.
//
//	The fallback silently masked the real problem (Driver not set at InitMaster).
//	A clear error forces the bug to surface immediately instead of producing
//	wrong clamp/declamp readings that are impossible to diagnose in the field.
func hasDeclamped(masterDevice *MasterDevice, envSettings settings.DriverSettings) (bool, error) {
	if masterDevice == nil {
		return false, errors.New("hasDeclamped: nil device")
	}
	if masterDevice.Driver == nil {
		return false, fmt.Errorf("hasDeclamped: Driver not initialised for device %s — check InitMaster", masterDevice.Name)
	}

	if envSettings.ClampDeclamp == 0 {
		logger.Info("[BRAKE-SEQ] phase=DECLAMP_BYPASS drive=", masterDevice.Name)
		return true, nil
	}

	logger.Info("[BRAKE-SEQ] phase=DECLAMP_START drive=", masterDevice.Name,
		" timeout=", envSettings.ClampDeclampTiming, "ms")

	// The previous clamp sequence leaves the servo in Shutdown. Re-enable it
	// and wait for Operation Enabled before releasing the mechanical brake.
	if err := FastPowerOn(masterDevice); err != nil {
		return false, fmt.Errorf("declamp servo-on failed: %w", err)
	}
	if !waitForServoOn(masterDevice, time.Second) {
		logger.Warn("[BRAKE-SEQ] phase=SERVO_ON_TIMEOUT_WARN drive=", masterDevice.Name,
			" — releasing brake; cyclic state machine is still transitioning")
	}

	if err := breakOff(masterDevice); err != nil {
		return false, fmt.Errorf("declamp command failed: %w", err)
	}

	ok, err := masterDevice.Driver.readDeclampSignal(masterDevice, envSettings.ClampDeclampTiming)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, errors.New("declamping error")
	}

	logger.Info("[BRAKE-SEQ] phase=DECLAMP_COMPLETE drive=", masterDevice.Name)
	return true, nil
}

// hasClamped verifies whether clamping happened.
// Returns true if clamped successfully, false with error if not clamped.
//
// FIX (Bug 2a): Changed signature from (MasterDevice) to (*MasterDevice).
// FIX (Bug 2b): Replace GetMotorDriver() fallback with a hard error.
// See hasDeclamped above for the full explanation.
func hasClamped(masterDevice *MasterDevice, envSettings settings.DriverSettings) (bool, error) {
	if masterDevice == nil {
		return false, errors.New("hasClamped: nil device")
	}
	if masterDevice.Driver == nil {
		return false, fmt.Errorf("hasClamped: Driver not initialised for device %s — check InitMaster", masterDevice.Name)
	}

	if envSettings.ClampDeclamp == 0 {
		logger.Info("[BRAKE-SEQ] phase=CLAMP_BYPASS drive=", masterDevice.Name)
		return false, nil
	}

	logger.Info("[BRAKE-SEQ] phase=CLAMP_START drive=", masterDevice.Name,
		" timeout=", envSettings.ClampDeclampTiming, "ms")

	// Command the brake before removing servo torque so the axis is never
	// simultaneously servo-off and brake-open.
	if err := breakOn(masterDevice); err != nil {
		return false, fmt.Errorf("clamp command failed: %w", err)
	}
	logger.Info("[BRAKE-SEQ] phase=BRAKE_COMMANDED drive=", masterDevice.Name)

	if err := FastPowerOff(masterDevice); err != nil {
		return false, fmt.Errorf("clamp servo-off failed: %w", err)
	}
	logger.Info("[BRAKE-SEQ] phase=SERVO_OFF_REQUESTED drive=", masterDevice.Name)
	if !waitForServoOff(masterDevice, 2*time.Second) {
		logger.Warn("[BRAKE-SEQ] phase=SERVO_OFF_TIMEOUT_WARN drive=", masterDevice.Name,
			" — continuing to CL feedback check")
	}

	ok, err := masterDevice.Driver.readClampSignal(masterDevice, envSettings.ClampDeclampTiming)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, errors.New("clamping error")
	}

	logger.Info("[BRAKE-SEQ] phase=CLAMP_COMPLETE drive=", masterDevice.Name)
	return true, nil
}

// applyClampIfSettingsChanged applies clamping when settings change and
// clamp/declamp is enabled, provided the motor is not currently running.
func applyClampIfSettingsChanged() {
	for _, dev := range masterDevices {
		if dev == nil {
			continue
		}
		devSetting := settings.GetDriverSettings(dev.Name)
		stat := getCurrentDriverStatus(dev.Name)
		if stat.isMotorRunning {
			logger.Warn("[BRAKE-SEQ] settings change deferred; motor running drive=", dev.Name)
			continue
		}

		if devSetting.ClampDeclamp == 1 {
			logger.Info("[BRAKE-SEQ] settings cl_dl=1; applying clamp drive=", dev.Name)
			if err := breakOn(dev); err != nil {
				logger.Error("[BRAKE-SEQ] settings brake-on failed drive=", dev.Name, " error=", err)
				continue
			}
			if err := FastPowerOff(dev); err != nil {
				logger.Error("[BRAKE-SEQ] settings servo-off failed drive=", dev.Name, " error=", err)
				continue
			}
			waitForServoOff(dev, 2*time.Second)
		} else {
			logger.Info("[BRAKE-SEQ] settings cl_dl=0; releasing brake drive=", dev.Name)
			if err := FastPowerOn(dev); err != nil {
				logger.Error("[BRAKE-SEQ] settings servo-on failed drive=", dev.Name, " error=", err)
				continue
			}
			waitForServoOn(dev, time.Second)
			if err := breakOff(dev); err != nil {
				logger.Error("[BRAKE-SEQ] settings declamp failed drive=", dev.Name, " error=", err)
			}
		}
	}
}