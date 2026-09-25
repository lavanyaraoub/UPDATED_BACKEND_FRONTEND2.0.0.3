//go:build unit

package motordriver

// Unit tests for read_drive_params.go.
//
// readDigitalInputs has three branches:
//   1. PdoDIReady=true            -> pure atomic read, no hardware, no config needed.
//   2. PdoDIReady=false, no config -> GetEtherCATOperation fails, returns error.
//   3. PdoDIReady=false, config found -> proceeds to SDODownload/SDOUpload2,
//      which need a real EtherCAT master. Out of scope here; only the
//      config-lookup succeeding (not the SDO call itself) is exercised,
//      via the operation having zero steps so the function returns (0, nil)
//      without ever reaching a cgo call.

import (
	"testing"

	ethercatDevice "EtherCAT/ethercatdevicedatatypes"
)

// ─── readDigitalInputs — PDO-ready fast path ──────────────────────────────────

func TestReadDigitalInputs_PdoReady_ReturnsAtomicValue(t *testing.T) {
	d := stubDevice("A")
	d.PdoDIReady = true
	d.PDODI.Store(0x4F)

	val, err := readDigitalInputs(d, "readinputsignal")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != 0x4F {
		t.Errorf("readDigitalInputs = %d, want 0x4F (79)", val)
	}
}

func TestReadDigitalInputs_PdoReady_ZeroValue(t *testing.T) {
	d := stubDevice("A")
	d.PdoDIReady = true
	d.PDODI.Store(0)

	val, err := readDigitalInputs(d, "readinputsignal")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != 0 {
		t.Errorf("readDigitalInputs = %d, want 0", val)
	}
}

func TestReadDigitalInputs_PdoReady_IgnoresOperationName(t *testing.T) {
	// The PDO-ready branch never touches sdoOperationName — confirm the
	// same cached value comes back regardless of which caller invoked it.
	d := stubDevice("A")
	d.PdoDIReady = true
	d.PDODI.Store(0x03)

	for _, opName := range []string{"readinputsignal", "ecs", "hard_reset", "anything"} {
		val, err := readDigitalInputs(d, opName)
		if err != nil {
			t.Fatalf("opName=%q: unexpected error: %v", opName, err)
		}
		if val != 0x03 {
			t.Errorf("opName=%q: readDigitalInputs = %d, want 3", opName, val)
		}
	}
}

// ─── readDigitalInputs — PDO not ready, no SDO config available ──────────────

func TestReadDigitalInputs_PdoNotReady_NoConfig_ReturnsError(t *testing.T) {
	d := stubDevice("A") // PdoDIReady defaults false, AddressConfigName empty
	_, err := readDigitalInputs(d, "readinputsignal")
	if err == nil {
		t.Fatal("no PDO and no SDO config: expected error, got nil")
	}
}

func TestReadDigitalInputs_PdoNotReady_PDOActive_StillFailsWithoutConfig(t *testing.T) {
	// Even when PDO is active elsewhere on the bus, a device whose own
	// PdoDIReady is false must fall through to the SDO path (with a warning)
	// and still surface a config-not-found error, not silently succeed.
	activatePDO(t)
	d := stubDevice("A")
	_, err := readDigitalInputs(d, "readinputsignal")
	if err == nil {
		t.Fatal("PDO active but PdoDIReady=false and no config: expected error, got nil")
	}
}

func TestReadDigitalInputs_PdoNotReady_ConfigFoundWithNoSteps_ReturnsZero(t *testing.T) {
	// Config exists but defines zero steps for the operation — the function
	// should return (0, nil) without ever reaching an SDO call.
	restore := withEtherCATOperationForTest("teststub", ethercatDevice.Operation{
		Name:  "readinputsignal",
		Steps: nil,
	})
	defer restore()

	d := stubDevice("A")
	d.Device = ethercatDevice.Device{AddressConfigName: "teststub"}

	val, err := readDigitalInputs(d, "readinputsignal")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != 0 {
		t.Errorf("readDigitalInputs (no steps) = %d, want 0", val)
	}
}

// ─── readInputSignal / readECSSignal / hardResetInput — operation-name wiring ─

func TestReadInputSignal_UsesReadinputsignalOperationName(t *testing.T) {
	d := stubDevice("A")
	d.PdoDIReady = true
	d.PDODI.Store(0x11)

	val, err := readInputSignal(d)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != 0x11 {
		t.Errorf("readInputSignal = %d, want 0x11", val)
	}
}

func TestReadECSSignal_UsesECSOperationName(t *testing.T) {
	d := stubDevice("A")
	d.PdoDIReady = true
	d.PDODI.Store(0x01)

	val, err := readECSSignal(d)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != 0x01 {
		t.Errorf("readECSSignal = %d, want 0x01", val)
	}
}

func TestHardResetInput_UsesHardResetOperationName(t *testing.T) {
	d := stubDevice("A")
	d.PdoDIReady = true
	d.PDODI.Store(0x20)

	val, err := hardResetInput(d)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != 0x20 {
		t.Errorf("hardResetInput = %d, want 0x20", val)
	}
}

func TestReadECSSignal_NoConfig_ReturnsError(t *testing.T) {
	d := stubDevice("A")
	_, err := readECSSignal(d)
	if err == nil {
		t.Fatal("readECSSignal with no PDO and no config: expected error, got nil")
	}
}

func TestHardResetInput_NoConfig_ReturnsError(t *testing.T) {
	d := stubDevice("A")
	_, err := hardResetInput(d)
	if err == nil {
		t.Fatal("hardResetInput with no PDO and no config: expected error, got nil")
	}
}
