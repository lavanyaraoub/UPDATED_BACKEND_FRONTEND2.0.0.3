package motordriver

import (

	//\"fmt\"
	channels "EtherCAT/channels"
	//ethercatDevice \"EtherCAT/ethercatdevicedatatypes\"
	logger "EtherCAT/logger"
	"EtherCAT/settings"
	"fmt"
	"sync/atomic"
	"time"
)

var stopECSCheckChan chan bool
var isECSCheckInProgress atomic.Bool

func init() {
	stopECSCheckChan = make(chan bool, 1)
}

func stopECSCheck() {
	if isECSCheckInProgress.Load() {
		select {
		case stopECSCheckChan <- true:
		default:
		}
	}
	isECSCheckInProgress.Store(false)
}

// -------------------------------------------------------------------
// ECS "GO-HIGH" Phase — wait until ECS goes high (ready to move)
// -------------------------------------------------------------------
func doECSCheck(masterDevice *MasterDevice, degree float64) int {
	envSettings := settings.GetDriverSettings(masterDevice.Name)
	if envSettings.ECS == 1 {
		logger.Debug("driver", masterDevice.Name, "waiting for ECS HIGH. rotate to", degree)
		ecsRec, _ := waitForECS(masterDevice)
		if ecsRec == 1 {
			logger.Debug("driver", masterDevice.Name, "received ECS HIGH")
		} else if ecsRec == 0 {
			logger.Error("driver", masterDevice.Name, "NOT received ECS HIGH")
			channels.SendAlarm("Not received ECS")
		} else {
			logger.Info("exiting ECS check due to stop/reset event")
		}
		return ecsRec
	}
	return 1
}

// -------------------------------------------------------------------
// ECS "GO-LOW" Phase — wait forever until ECS goes low again
// -------------------------------------------------------------------
func doECSCheckZero(masterDevice *MasterDevice, degree float64) int {
	envSettings := settings.GetDriverSettings(masterDevice.Name)
	if envSettings.ECS == 1 {
		logger.Debug("driver", masterDevice.Name, "waiting for ECS LOW (zero). rotate to", degree)
		ecsRec, _ := waitForECSZero(masterDevice)
		if ecsRec == 1 {
			logger.Debug("driver", masterDevice.Name, "ECS went LOW (zero phase done)")
		} else if ecsRec == 0 {
			logger.Error("driver", masterDevice.Name, "ECS zero NOT received")
			channels.SendAlarm("ECS did not go LOW")
		} else {
			logger.Info("exiting ECS zero check as program stop/reset event received")
		}
		return ecsRec
	}
	return 1
}

// -------------------------------------------------------------------
// Wait for ECS HIGH (ready)
// -------------------------------------------------------------------
func waitForECS(masterDevice *MasterDevice) (int, error) {
	operation, err := GetEtherCATOperation("ecs", masterDevice.Device.AddressConfigName)
	if err != nil {
		return 0, err
	}

	// FIX (Bug 3): Replace GetMotorDriver() fallback with a hard error.
	//
	// GetMotorDriver() returns the GLOBAL driver — whichever drive type was
	// configured last via SetMotorDriver(). On a mixed-axis setup (A6 on Axis A,
	// Delta on Axis B), this always returned the Delta driver regardless of which
	// axis triggered the ECS check. Delta's receivedECS() reads different digital
	// input bits than A6's — so the ECS check on the A6 axis would either:
	//   - Never see HIGH (stalls the program indefinitely waiting for ECS)
	//   - Always see HIGH (fires finish signal immediately, skips ECS entirely)
	// Both outcomes cause incorrect machine behaviour that is impossible to
	// diagnose without knowing about the global driver bug.
	//
	// Hard error makes the real problem (Driver not set in InitMaster) visible
	// immediately at startup rather than causing silent wrong readings in the field.
	driver := masterDevice.Driver
	if driver == nil {
		return 0, fmt.Errorf("waitForECS: Driver not initialised for device %s — check InitMaster", masterDevice.Name)
	}

	isECSCheckInProgress.Store(true)
	ecsStat, err := driver.receivedECS(masterDevice, operation, stopECSCheckChan), nil
	isECSCheckInProgress.Store(false)
	return ecsStat, err
}

// -------------------------------------------------------------------
// Wait for ECS LOW (forever, no timeout)
// -------------------------------------------------------------------
func waitForECSZero(masterDevice *MasterDevice) (int, error) {
	operation, err := GetEtherCATOperation("ecs", masterDevice.Device.AddressConfigName)
	if err != nil {
		return 0, err
	}

	// FIX (Bug 3): Same fix as waitForECS — hard error instead of
	// GetMotorDriver() fallback. See waitForECS above for full explanation.
	driver := masterDevice.Driver
	if driver == nil {
		return 0, fmt.Errorf("waitForECSZero: Driver not initialised for device %s — check InitMaster", masterDevice.Name)
	}

	isECSCheckInProgress.Store(true)
	defer func() { isECSCheckInProgress.Store(false) }()

	logger.Info("Waiting indefinitely for ECS to go LOW via driver interface...")

	// Pass the stop channel to the driver so it can break out if the user hits Reset
	ecsStat := driver.receivedECSZero(masterDevice, operation, stopECSCheckChan)

	return ecsStat, nil
}

// -------------------------------------------------------------------
// Send finish signal to controller after motion complete
// -------------------------------------------------------------------
// sendECSFinSignal sends the ECS finish signal to the controller after motion
// completes. This toggles a digital output on the drive (object 0x60FE).
func sendECSFinSignal(device *MasterDevice) error {
	driverSettings := settings.GetDriverSettings(device.Name)
	if driverSettings.FinishSignal == 0 {
		return nil
	}

	// ==========================================
	// 1. PDO PATH (Delegated to Hardware Interface)
	// ==========================================
	if IsPDOActive() {
		logger.Info("[PDO] sendECSFinSignal: Delegating to active MotorDriver interface")

		// FIX (Bug 3): Hard error instead of GetMotorDriver() fallback.
		// On a mixed-axis setup the fallback would send the finish signal
		// using the wrong driver's digital output bit — either writing to
		// the wrong output pin or silently doing nothing, depending on the
		// drive type. Either way the external controller never receives the
		// finish pulse and the machine stalls waiting for an acknowledgement.
		driver := device.Driver
		if driver == nil {
			return fmt.Errorf("sendECSFinSignal: Driver not initialised for device %s — check InitMaster", device.Name)
		}

		notifyDriverStatus("fin_signal", "1", device)

		// Fetch the operation from config so the driver knows which bit to toggle
		operation, _ := GetEtherCATOperation("finsignal", device.Device.AddressConfigName)
		err := driver.sendFinishSignal(device, operation)

		notifyDriverStatus("fin_signal", "0", device)
		logger.Info("[PDO] sendECSFinSignal: fin signal sequence completed")
		return err
	}

	// ==========================================
	// 2. SDO PATH (Drive-Agnostic via YAML Configs)
	// ==========================================
	operation, _ := GetEtherCATOperation("finsignal", device.Device.AddressConfigName)
	operationEnd, err := GetEtherCATOperation("finsignalend", device.Device.AddressConfigName)
	if err != nil {
		return err
	}

	notifyDriverStatusWithWait("fin_signal", "1", device)

	for i, step := range operation.Steps {
		logger.Info("[SDO] sendECSFinSignal step", i, "action:", step.Action, "value:", step.Value, "addr:", step.Address, "sub:", step.SubIndex)
	}
	logger.Trace("start sending ecs fin signal ", device.Name)

	// Assert Fin Signal
	for _, step := range operation.Steps {
		if step.Action == "read" {
			SDOUpload2(device.Master, device.Position, step)
		} else {
			SDODownload(device.Master, device.Position, step)
		}
	}

	// Wait for configured time
	time.Sleep(time.Duration(driverSettings.ECSFinTiming) * time.Millisecond)

	// Release Fin Signal
	for _, step := range operationEnd.Steps {
		if step.Action == "read" {
			SDOUpload2(device.Master, device.Position, step)
		} else {
			SDODownload(device.Master, device.Position, step)
		}
	}

	notifyDriverStatusWithWait("fin_signal", "0", device)
	logger.Trace("finish sending ecs fin signal ", device.Name)
	return nil
}
