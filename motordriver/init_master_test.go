//go:build unit

package motordriver

// Unit tests for init_master.go.
//
// InitMaster() itself is the full hardware bring-up sequence (RequestMaster,
// configureDriver, PowerOn, SetupPDOPosition, StartPDOCyclic, ...) and calls
// into the real EtherCAT master almost immediately — it is not unit-testable
// without a live bus and is intentionally out of scope here.
//
// ScanBus() calls C.scan_bus directly — also out of scope.
//
// Everything below is either pure Go (ValidateBusConfig, HasDriverConnected,
// getMasterDevices, getEtherCATAddress, GetEtherCATOperation) or a guard
// clause that returns/no-ops before any C.ecrt_*/SDO call is reached
// (ForceReleaseMaster, waitForFaultClearSDO, waitForReadyToSwitchOnSDO,
// initListeners, ShutdownMasters, PowerOnMasters).

import (
	"strings"
	"testing"
	"time"

	ethercatDevice "EtherCAT/ethercatdevicedatatypes"
)

// ─── ValidateBusConfig — all-match, no errors ─────────────────────────────────

func TestValidateBusConfig_AllDevicesMatch_ReturnsEmptyString(t *testing.T) {
	scanned := []SlaveOnBus{
		{Position: 0, VendorID: 0x0000066F, ProductCode: 0x60380008, Name: "A6"},
		{Position: 1, VendorID: 0x000001DD, ProductCode: 0x10305070, Name: "Delta"},
	}
	configured := []ethercatDevice.Device{
		{Name: "A", ID: 0, VendorID: 0x0000066F, ProductCode: 0x60380008},
		{Name: "B", ID: 1, VendorID: 0x000001DD, ProductCode: 0x10305070},
	}

	if got := ValidateBusConfig(scanned, configured); got != "" {
		t.Errorf("all devices match: got %q, want empty string", got)
	}
}

func TestValidateBusConfig_EmptyBothSides_ReturnsEmptyString(t *testing.T) {
	if got := ValidateBusConfig(nil, nil); got != "" {
		t.Errorf("empty scanned+configured: got %q, want empty string", got)
	}
}

// ─── ValidateBusConfig — slave count mismatch (configured device not found) ──

func TestValidateBusConfig_ConfiguredDeviceNotOnBus_ReportsNotFound(t *testing.T) {
	scanned := []SlaveOnBus{} // nothing on the bus at all
	configured := []ethercatDevice.Device{
		{Name: "A", ID: 0, VendorID: 0x0000066F, ProductCode: 0x60380008},
	}

	got := ValidateBusConfig(scanned, configured)
	if !strings.Contains(got, "NOT FOUND") {
		t.Errorf("got %q, want it to contain NOT FOUND", got)
	}
	if !strings.Contains(got, "Axis A") {
		t.Errorf("got %q, want it to name Axis A", got)
	}
}

func TestValidateBusConfig_SecondAxisMissing_OnlySecondIsReported(t *testing.T) {
	scanned := []SlaveOnBus{
		{Position: 0, VendorID: 0x0000066F, ProductCode: 0x60380008, Name: "A6"},
	}
	configured := []ethercatDevice.Device{
		{Name: "A", ID: 0, VendorID: 0x0000066F, ProductCode: 0x60380008},
		{Name: "B", ID: 1, VendorID: 0x000001DD, ProductCode: 0x10305070},
	}

	got := ValidateBusConfig(scanned, configured)
	if !strings.Contains(got, "Axis B") || !strings.Contains(got, "NOT FOUND") {
		t.Errorf("got %q, want NOT FOUND reported for Axis B only", got)
	}
	if strings.Contains(got, "Axis A") {
		t.Errorf("got %q, Axis A matched and should not be reported", got)
	}
}

// ─── ValidateBusConfig — wrong vendor/product ID (MISMATCH) ──────────────────

func TestValidateBusConfig_WrongVendorID_ReportsMismatch(t *testing.T) {
	scanned := []SlaveOnBus{
		{Position: 0, VendorID: 0x0000066F, ProductCode: 0x535300A1, Name: "WrongDrive"},
	}
	configured := []ethercatDevice.Device{
		{Name: "A", ID: 0, VendorID: 0x000001DD, ProductCode: 0x10305070}, // expects Delta
	}

	got := ValidateBusConfig(scanned, configured)
	if !strings.Contains(got, "MISMATCH") {
		t.Errorf("got %q, want it to contain MISMATCH", got)
	}
}

func TestValidateBusConfig_RightVendorWrongProductCode_ReportsMismatch(t *testing.T) {
	scanned := []SlaveOnBus{
		{Position: 0, VendorID: 0x0000066F, ProductCode: 0x00000001, Name: "SomeOtherPanasonicDrive"},
	}
	configured := []ethercatDevice.Device{
		{Name: "A", ID: 0, VendorID: 0x0000066F, ProductCode: 0x60380008}, // expects A6 Minas specifically
	}

	got := ValidateBusConfig(scanned, configured)
	if !strings.Contains(got, "MISMATCH") {
		t.Errorf("got %q, want it to contain MISMATCH", got)
	}
}

func TestValidateBusConfig_MismatchMessageIncludesBothConfigAndBusValues(t *testing.T) {
	scanned := []SlaveOnBus{
		{Position: 0, VendorID: 0x0000066F, ProductCode: 0x535300A1, Name: "FoundThis"},
	}
	configured := []ethercatDevice.Device{
		{Name: "A", ID: 0, VendorID: 0x000001DD, ProductCode: 0x10305070},
	}

	got := ValidateBusConfig(scanned, configured)
	for _, want := range []string{"000001DD", "10305070", "0000066F", "535300A1", "FoundThis"} {
		if !strings.Contains(got, want) {
			t.Errorf("mismatch message %q missing expected substring %q", got, want)
		}
	}
}

// ─── ValidateBusConfig — extra slaves are warnings only, not errors ──────────

func TestValidateBusConfig_ExtraSlavesBeyondConfigured_AreNotReportedAsErrors(t *testing.T) {
	scanned := []SlaveOnBus{
		{Position: 0, VendorID: 0x0000066F, ProductCode: 0x60380008, Name: "A6"},
		{Position: 1, VendorID: 0x000001DD, ProductCode: 0x10305070, Name: "UnconfiguredExtraDrive"},
	}
	configured := []ethercatDevice.Device{
		{Name: "A", ID: 0, VendorID: 0x0000066F, ProductCode: 0x60380008},
	}

	if got := ValidateBusConfig(scanned, configured); got != "" {
		t.Errorf("extra unconfigured slave should not produce an error string, got %q", got)
	}
}

// ─── ValidateBusConfig — multiple configured devices, mixed results ──────────

func TestValidateBusConfig_MultipleDevices_OnlyProblemsAreJoined(t *testing.T) {
	scanned := []SlaveOnBus{
		{Position: 0, VendorID: 0x0000066F, ProductCode: 0x60380008, Name: "A6"},       // matches A
		{Position: 1, VendorID: 0x0000066F, ProductCode: 0x535300A1, Name: "WrongOne"}, // mismatches B
	}
	configured := []ethercatDevice.Device{
		{Name: "A", ID: 0, VendorID: 0x0000066F, ProductCode: 0x60380008},
		{Name: "B", ID: 1, VendorID: 0x000001DD, ProductCode: 0x10305070},
		{Name: "C", ID: 2, VendorID: 0x000000F9, ProductCode: 0x01000102}, // not on bus at all
	}

	got := ValidateBusConfig(scanned, configured)
	if strings.Contains(got, "Axis A") {
		t.Errorf("Axis A matched and should not appear in %q", got)
	}
	if !strings.Contains(got, "Axis B") || !strings.Contains(got, "MISMATCH") {
		t.Errorf("Axis B should be reported as MISMATCH, got %q", got)
	}
	if !strings.Contains(got, "Axis C") || !strings.Contains(got, "NOT FOUND") {
		t.Errorf("Axis C should be reported as NOT FOUND, got %q", got)
	}
	// Both problems should be present, joined.
	if !strings.Contains(got, ";") {
		t.Errorf("expected multiple problems joined with '; ', got %q", got)
	}
}

// ─── ForceReleaseMaster — guard clauses (empty slice / nil Master) ───────────

func TestForceReleaseMaster_EmptyMasterDevices_NoPanic(t *testing.T) {
	restore := setMasterDevicesForTest() // empty slice
	defer restore()

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("ForceReleaseMaster panicked on empty masterDevices: %v", r)
		}
	}()
	ForceReleaseMaster()
}

func TestForceReleaseMaster_NilMasterPointer_NoPanic(t *testing.T) {
	d := stubDevice("A") // Master field is the zero value (nil cgo pointer)
	restore := setMasterDevicesForTest(d)
	defer restore()

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("ForceReleaseMaster panicked on nil Master pointer: %v", r)
		}
	}()
	ForceReleaseMaster()
}

// ─── waitForFaultClearSDO / waitForReadyToSwitchOnSDO — no-config fallback ───

func TestWaitForFaultClearSDO_NoConfig_FallsBackAndReturnsTrue(t *testing.T) {
	d := stubDevice("A") // AddressConfigName empty, no config registered

	start := time.Now()
	got := waitForFaultClearSDO(d, time.Second)
	elapsed := time.Since(start)

	if !got {
		t.Error("no readStatusword config: expected fallback to return true")
	}
	// Fallback sleeps 600ms; allow generous slack for a loaded CI/Pi.
	if elapsed < 500*time.Millisecond {
		t.Errorf("fallback returned too fast (%v) — expected ~600ms sleep", elapsed)
	}
}

func TestWaitForReadyToSwitchOnSDO_NoConfig_FallsBackAndReturnsTrue(t *testing.T) {
	d := stubDevice("A")

	start := time.Now()
	got := waitForReadyToSwitchOnSDO(d, time.Second)
	elapsed := time.Since(start)

	if !got {
		t.Error("no readStatusword config: expected fallback to return true")
	}
	if elapsed < 400*time.Millisecond {
		t.Errorf("fallback returned too fast (%v) — expected ~500ms sleep", elapsed)
	}
}

// ─── initListeners — empty-devices guard ─────────────────────────────────────

func TestInitListeners_EmptyDevices_ReturnsImmediatelyWithoutStartingGoroutines(t *testing.T) {
	// If this guard clause is ever removed, initListeners would reach
	// initDriverActionListener/pollDrivePosition/etc. with zero devices,
	// which is a different (and untested-here) code path. This test's job
	// is only to confirm the empty case is a true no-op.
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("initListeners([], ...) panicked: %v", r)
		}
	}()
	done := make(chan struct{})
	go func() {
		initListeners([]*MasterDevice{}, false)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Error("initListeners([], ...) did not return promptly — guard clause may be missing")
	}
}

// ─── ShutdownMasters — PDO-not-active guard ──────────────────────────────────

func TestShutdownMasters_PDONotActive_ReturnsImmediately(t *testing.T) {
	setPDOInactive(t)
	restore := setMasterDevicesForTest() // no devices needed; guard fires first
	defer restore()

	done := make(chan struct{})
	go func() {
		ShutdownMasters()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Error("ShutdownMasters() with PDO inactive did not return promptly")
	}
}

// ─── PowerOnMasters — empty slice, and PDO-active delegation ─────────────────

func TestPowerOnMasters_EmptyMasterDevices_NoOp(t *testing.T) {
	restore := setMasterDevicesForTest()
	defer restore()

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("PowerOnMasters panicked on empty masterDevices: %v", r)
		}
	}()
	PowerOnMasters()
}

func TestPowerOnMasters_PDOActive_DelegatesToPowerOnWithoutSDO(t *testing.T) {
	// PowerOn(dev) itself no-ops (returns nil) when PDO is active, so this
	// exercises PowerOnMasters' iteration without ever reaching an SDO call.
	activatePDO(t)
	d := stubDevice("A")
	restore := setMasterDevicesForTest(d)
	defer restore()

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("PowerOnMasters panicked with PDO active: %v", r)
		}
	}()
	PowerOnMasters()
}

// ─── HasDriverConnected ───────────────────────────────────────────────────────

func TestHasDriverConnected_SuccessStatus_ReturnsTrue(t *testing.T) {
	prev := driverConnectionStatus
	driverConnectionStatus = "SUCCESS"
	t.Cleanup(func() { driverConnectionStatus = prev })

	if !HasDriverConnected() {
		t.Error("driverConnectionStatus=SUCCESS: expected true")
	}
}

func TestHasDriverConnected_OtherStatus_ReturnsFalse(t *testing.T) {
	prev := driverConnectionStatus
	driverConnectionStatus = "FAILED"
	t.Cleanup(func() { driverConnectionStatus = prev })

	if HasDriverConnected() {
		t.Error("driverConnectionStatus=FAILED: expected false")
	}
}

// ─── getMasterDevices ─────────────────────────────────────────────────────────

func TestGetMasterDevices_ReturnsPackageLevelSlice(t *testing.T) {
	d := stubDevice("A")
	restore := setMasterDevicesForTest(d)
	defer restore()

	got := getMasterDevices()
	if len(got) != 1 || got[0] != d {
		t.Errorf("getMasterDevices() = %+v, want [d]", got)
	}
}

func TestGetMasterDevices_Empty_ReturnsEmpty(t *testing.T) {
	restore := setMasterDevicesForTest()
	defer restore()

	if got := getMasterDevices(); len(got) != 0 {
		t.Errorf("getMasterDevices() = %+v, want empty", got)
	}
}

// ─── getEtherCATAddress / GetEtherCATOperation ───────────────────────────────

func TestGetEtherCATAddress_NilMap_ReturnsZeroValue(t *testing.T) {
	prev := ethercatAddressMapping
	ethercatAddressMapping = nil
	t.Cleanup(func() { ethercatAddressMapping = prev })

	got := getEtherCATAddress("anything_not_registered")
	if len(got.Operation) != 0 {
		t.Errorf("unregistered config name on nil map: got %+v, want zero-value Ethercat", got)
	}
}

func TestGetEtherCATAddress_KnownName_ReturnsRegisteredEntry(t *testing.T) {
	restore := withEtherCATOperationForTest("delta_asda2e", ethercatDevice.Operation{Name: "poweron"})
	defer restore()

	got := getEtherCATAddress("delta_asda2e")
	if len(got.Operation) != 1 || got.Operation[0].Name != "poweron" {
		t.Errorf("got %+v, want one operation named poweron", got)
	}
}

func TestGetEtherCATOperation_DelegatesToRegisteredAddress(t *testing.T) {
	restore := withEtherCATOperationForTest("delta_asda2e",
		ethercatDevice.Operation{Name: "poweron", Steps: []ethercatDevice.Step{{Name: "step1"}}})
	defer restore()

	op, err := GetEtherCATOperation("poweron", "delta_asda2e")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if op.Name != "poweron" || len(op.Steps) != 1 {
		t.Errorf("got %+v, want poweron operation with 1 step", op)
	}
}

func TestGetEtherCATOperation_UnknownOperationName_ReturnsError(t *testing.T) {
	restore := withEtherCATOperationForTest("delta_asda2e", ethercatDevice.Operation{Name: "poweron"})
	defer restore()

	_, err := GetEtherCATOperation("nonexistent_operation", "delta_asda2e")
	if err == nil {
		t.Fatal("unknown operation name: expected error, got nil")
	}
}

func TestGetEtherCATOperation_UnknownConfigName_ReturnsError(t *testing.T) {
	restore := withEtherCATOperationForTest("delta_asda2e", ethercatDevice.Operation{Name: "poweron"})
	defer restore()

	_, err := GetEtherCATOperation("poweron", "unregistered_drive")
	if err == nil {
		t.Fatal("unknown config name: expected error, got nil")
	}
}
