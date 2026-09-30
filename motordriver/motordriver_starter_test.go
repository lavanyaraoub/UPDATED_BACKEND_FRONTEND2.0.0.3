//go:build unit

package motordriver

import (
	"errors"
	"strings"
	"testing"

	ethercatDevice "EtherCAT/ethercatdevicedatatypes"
	"EtherCAT/settings"
)

// Starter unit tests built against HAL's current motordriver architecture,
// using the mockDriver defined in mock_driver_test.go.
//
// Scope is deliberately safe:
// - no real EtherCAT bus access
// - no real PDO memory writes
// - no motor movement
// - no SDO download/upload calls
//
// These tests strengthen low-risk HAL coverage around:
// - ECS finish signal dispatch
// - per-device driver dispatch
// - ECS wait logic
// - setRpm guard/safe branch behavior
// - active-driver test injection

// ─── sendECSFinSignal ────────────────────────────────────────────────────
//
// FinishSignal == 0 returns before reading device.Driver or touching PDO/SDO.
// This is the safest no-op branch and must always stay safe.

func TestSendECSFinSignal_ZeroFinishSignalReturnsNil(t *testing.T) {
	settings.SetDriverSettings("A", settings.DriverSettings{FinishSignal: 0})

	d := &MasterDevice{Name: "A"}
	err := sendECSFinSignal(d)
	if err != nil {
		t.Errorf("sendECSFinSignal FinishSignal=0: got error %v, want nil", err)
	}
}

// When PDO is active and finish signal is enabled, HAL must use the driver
// attached to the specific MasterDevice. If that per-device driver is missing,
// return a hard error instead of silently falling back to the global driver.

func TestSendECSFinSignal_PDOActiveNilDriverReturnsExplicitError(t *testing.T) {
	activatePDO(t)

	settings.SetDriverSettings("A", settings.DriverSettings{
		FinishSignal: 1,
	})

	d := &MasterDevice{Name: "A", Driver: nil}

	err := sendECSFinSignal(d)
	if err == nil {
		t.Fatal("sendECSFinSignal with PDO active and nil device.Driver: got nil, want error")
	}
	if !strings.Contains(err.Error(), "Driver not initialised") {
		t.Errorf("sendECSFinSignal error = %q, want Driver not initialised", err.Error())
	}
}

// When PDO is active and a per-device driver exists, sendECSFinSignal must
// delegate to that driver and propagate the driver's error.

func TestSendECSFinSignal_PDOActivePropagatesDriverError(t *testing.T) {
	activatePDO(t)

	settings.SetDriverSettings("A", settings.DriverSettings{
		FinishSignal: 1,
	})

	wantErr := errors.New("finish output failed")
	d := &MasterDevice{
		Name:   "A",
		Driver: &mockDriver{finErr: wantErr},
	}

	err := sendECSFinSignal(d)
	if !errors.Is(err, wantErr) {
		t.Errorf("sendECSFinSignal error = %v, want %v", err, wantErr)
	}
}

// ─── stopECSCheck ────────────────────────────────────────────────────────

func TestStopECSCheck_DoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("stopECSCheck panicked: %v", r)
		}
	}()
	stopECSCheck()
}

func TestStopECSCheck_WhenInProgressClearsFlag(t *testing.T) {
	isECSCheckInProgress.Store(true)

	defer func() {
		isECSCheckInProgress.Store(false)
		// Drain the buffered stop signal if this test left one behind.
		select {
		case <-stopECSCheckChan:
		default:
		}
	}()

	stopECSCheck()

	if isECSCheckInProgress.Load() {
		t.Error("isECSCheckInProgress = true after stopECSCheck, want false")
	}
}

// ─── ECS wait helpers ────────────────────────────────────────────────────

func withEtherCATOperationForTest(configName string, operations ...ethercatDevice.Operation) func() {
	previous := ethercatAddressMapping

	ethercatAddressMapping = map[string]ethercatDevice.Ethercat{
		configName: {
			Operation: operations,
		},
	}

	return func() {
		ethercatAddressMapping = previous
	}
}

func TestWaitForECS_NilDriverReturnsExplicitError(t *testing.T) {
	restore := withEtherCATOperationForTest("testcfg", ethercatDevice.Operation{Name: "ecs"})
	defer restore()

	d := &MasterDevice{
		Name: "A",
		Device: ethercatDevice.Device{
			AddressConfigName: "testcfg",
		},
		Driver: nil,
	}

	got, err := waitForECS(d)
	if err == nil {
		t.Fatal("waitForECS with nil device.Driver: got nil error, want error")
	}
	if got != 0 {
		t.Errorf("waitForECS return code = %d, want 0 on error", got)
	}
	if !strings.Contains(err.Error(), "Driver not initialised") {
		t.Errorf("waitForECS error = %q, want Driver not initialised", err.Error())
	}
}

func TestWaitForECSZero_NilDriverReturnsExplicitError(t *testing.T) {
	restore := withEtherCATOperationForTest("testcfg", ethercatDevice.Operation{Name: "ecs"})
	defer restore()

	d := &MasterDevice{
		Name: "A",
		Device: ethercatDevice.Device{
			AddressConfigName: "testcfg",
		},
		Driver: nil,
	}

	got, err := waitForECSZero(d)
	if err == nil {
		t.Fatal("waitForECSZero with nil device.Driver: got nil error, want error")
	}
	if got != 0 {
		t.Errorf("waitForECSZero return code = %d, want 0 on error", got)
	}
	if !strings.Contains(err.Error(), "Driver not initialised") {
		t.Errorf("waitForECSZero error = %q, want Driver not initialised", err.Error())
	}
}

func TestWaitForECS_UsesPerDeviceDriverResult(t *testing.T) {
	restore := withEtherCATOperationForTest("testcfg", ethercatDevice.Operation{Name: "ecs"})
	defer restore()

	d := &MasterDevice{
		Name: "A",
		Device: ethercatDevice.Device{
			AddressConfigName: "testcfg",
		},
		Driver: &mockDriver{ecsResult: 1},
	}

	got, err := waitForECS(d)
	if err != nil {
		t.Fatalf("waitForECS returned unexpected error: %v", err)
	}
	if got != 1 {
		t.Errorf("waitForECS = %d, want 1 from per-device driver", got)
	}
}

func TestWaitForECSZero_UsesPerDeviceDriverResult(t *testing.T) {
	restore := withEtherCATOperationForTest("testcfg", ethercatDevice.Operation{Name: "ecs"})
	defer restore()

	d := &MasterDevice{
		Name: "A",
		Device: ethercatDevice.Device{
			AddressConfigName: "testcfg",
		},
		Driver: &mockDriver{ecsResult: 1},
	}

	got, err := waitForECSZero(d)
	if err != nil {
		t.Fatalf("waitForECSZero returned unexpected error: %v", err)
	}
	if got != 1 {
		t.Errorf("waitForECSZero = %d, want 1 from per-device driver", got)
	}
}

func TestDoECSCheck_ECSEnabledUsesPerDeviceDriver(t *testing.T) {
	restore := withEtherCATOperationForTest("testcfg", ethercatDevice.Operation{Name: "ecs"})
	defer restore()

	settings.SetDriverSettings("A", settings.DriverSettings{ECS: 1})

	d := &MasterDevice{
		Name: "A",
		Device: ethercatDevice.Device{
			AddressConfigName: "testcfg",
		},
		Driver: &mockDriver{ecsResult: 1},
	}

	got := doECSCheck(d, 90)
	if got != 1 {
		t.Errorf("doECSCheck = %d, want 1", got)
	}
}

func TestDoECSCheckZero_ECSEnabledUsesPerDeviceDriver(t *testing.T) {
	restore := withEtherCATOperationForTest("testcfg", ethercatDevice.Operation{Name: "ecs"})
	defer restore()

	settings.SetDriverSettings("A", settings.DriverSettings{ECS: 1})

	d := &MasterDevice{
		Name: "A",
		Device: ethercatDevice.Device{
			AddressConfigName: "testcfg",
		},
		Driver: &mockDriver{ecsResult: 1},
	}

	got := doECSCheckZero(d, 90)
	if got != 1 {
		t.Errorf("doECSCheckZero = %d, want 1", got)
	}
}

// ─── pollIOStat / stopPollIOStat — per-device dispatch ───────────────────
//
// This verifies that each MasterDevice is dispatched to its own Driver.
// This is important for HAL multi-drive support because A6, Delta, and Nidec
// must not share a single global IO polling implementation.

func TestPollIOStat_DispatchesToEachDevicesOwnDriver(t *testing.T) {
	mockA := &mockDriver{}
	mockB := &mockDriver{}
	devA := &MasterDevice{Name: "A", Driver: mockA}
	devB := &MasterDevice{Name: "B", Driver: mockB}

	pollIOStat([]*MasterDevice{devA, devB})

	if mockA.pollIOStatCalls != 1 {
		t.Errorf("mockA.pollIOStatCalls = %d, want 1", mockA.pollIOStatCalls)
	}
	if mockB.pollIOStatCalls != 1 {
		t.Errorf("mockB.pollIOStatCalls = %d, want 1", mockB.pollIOStatCalls)
	}
	if len(mockA.lastPolledDevices) != 1 || mockA.lastPolledDevices[0] != devA {
		t.Errorf("mockA was not dispatched exactly its own device")
	}
	if len(mockB.lastPolledDevices) != 1 || mockB.lastPolledDevices[0] != devB {
		t.Errorf("mockB was not dispatched exactly its own device")
	}
}

func TestPollIOStat_NilDeviceOrNilDriver_SkippedWithoutPanic(t *testing.T) {
	mockA := &mockDriver{}
	devA := &MasterDevice{Name: "A", Driver: mockA}
	devNilDriver := &MasterDevice{Name: "B", Driver: nil}

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("pollIOStat panicked on nil device/driver: %v", r)
		}
	}()

	pollIOStat([]*MasterDevice{devA, nil, devNilDriver})

	if mockA.pollIOStatCalls != 1 {
		t.Errorf("mockA.pollIOStatCalls = %d, want 1", mockA.pollIOStatCalls)
	}
}

func TestStopPollIOStat_StopsEveryDriverThatWasPolled(t *testing.T) {
	mockA := &mockDriver{}
	mockB := &mockDriver{}
	devA := &MasterDevice{Name: "A", Driver: mockA}
	devB := &MasterDevice{Name: "B", Driver: mockB}

	pollIOStat([]*MasterDevice{devA, devB})
	stopPollIOStat()

	if mockA.stopPollIOStatCalls != 1 {
		t.Errorf("mockA.stopPollIOStatCalls = %d, want 1", mockA.stopPollIOStatCalls)
	}
	if mockB.stopPollIOStatCalls != 1 {
		t.Errorf("mockB.stopPollIOStatCalls = %d, want 1", mockB.stopPollIOStatCalls)
	}
}

// ─── setRpm ───────────────────────────────────────────────────────────────
//
// These tests stay in safe Go-level branches. They do not call the C async
// SDO functions because PdoVelSdoReady remains false.

func TestSetRpm_PDOActiveButJogNotReady_ReturnsError(t *testing.T) {
	activatePDO(t)

	d := stubDevice("A")
	d.PdoJogReady = false
	d.Device.RPMConst = 1000

	err := setRpm(d, 20)
	if err == nil {
		t.Fatal("setRpm with PdoJogReady=false: got nil, want error")
	}
	if !strings.Contains(err.Error(), "PDO not active or RxPDO not ready") {
		t.Errorf("setRpm error = %q, want PDO readiness error", err.Error())
	}
}

func TestSetRpm_PDOActiveJogReadyCachesScaledVelocity(t *testing.T) {
	activatePDO(t)

	d := stubDevice("A")
	d.PdoJogReady = true
	d.PdoVelSdoReady = false // keep test out of C async-SDO path
	d.Device.RPMConst = 1000

	err := setRpm(d, 20)
	if err != nil {
		t.Fatalf("setRpm returned unexpected error: %v", err)
	}

	if got := d.desiredTargetVelocity.Load(); got != 20000 {
		t.Errorf("desiredTargetVelocity = %d, want 20000", got)
	}
}

// ─── JogControlword default behavior on mock ──────────────────────────────

func TestJogControlword_DefaultIsIdentity(t *testing.T) {
	m := &mockDriver{}
	got := m.JogControlword(0x000F)
	if got != 0x000F {
		t.Errorf("JogControlword default = 0x%04X, want identity 0x000F", got)
	}
}

func TestJogControlword_CanBeOverridden(t *testing.T) {
	m := &mockDriver{
		jogControlwordFn: func(cwBase uint16) uint16 {
			return cwBase | 0x0070
		},
	}

	got := m.JogControlword(0x000F)
	want := uint16(0x000F | 0x0070)

	if got != want {
		t.Errorf("JogControlword override = 0x%04X, want 0x%04X", got, want)
	}
}

// ─── setActiveDriverForTest ───────────────────────────────────────────────

func TestSetActiveDriverForTest_RestoresPreviousDriverOnRestore(t *testing.T) {
	before := GetMotorDriver()

	mock := &mockDriver{standbyOpMode: 3}
	restore := setActiveDriverForTest(mock)

	if GetMotorDriver() != IMotorDriver(mock) {
		t.Fatalf("GetMotorDriver() after injection did not return the mock")
	}
	if GetMotorDriver().StandbyOpMode() != 3 {
		t.Errorf("GetMotorDriver().StandbyOpMode() = %d, want 3", GetMotorDriver().StandbyOpMode())
	}

	restore()

	after := GetMotorDriver()
	if after != before {
		t.Errorf("setActiveDriverForTest did not restore the previous driver")
	}
}

// ─── motor_driver_factory.go — registry / active driver dispatch ───────────
//
// These tests cover the pure-Go HAL driver factory. They do not touch EtherCAT,
// PDO memory, SDO calls, or motor motion.

func restoreDriverFactoryForTest(t *testing.T) {
	t.Helper()

	driverRegistryMu.RLock()
	registryCopy := make(map[string]func() IMotorDriver, len(driverRegistry))
	for k, v := range driverRegistry {
		registryCopy[k] = v
	}
	driverRegistryMu.RUnlock()

	activeBefore := GetMotorDriver()

	t.Cleanup(func() {
		driverRegistryMu.Lock()
		driverRegistry = registryCopy
		driverRegistryMu.Unlock()

		restore := setActiveDriverForTest(activeBefore)
		restore()
	})
}

func TestRegisterDriver_RegistersNewDriveTypeForGetDriverForType(t *testing.T) {
	restoreDriverFactoryForTest(t)

	RegisterDriver("unit_mock_factory", func() IMotorDriver {
		return &mockDriver{standbyOpMode: 7}
	})

	driver := GetDriverForType("unit_mock_factory")

	mock, ok := driver.(*mockDriver)
	if !ok {
		t.Fatalf("GetDriverForType returned %T, want *mockDriver", driver)
	}

	if got := mock.StandbyOpMode(); got != 7 {
		t.Errorf("registered mock StandbyOpMode = %d, want 7", got)
	}
}

func TestSetMotorDriver_RegisteredTypeUpdatesGlobalActiveDriver(t *testing.T) {
	restoreDriverFactoryForTest(t)

	RegisterDriver("unit_active_mock", func() IMotorDriver {
		return &mockDriver{standbyOpMode: 5}
	})

	SetMotorDriver("unit_active_mock")

	driver := GetMotorDriver()

	mock, ok := driver.(*mockDriver)
	if !ok {
		t.Fatalf("GetMotorDriver returned %T, want *mockDriver", driver)
	}

	if got := mock.StandbyOpMode(); got != 5 {
		t.Errorf("active mock StandbyOpMode = %d, want 5", got)
	}
}

func TestGetDriverForType_UnknownTypeFallsBackToA6Minas(t *testing.T) {
	restoreDriverFactoryForTest(t)

	driver := GetDriverForType("unknown_drive_type_for_unit_test")

	if _, ok := driver.(*A6Minas); !ok {
		t.Fatalf("GetDriverForType unknown returned %T, want *A6Minas fallback", driver)
	}
}

func TestSetMotorDriver_UnknownTypeFallsBackToA6Minas(t *testing.T) {
	restoreDriverFactoryForTest(t)

	SetMotorDriver("unknown_active_drive_type_for_unit_test")

	driver := GetMotorDriver()

	if _, ok := driver.(*A6Minas); !ok {
		t.Fatalf("SetMotorDriver unknown made active driver %T, want *A6Minas fallback", driver)
	}
}

func TestGetDriverForType_ReturnsFreshInstanceEachCall(t *testing.T) {
	restoreDriverFactoryForTest(t)

	RegisterDriver("unit_fresh_instance_mock", func() IMotorDriver {
		return &mockDriver{}
	})

	first := GetDriverForType("unit_fresh_instance_mock")
	second := GetDriverForType("unit_fresh_instance_mock")

	if first == nil || second == nil {
		t.Fatalf("GetDriverForType returned nil instance(s): first=%v second=%v", first, second)
	}

	if first == second {
		t.Fatal("GetDriverForType returned same driver instance twice, want fresh per-device instances")
	}
}

func TestRegisterDriver_OverwriteExistingRegistration(t *testing.T) {
	restoreDriverFactoryForTest(t)

	RegisterDriver("unit_overwrite_mock", func() IMotorDriver {
		return &mockDriver{standbyOpMode: 1}
	})

	RegisterDriver("unit_overwrite_mock", func() IMotorDriver {
		return &mockDriver{standbyOpMode: 9}
	})

	driver := GetDriverForType("unit_overwrite_mock")

	mock, ok := driver.(*mockDriver)
	if !ok {
		t.Fatalf("GetDriverForType returned %T, want *mockDriver", driver)
	}

	if got := mock.StandbyOpMode(); got != 9 {
		t.Errorf("overwritten mock StandbyOpMode = %d, want 9", got)
	}
}
