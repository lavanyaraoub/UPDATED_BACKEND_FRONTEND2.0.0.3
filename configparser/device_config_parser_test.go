//go:build unit

package configparser

// Tests for device_config_parser.go.
//
// We test the *FromReader variant exclusively. The file-reading wrappers
// (ParseDeviceConfig) are thin layers around the reader variants and
// add only os.Open + defer Close, which doesn't merit a unit test —
// it would just be testing the standard library.

import (
	"strings"
	"testing"
)

// ─── Happy path: real-world YAML from production configs/ ─────────────────

// The actual device-configuration.yml content (minus the commented-out
// device B). If this test ever fails, either the YAML format changed or
// the Device struct tags changed.
const validDeviceConfigYAML = `---
devices:
    - device:
      name: A
      vendor-id: 0x0000066f
      product-code: 0x60380008
      rpm-const: 120000.006
      drive-x-ratio: 20000
      alias: 0
      id: 0
      address-config-name: a6minas
      address-config-file: "/configs/a6minas.yml"
      pot-not-threshold: 1.5
      stop-when-hardware-potnot: true
      io-poll-interval: 10000
`

func TestParseDeviceConfigFromReader_ValidYAML(t *testing.T) {
	devices, err := ParseDeviceConfigFromReader(strings.NewReader(validDeviceConfigYAML))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if len(devices.Device) != 1 {
		t.Fatalf("expected 1 device, got %d", len(devices.Device))
	}

	d := devices.Device[0]
	if d.Name != "A" {
		t.Errorf("Name: got %q, want %q", d.Name, "A")
	}
	if d.VendorID != 0x0000066f {
		t.Errorf("VendorID: got 0x%X, want 0x%X", d.VendorID, 0x0000066f)
	}
	if d.ProductCode != 0x60380008 {
		t.Errorf("ProductCode: got 0x%X, want 0x%X", d.ProductCode, 0x60380008)
	}
	// DriveXRatio is deliberately NOT yaml-tagged in this codebase: it is
	// injected at runtime by auto-discovery from the drive YAML's `motion:`
	// section, not read from device-configuration.yml. Parsing this file
	// alone must leave it at the zero value — see ethercatdevicedatatypes
	// device_config.go for the rationale.
	if d.DriveXRatio != 0 {
		t.Errorf("DriveXRatio: got %d, want 0 (not sourced from device-configuration.yml by design)", d.DriveXRatio)
	}
	if d.AddressConfigName != "a6minas" {
		t.Errorf("AddressConfigName: got %q, want %q", d.AddressConfigName, "a6minas")
	}
	if d.AddressConfigFile != "/configs/a6minas.yml" {
		t.Errorf("AddressConfigFile: got %q, want %q", d.AddressConfigFile, "/configs/a6minas.yml")
	}
	if d.PotNotThreshold != 1.5 {
		t.Errorf("PotNotThreshold: got %v, want 1.5", d.PotNotThreshold)
	}
	if !d.StopWhenHWPOTNOT {
		t.Errorf("StopWhenHWPOTNOT: got false, want true")
	}
	if d.IOPollingInterval != 10000 {
		t.Errorf("IOPollingInterval: got %d, want 10000", d.IOPollingInterval)
	}
}

// ─── RPMConst is not sourced from device-configuration.yml ────────────────
//
// The YAML stores `rpm-const: 120000.006`, but in this codebase RPMConst is
// deliberately NOT yaml-tagged on the Device struct — it's injected at
// runtime by auto-discovery from the drive YAML's `motion:` section instead,
// so that a value in device-configuration.yml can never silently shadow the
// drive-specific value. Parsing alone must always leave it at zero.

func TestParseDeviceConfigFromReader_RPMConstFloatValueBehavior(t *testing.T) {
	devices, err := ParseDeviceConfigFromReader(strings.NewReader(validDeviceConfigYAML))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if len(devices.Device) != 1 {
		t.Fatalf("expected 1 device, got %d", len(devices.Device))
	}

	got := devices.Device[0].RPMConst
	if got != 0 {
		t.Errorf("RPMConst = %d, want 0 (not sourced from device-configuration.yml by design — "+
			"injected at runtime from the drive YAML instead)", got)
	}
}

// ─── Empty input edge cases ────────────────────────────────────────────────

func TestParseDeviceConfigFromReader_EmptyInput(t *testing.T) {
	devices, err := ParseDeviceConfigFromReader(strings.NewReader(""))
	// Empty YAML is valid — should produce a zero-value struct without error
	if err != nil {
		t.Fatalf("empty input should not error, got: %v", err)
	}
	if len(devices.Device) != 0 {
		t.Errorf("empty input should produce 0 devices, got %d", len(devices.Device))
	}
}

func TestParseDeviceConfigFromReader_NoDevicesArray(t *testing.T) {
	// YAML valid but without the "devices:" key
	yaml := `---
something_else: value
`
	devices, err := ParseDeviceConfigFromReader(strings.NewReader(yaml))
	if err != nil {
		t.Fatalf("YAML without devices key should not error, got: %v", err)
	}
	if len(devices.Device) != 0 {
		t.Errorf("missing devices key should produce 0 devices, got %d", len(devices.Device))
	}
}

func TestParseDeviceConfigFromReader_EmptyDevicesArray(t *testing.T) {
	yaml := `---
devices: []
`
	devices, err := ParseDeviceConfigFromReader(strings.NewReader(yaml))
	if err != nil {
		t.Fatalf("empty devices array should not error, got: %v", err)
	}
	if len(devices.Device) != 0 {
		t.Errorf("empty array should produce 0 devices, got %d", len(devices.Device))
	}
}

// ─── Malformed input ───────────────────────────────────────────────────────

func TestParseDeviceConfigFromReader_MalformedYAML(t *testing.T) {
	yaml := `this is not: valid: yaml: at all`
	_, err := ParseDeviceConfigFromReader(strings.NewReader(yaml))
	if err == nil {
		t.Errorf("malformed YAML should produce an error, got nil")
	}
}

func TestParseDeviceConfigFromReader_TypeMismatch(t *testing.T) {
	// vendor-id should be a number, but here it's a string. yaml.v2 may
	// accept the hex string "0x66f" but reject a clearly-non-numeric one.
	yaml := `---
devices:
    - device:
      name: A
      vendor-id: "not a number at all"
      product-code: 0x60380008
`
	_, err := ParseDeviceConfigFromReader(strings.NewReader(yaml))
	if err == nil {
		t.Errorf("type mismatch on vendor-id should produce an error, got nil")
	}
}

// ─── Multiple devices ──────────────────────────────────────────────────────
//
// Per the A2 audit, the system currently only supports one device, but
// the YAML format does allow multiple. This test pins that the PARSER
// accepts multiple devices correctly — the single-device limitation is
// elsewhere (in init_master.go), not in the parser.

func TestParseDeviceConfigFromReader_MultipleDevices(t *testing.T) {
	yaml := `---
devices:
    - device:
      name: A
      vendor-id: 0x0000066f
      id: 0
    - device:
      name: B
      vendor-id: 0x0000066f
      id: 1
`
	devices, err := ParseDeviceConfigFromReader(strings.NewReader(yaml))
	if err != nil {
		t.Fatalf("multi-device YAML should parse, got: %v", err)
	}
	if len(devices.Device) != 2 {
		t.Fatalf("expected 2 devices, got %d", len(devices.Device))
	}
	if devices.Device[0].Name != "A" || devices.Device[1].Name != "B" {
		t.Errorf("device names: got [%q, %q], want [A, B]",
			devices.Device[0].Name, devices.Device[1].Name)
	}
	if devices.Device[0].ID != 0 || devices.Device[1].ID != 1 {
		t.Errorf("device IDs: got [%d, %d], want [0, 1]",
			devices.Device[0].ID, devices.Device[1].ID)
	}
}

// ─── Default values for missing fields ────────────────────────────────────

func TestParseDeviceConfigFromReader_MissingOptionalFields(t *testing.T) {
	yaml := `---
devices:
    - device:
      name: A
`
	devices, err := ParseDeviceConfigFromReader(strings.NewReader(yaml))
	if err != nil {
		t.Fatalf("minimal YAML should parse, got: %v", err)
	}
	if len(devices.Device) != 1 {
		t.Fatalf("expected 1 device, got %d", len(devices.Device))
	}
	d := devices.Device[0]

	// All fields not provided should be zero values
	if d.Name != "A" {
		t.Errorf("Name: got %q, want A", d.Name)
	}
	if d.VendorID != 0 {
		t.Errorf("VendorID should default to 0, got %d", d.VendorID)
	}
	if d.PotNotThreshold != 0 {
		t.Errorf("PotNotThreshold should default to 0, got %v", d.PotNotThreshold)
	}
	if d.StopWhenHWPOTNOT {
		t.Errorf("StopWhenHWPOTNOT should default to false, got true")
	}
}

// ─── Partial-corrupt and boundary inputs ──────────────────────────────────
//
// These tests cover inputs between "fully valid" and "completely broken":
// structurally valid YAML where individual fields have unexpected values.
// These are the cases most likely from a hand-edited config on the controller.

// TestParseDeviceConfig_ZeroDriveXRatio confirms a zero ratio parses without
// error — the executor, not the parser, should validate this.
func TestParseDeviceConfig_ZeroDriveXRatio(t *testing.T) {
	yaml := `---
devices:
    - device:
      name: A
      drive-x-ratio: 0
`
	devices, err := ParseDeviceConfigFromReader(strings.NewReader(yaml))
	if err != nil {
		t.Fatalf("zero drive-x-ratio should parse without error: %v", err)
	}
	if devices.Device[0].DriveXRatio != 0 {
		t.Errorf("DriveXRatio = %d, want 0", devices.Device[0].DriveXRatio)
	}
}

// TestParseDeviceConfig_UnknownFieldIgnored documents that unknown YAML keys
// are silently ignored (standard gopkg.in/yaml.v2 behaviour).
func TestParseDeviceConfig_UnknownFieldIgnored(t *testing.T) {
	yaml := `---
devices:
    - device:
      name: A
      drive-x-ratio: 20000
      futureField: "does not exist in the struct"
`
	_, err := ParseDeviceConfigFromReader(strings.NewReader(yaml))
	if err != nil {
		t.Errorf("unknown YAML field should be silently ignored, got error: %v", err)
	}
}

// TestParseDeviceConfig_NegativePotNotThreshold verifies that a negative
// threshold (misconfigured but structurally valid) parses without error.
func TestParseDeviceConfig_NegativePotNotThreshold(t *testing.T) {
	yaml := `---
devices:
    - device:
      name: A
      drive-x-ratio: 20000
      pot-not-threshold: -5.0
`
	devices, err := ParseDeviceConfigFromReader(strings.NewReader(yaml))
	if err != nil {
		t.Fatalf("negative pot-not-threshold should parse without error: %v", err)
	}
	if devices.Device[0].PotNotThreshold != -5.0 {
		t.Errorf("PotNotThreshold = %v, want -5.0", devices.Device[0].PotNotThreshold)
	}
}
