package motordriver

import (
	"sync"
	"sync/atomic"
	"unsafe"
)

// activeDriverPtr holds the currently selected IMotorDriver implementation.
// Stored as unsafe.Pointer for atomic swap without a mutex.
var activeDriverPtr unsafe.Pointer

// driverRegistry maps drive-type strings to constructor functions.
//
// TO ADD A NEW DRIVE: add one line here.
//   "<drive-type-string>": func() IMotorDriver { return &YourNewDriver{} },
//
// The drive-type string must match the value of "drive-type" in
// configs/device-configuration.yml.
// No other file needs editing to register a new drive.
var (
	driverRegistryMu sync.RWMutex
	driverRegistry   = map[string]func() IMotorDriver{
		"a6_minas":     func() IMotorDriver { return &A6Minas{} },
		"delta_asda2e": func() IMotorDriver { return &DeltaASDA2E{} },
		"Nidec":        func() IMotorDriver { return &NidecM700{} }, // Added Nidec M700 mapping
	}
)

func init() {
	d := IMotorDriver(&A6Minas{})
	atomic.StorePointer(&activeDriverPtr, unsafe.Pointer(&d))
}

// RegisterDriver allows a driver file to self-register at package init time.
// Calling this from a driver's init() means motor_driver_factory.go never
// needs to import or know about that specific driver type directly.
//
// Example — in your_drive_motor_driver.go:
//   func init() {
//       RegisterDriver("your_drive_type", func() IMotorDriver { return &YourDrive{} })
//   }
func RegisterDriver(driveType string, constructor func() IMotorDriver) {
	driverRegistryMu.Lock()
	defer driverRegistryMu.Unlock()
	driverRegistry[driveType] = constructor
}

// SetMotorDriver selects the active IMotorDriver by drive-type string.
// Looks up the registry — no hard-coded switch, no knowledge of specific drives.
// Falls back to A6Minas if the drive type is not registered (safe default).
// Must be called before InitMaster begins PDO setup.
func SetMotorDriver(driveType string) {
	driverRegistryMu.RLock()
	constructor, ok := driverRegistry[driveType]
	driverRegistryMu.RUnlock()

	var d IMotorDriver
	if ok {
		d = constructor()
	} else {
		d = &A6Minas{}
	}
	atomic.StorePointer(&activeDriverPtr, unsafe.Pointer(&d))
}

// GetMotorDriver returns the currently active IMotorDriver implementation.
// This is the global (single-drive) accessor — preserved for backward
// compatibility with all callers that do not yet carry a device reference.
// The cyclic task uses MasterDevice.Driver instead for per-axis dispatch.
func GetMotorDriver() IMotorDriver {
	return *(*IMotorDriver)(atomic.LoadPointer(&activeDriverPtr))
}

// GetDriverForType instantiates and returns the IMotorDriver for a given
// drive-type string without changing the global activeDriverPtr.
//
// Phase 4 use: InitMaster calls this once per device in the device loop
// and stores the result in masterDevice.Driver, giving each MasterDevice
// its own driver instance. This is what allows a Delta on Axis A and a
// Panasonic on Axis B to coexist in the same cyclic task — each device
// calls d.Driver.JogControlword() and gets the right behaviour for its
// specific hardware.
//
// Falls back to A6Minas (same as SetMotorDriver) for unknown drive types.
func GetDriverForType(driveType string) IMotorDriver {
	driverRegistryMu.RLock()
	constructor, ok := driverRegistry[driveType]
	driverRegistryMu.RUnlock()
	if ok {
		return constructor()
	}
	return &A6Minas{}
}