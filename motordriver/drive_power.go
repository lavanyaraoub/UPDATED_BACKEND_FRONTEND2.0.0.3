package motordriver

import (
	"EtherCAT/ethercatdevicedatatypes"
	logger "EtherCAT/logger"
	"time"
)

// pdoStopMotion halts all PDO motion on a device atomically and returns the
// live *MasterDevice pointer so the caller can do additional atomic writes.
// Returns nil if no matching device is found in masterDevices.
func pdoStopMotion(name string) *MasterDevice {
	for _, d := range masterDevices {
		if d.Name == name {
			d.EnableJogPDO(false)
			d.EnablePosPDO(false)
			d.desiredTargetVelocity.Store(0)
			return d
		}
	}
	return nil
}

// PowerOn brings the drive to Operation Enabled via the CiA-402 SDO sequence.
//
// Safe to call ONLY before ecrt_master_activate (i.e. during InitMaster).
// Once PDO is active the CiA-402 state machine inside StartPDOCyclic handles
// the power-on sequence automatically — no SDO intervention is needed or safe.
//
// 0x6040 controlword sequence per SX-DSV03242_R2_1E_A6.pdf page 83.
func PowerOn(masterDevice *MasterDevice) error {
	if IsPDOActive() {
		// PDO cyclic owns the bus — the state machine will (re-)enable the
		// drive automatically. Nothing to do here.
		logger.Info("[PDO] PowerOn: PDO active — CiA-402 state machine handles enable. Skipping SDO.")
		return nil
	}

	logger.Trace("poweron driver: ", masterDevice.Name)
	operation, err := GetEtherCATOperation("poweron", masterDevice.Device.AddressConfigName)
	if err != nil {
		return err
	}
	for _, step := range operation.Steps {
		SDODownload(masterDevice.Master, masterDevice.Position, step)
	}
	return nil
}

// FastPowerOn enables the drive quickly via SDO.
//
// Safe to call ONLY before ecrt_master_activate (startup / setupDrivers).
// When PDO is active, drive enable is managed by the CiA-402 state machine;
// this function just notifies the UI that the drive is on.
func FastPowerOn(masterDevice *MasterDevice) error {
	logger.Trace("fast poweron driver: ", masterDevice.Name)

	if IsPDOActive() {
		// PDO-safe equivalent of old FastPowerOn:
		// clear pdoShutdownActive so the cyclic standby branch reverts to
		// CW=0x000F and walks the drive back up to Operation Enabled.
		if d := pdoStopMotion(masterDevice.Name); d != nil {
			d.pdoShutdownActive.Store(false)
		}
		logger.Info("[PDO] FastPowerOn: shutdown cleared — drive re-enabled by PDO cyclic task.")
		notifyDriverStatus("driver_on_off", "1", masterDevice)
		return nil
	}

	operation, err := GetEtherCATOperation("fastPowerOn", masterDevice.Device.AddressConfigName)
	if err != nil {
		return err
	}
	for _, step := range operation.Steps {
		if err := SDODownload(masterDevice.Master, masterDevice.Position, step); err != nil {
			logger.Error(err)
		}
	}
	notifyDriverStatus("driver_on_off", "1", masterDevice)
	return nil
}

// PowerOffAll powers off every device in the slice.
func PowerOffAll(masterDevices []*MasterDevice) error {
	for _, d := range masterDevices {
		if err := PowerOff(d); err != nil {
			return err
		}
	}
	return nil
}

var powerOff = ethercatdevicedatatypes.Operation{}

// PowerOff disables the drive.
//
// When PDO is active, motion is halted via atomic setpoints and the drive is
// left in Operation Enabled standby (holding position). This is intentional:
// IgH EtherCAT has no ecrt_master_deactivate(), so the cyclic task must keep
// running. Issuing SDO writes to 0x6040 after activation would block waiting
// for a mailbox response that only arrives when the cyclic is running.
func PowerOff(masterDevice *MasterDevice) error {
	logger.Trace("poweroff driver: ", masterDevice.Name)

	if IsPDOActive() {
		// Halt motion atomically — cyclic standby branch holds position.
		pdoStopMotion(masterDevice.Name)
		logger.Info("[PDO] PowerOff: motion stopped via PDO atomics. Drive held at position.")
		notifyDriverStatus("driver_on_off", "0", masterDevice)
		notifyDriverStatus("motor_running", "false", masterDevice)
		return nil
	}

	if powerOff.Name == "" {
		powerOffTmp, err := GetEtherCATOperation("poweroff", masterDevice.Device.AddressConfigName)
		if err != nil {
			return err
		}
		powerOff = powerOffTmp
	}
	for _, step := range powerOff.Steps {
		SDODownload(masterDevice.Master, masterDevice.Position, step)
	}
	notifyDriverStatus("driver_on_off", "0", masterDevice)
	notifyDriverStatus("motor_running", "false", masterDevice)
	return nil
}

// FastPowerOff halts the drive quickly.
//
// Called from: setupDrivers (pre-activation, safe), checkPotNotLimit (live,
// PDO active). The PDO path must NOT issue any SDO writes — it disables
// motion modes and zeroes velocity via the atomic setpoints instead.
func FastPowerOff(masterDevice *MasterDevice) error {
	logger.Trace("fast poweroff driver: ", masterDevice.Name)

	if IsPDOActive() {
		// PDO-safe equivalent of old FastPowerOff:
		// stop motion atomically, then set pdoShutdownActive=true so the cyclic
		// standby branch sends CW=0x0006 (Shutdown), walking the drive from
		// Operation Enabled → Ready To Switch On without any SDO/PDO race.
		// Non-blocking — readClampSignal polls with its own timeout.
		if d := pdoStopMotion(masterDevice.Name); d != nil {
			d.pdoShutdownActive.Store(true)
		}
		logger.Info("[PDO] FastPowerOff: shutdown requested via PDO cyclic for driver:", masterDevice.Name)
		notifyDriverStatus("driver_on_off", "0", masterDevice)
		notifyDriverStatus("motor_running", "false", masterDevice)
		return nil
	}

	operation, err := GetEtherCATOperation("fastPowerOff", masterDevice.Device.AddressConfigName)
	if err != nil {
		return err
	}
	for _, step := range operation.Steps {
		if step.Action == "read" {
			val, _ := SDOUpload2(masterDevice.Master, masterDevice.Position, step)
			logger.Trace("fast power off current val", val)
		} else {
			if err := SDODownload(masterDevice.Master, masterDevice.Position, step); err != nil {
				logger.Error(err)
			}
		}
	}
	notifyDriverStatus("driver_on_off", "0", masterDevice)
	notifyDriverStatus("motor_running", "false", masterDevice)
	return nil
}

// emergencyRampStop performs a fast, smooth velocity ramp-down to zero.
//
// WHY THIS IS NEEDED:
//
//	Setting velocity=0 and waiting for the drive's own decel ramp causes
//	overshoot: the motor coasts past the stopping point, then CSP standby
//	locks onto the overshot position and the drive's position controller
//	yanks it back — producing a visible jerk/bounce.
//
//	By stepping velocity down ourselves in tight increments we control the
//	deceleration profile directly. The motor stops exactly where we want,
//	CSP has nothing to correct, and the result is a firm but clean stop.
//
//	steps × interval = total ramp time.  10 × 10ms = 100ms is fast enough
//	for emergency but gentle enough to avoid mechanical shock.
func emergencyRampStop(d *MasterDevice) {
	const (
		steps    = 10
		interval = 10 * time.Millisecond
	)

	current := d.desiredTargetVelocity.Load()
	if current == 0 {
		return
	}

	logger.Warn("[PDO] emergencyRampStop: ramping velocity from", current, "to 0 in",
		steps, "steps over", steps*int(interval/time.Millisecond), "ms")

	for i := 1; i <= steps; i++ {
		stepped := current * int32(steps-i) / int32(steps)
		d.desiredTargetVelocity.Store(stepped)
		time.Sleep(interval)
	}
	d.desiredTargetVelocity.Store(0)

	// One extra cycle settle: wait for the drive to physically stop
	// (velocity actual reaches ~0) before we switch modes.
	time.Sleep(20 * time.Millisecond)

	// Snapshot the final rest position for the cyclic standby reference.
	// Do NOT call SetTargetPositionPDO here — that arms ppSetpointPending and
	// causes the cyclic PP branch to fire a set-point pulse on the next tick,
	// which was the Mode 3→1 jerk at emergency stop.
	// The cyclic standby (Mode 3 vel=0) tracks actual position every cycle;
	// it does not need an explicit target update.
	restPos := d.PDOPos.Load()
	d.currentTargetPosition.Store(restPos)

	logger.Info("[PDO] emergencyRampStop: motor stopped at pos=", restPos)
}

// emergency triggers an emergency stop.
//
// When PDO is active this function uses a two-phase approach:
//
//	Phase 1 – Fast software ramp-down (eliminates bounce-back jerk)
//	  Root cause of bounce: simply zeroing velocity and waiting lets the
//	  drive's internal ramp overshoot the stopping point. CSP standby then
//	  locks onto the overshot position and the position controller corrects
//	  back, producing a visible jerk. emergencyRampStop() steps the velocity
//	  down in 10 equal increments over 100 ms so the motor stops in-place.
//	  The exact rest position is latched as the CSP target before jog is
//	  disabled, so standby has nothing to correct.
//
//	Phase 2 – Fault reset / position hold
//	  Once the motor is stationary, PDOFaultReset clears any latched fault
//	  and parks the drive in CiA-402 standby.
func emergency(masterDevice *MasterDevice) error {
	logger.Trace("emergency activated ", masterDevice.Name)

	if IsPDOActive() {
		for _, d := range masterDevices {
			if d.Name == masterDevice.Name {
				if d.IsJogEnabled() {
					// Fast ramp-down: steps velocity to 0 smoothly, then
					// locks the rest position before cutting jog mode.
					emergencyRampStop(d)
					d.EnableJogPDO(false)
					logger.Warn("[PDO] emergency: jog stopped cleanly via ramp for driver:", masterDevice.Name)
				}
				if d.IsPosEnabled() {
					// SAFE DECELERATION for PP mode:
					//
					// DO NOT call SetTargetPositionPDO(current) here.
					//
					// Root cause of 30-second hang:
					//   SetTargetPositionPDO() sets ppSetpointPending=true, causing the
					//   cyclic PP branch to fire a new set-point pulse (CW bit4=1).
					//   The drive ACKs with SW bit12=1. hasTargetReached Phase 2 checks
					//   bit10=1 AND bit12=0 — bit12=1 resets stableCount. After
					//   EnablePosPDO(false) the cyclic switches to standby where
					//   bit12 may stay HIGH, so Phase 2 never accumulates enough stable
					//   readings and the goroutine holds motionMu for the full 30s timeout.
					//
					// Correct approach (matches old build):
					//   1. Switch opMode to 3 (Profile Velocity) and set velocity=0.
					//      The drive decelerates cleanly using its 0x6084 profile ramp.
					//      No new set-point pulse, no bit12 interference.
					//   2. EnablePosPDO(false) → cyclic standby takes over in Mode 3 vel=0.
					//      No hardware mode switch occurs: drive was already in Mode 3
					//      from step 1, so there is no correction-torque jerk.
					//   3. Signal hasTargetReached via posMoveAborted to exit immediately.
					d.desiredOpMode.Store(3)         // decel via drive's 0x6084 profile ramp
					d.desiredTargetVelocity.Store(0) // target velocity = 0
					d.posMoveAborted.Store(true)     // unblock hasTargetReached immediately
					d.EnablePosPDO(false)            // cyclic standby (Mode 3) takes over
					logger.Warn("[PDO] emergency: PP move aborted — decelerating via mode 3 ramp for driver:", masterDevice.Name)
				}
				break
			}
		}

		logger.Warn("[PDO] emergency: motion stopped cleanly, delegating to PDOFaultReset for driver:", masterDevice.Name)
		PDOFaultReset(masterDevices)
		return nil
	}

	operation, err := GetEtherCATOperation("emergency", masterDevice.Device.AddressConfigName)
	if err != nil {
		return err
	}
	for _, step := range operation.Steps {
		SDODownload(masterDevice.Master, masterDevice.Position, step)
	}
	return nil
}
