//go:build unit

package motordriver

// mockDriver implements the CURRENT IMotorDriver interface (16 methods) for
// unit tests. All methods return safe, injectable zero values or configured
// stub values — no CGo, no hardware, no real MasterDevice fields are read.
//
// ── WHY THIS FILE EXISTS ─────────────────────────────────────────────────
// testenv's original mock_driver_test.go was written against an 8-method
// version of IMotorDriver and injected itself via a package-level
// `speclDriver` variable. Neither of those things exist in this codebase
// anymore:
//
//   - IMotorDriver grew 6 more methods (SetupPDO, IsTargetReached,
//     JogControlword, FaultResetControlword, StandbyOpMode, ResetMultiTurn)
//     to support the Panasonic A6 / Delta ASDA-A2-E / Nidec M700 multi-drive
//     abstraction. Any type claiming to implement IMotorDriver must provide
//     all 16 or it won't compile.
//
//   - The old global-driver mechanism was replaced by a lock-free atomic
//     pointer + registry (motor_driver_factory.go: activeDriverPtr,
//     driverRegistry, RegisterDriver, GetDriverForType). There is no
//     `speclDriver` package variable to assign to. Use setActiveDriverForTest
//     (below) to inject a mock for GetMotorDriver()-based code paths, and
//     construct a MasterDevice with Driver: mock set directly for
//     per-device code paths (clamp_declamp.go, ecs.go, pdo_cyclic_task.go
//     all read masterDevice.Driver, not the global).
//
//   - pollIOStat/stopPollIOStat are now per-device dispatch
//     (poll_iostat.go: pollIOStat iterates availableDevices and calls
//     dev.Driver.pollIOStat(...) on each one, tracking started drivers in
//     the package-level `polledDrivers` slice for stopPollIOStat to stop
//     correctly). There is nothing to inject globally for these two —
//     they're exercised by putting the mock on a MasterDevice.Driver and
//     calling the package-level pollIOStat([]*MasterDevice{dev}) directly.
//
// IMPORTANT CAVEAT: MasterDevice embeds `Master *C.ec_master_t` directly
// (see ether_cat_gateway.go). This means constructing so much as a zero-value
// `&MasterDevice{}` in a test requires the real cgo toolchain (ecrt.h,
// libethercat.a from /opt/etherlab) to even type-check — this file, and any
// test file that constructs a MasterDevice, can ONLY be compiled on a
// machine with that toolchain installed (e.g. the Pi). It will never build
// in a generic CI runner or sandbox without it.
import (
	"sync/atomic"
	"testing"
	"unsafe"

	ethercatDevice "EtherCAT/ethercatdevicedatatypes"
)

// mockDriver is a fully-configurable stand-in for any real IMotorDriver
// implementation. Every field is a return value (or pair of return values)
// for the correspondingly-named method, letting a test set up whatever
// scenario it needs before invoking the code under test.
type mockDriver struct {
	// hasTargetReached
	targetReachedErr error

	// potNotEnabled
	potResult bool
	potErr    error

	// readDeclampSignal
	declampResult bool
	declampErr    error

	// readClampSignal
	clampResult bool
	clampErr    error

	// receivedECS / receivedECSZero
	ecsResult int

	// sendFinishSignal
	finErr error

	// pollIOStat / stopPollIOStat call tracking
	pollIOStatCalls     int
	stopPollIOStatCalls int
	lastPolledDevices   []*MasterDevice

	// SetupPDO
	setupPDOErr error

	// IsTargetReached
	targetReached bool

	// JogControlword — identity by default; set jogControlwordFn to override
	jogControlwordFn func(cwBase uint16) uint16

	// FaultResetControlword
	faultResetControlword uint16

	// StandbyOpMode
	standbyOpMode int8

	// ResetMultiTurn
	resetMultiTurnErr error
}

// ── Legacy SDO-based methods ────────────────────────────────────────────

func (m *mockDriver) hasTargetReached(_ *MasterDevice, _ int, _ int, _ ethercatDevice.Operation) error {
	return m.targetReachedErr
}

func (m *mockDriver) potNotEnabled(_ *MasterDevice) (bool, error) {
	return m.potResult, m.potErr
}

func (m *mockDriver) readDeclampSignal(_ *MasterDevice, _ int) (bool, error) {
	return m.declampResult, m.declampErr
}

func (m *mockDriver) readClampSignal(_ *MasterDevice, _ int) (bool, error) {
	return m.clampResult, m.clampErr
}

func (m *mockDriver) receivedECS(_ *MasterDevice, _ ethercatDevice.Operation, _ chan bool) int {
	return m.ecsResult
}

func (m *mockDriver) receivedECSZero(_ *MasterDevice, _ ethercatDevice.Operation, _ chan bool) int {
	return m.ecsResult
}

func (m *mockDriver) sendFinishSignal(_ *MasterDevice, _ ethercatDevice.Operation) error {
	return m.finErr
}

func (m *mockDriver) pollIOStat(availableDevices []*MasterDevice) {
	m.pollIOStatCalls++
	m.lastPolledDevices = availableDevices
}

func (m *mockDriver) stopPollIOStat() {
	m.stopPollIOStatCalls++
}

// ── New (Phase 4+) methods — required by the current IMotorDriver ──────────

func (m *mockDriver) SetupPDO(_ *MasterDevice) error {
	return m.setupPDOErr
}

func (m *mockDriver) IsTargetReached(_ uint16) bool {
	return m.targetReached
}

func (m *mockDriver) JogControlword(cwBase uint16) uint16 {
	if m.jogControlwordFn != nil {
		return m.jogControlwordFn(cwBase)
	}
	return cwBase // identity by default, matching A6Minas's behavior
}

func (m *mockDriver) FaultResetControlword() uint16 {
	return m.faultResetControlword
}

func (m *mockDriver) StandbyOpMode() int8 {
	return m.standbyOpMode
}

func (m *mockDriver) ResetMultiTurn(_ []*MasterDevice) error {
	return m.resetMultiTurnErr
}

// ── Compile-time interface satisfaction check ───────────────────────────
// If IMotorDriver ever grows another method, this line fails to compile
// with a clear "does not implement" error instead of a confusing failure
// somewhere a mock is used.
var _ IMotorDriver = (*mockDriver)(nil)

// ── Test injection helpers ──────────────────────────────────────────────
//
// setActiveDriverForTest swaps the package's global active driver (the one
// GetMotorDriver() returns) for the duration of a test, and returns a
// restore function to defer. This targets code paths that call
// GetMotorDriver() directly rather than going through masterDevice.Driver.
//
// Usage:
//
//	restore := setActiveDriverForTest(&mockDriver{standbyOpMode: 3})
//	defer restore()
func setActiveDriverForTest(d IMotorDriver) (restore func()) {
	previous := GetMotorDriver()
	var iface IMotorDriver = d
	atomic.StorePointer(&activeDriverPtr, unsafe.Pointer(&iface))
	return func() {
		var prevIface IMotorDriver = previous
		atomic.StorePointer(&activeDriverPtr, unsafe.Pointer(&prevIface))
	}
}

// stubDevice constructs a minimal *MasterDevice for tests. All cgo-backed
// fields (Master, Domain, SlaveConfig, DomainPD, the PDO offset fields) are
// left at their zero values (nil / 0) — safe as long as the code under test
// doesn't dereference them. Functions guarded by readiness flags (e.g.
// SetTargetPositionPDO checks PdoPosReady before touching PDO memory) are
// safe to call on a stub; functions that unconditionally touch DomainPD or
// C types are NOT — check before using a stub against a new function.
func stubDevice(name string) *MasterDevice {
	return &MasterDevice{Name: name}
}

// setMasterDevicesForTest swaps the package-level masterDevices slice (read
// by getMasterDevices()/legacyFirstDevice(), populated in production by
// InitMaster) for the duration of a test, and returns a restore function.
//
// Usage:
//
//	restore := setMasterDevicesForTest(stubDevice("A"))
//	defer restore()
func setMasterDevicesForTest(devices ...*MasterDevice) (restore func()) {
	previous := masterDevices
	masterDevices = devices
	return func() {
		masterDevices = previous
	}
}

// activatePDO sets pdoActive=true for the duration of a test and restores it
// to false on cleanup. Many functions have an early-return guard for
// "PDO not active" — use this to get past that guard and exercise the
// PDO-active branch instead.
func activatePDO(t *testing.T) {
	t.Helper()
	pdoActive.Store(true)
	t.Cleanup(func() { pdoActive.Store(false) })
}
