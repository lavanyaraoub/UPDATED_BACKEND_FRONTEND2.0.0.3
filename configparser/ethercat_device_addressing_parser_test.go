//go:build unit

package configparser

// Tests for ethercat_device_addressing_parser.go.
//
// This parser handles files like a6minas.yml — the per-device address
// map that defines how each operation (configure, faultReset, manualjog,
// etc) is translated into SDO writes.

import (
	"strings"
	"testing"
)

// Minimal but representative YAML — two operations, each with steps.
// Drawn from the real a6minas.yml format.
const validEthercatYAML = `---
ethercat:
    - operation:
      name: configure
      steps:
        - step:
          name: "operation mode"
          address: 0x6060
          subindex: 0x00
          value: "0x1"
          delay: 0
          isBinary: false
          dataType: "U8"
        - step:
          name: "target position"
          address: 0x607A
          subindex: 0x00
          value: "100000"
          delay: 0
          isBinary: false
          dataType: "U32"
    - operation:
      name: faultReset
      steps:
        - step:
          name: "fault reset"
          address: 0x6040
          subindex: 0x00
          value: "11001111"
          delay: 150000
          isBinary: true
          dataType: "U16"
          action: "write"
`

// ─── Happy path ────────────────────────────────────────────────────────────

func TestParseEthercatAddressConfigFromReader_ValidYAML(t *testing.T) {
	cfg, err := ParseEthercatAddressConfigFromReader(strings.NewReader(validEthercatYAML))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if len(cfg.Operation) != 2 {
		t.Fatalf("expected 2 operations, got %d", len(cfg.Operation))
	}

	configure := cfg.Operation[0]
	if configure.Name != "configure" {
		t.Errorf("op[0].Name: got %q, want configure", configure.Name)
	}
	if len(configure.Steps) != 2 {
		t.Errorf("op[0].Steps: got %d steps, want 2", len(configure.Steps))
	}

	step0 := configure.Steps[0]
	if step0.Name != "operation mode" {
		t.Errorf("op[0].Steps[0].Name: got %q, want 'operation mode'", step0.Name)
	}
	if step0.Address != 0x6060 {
		t.Errorf("op[0].Steps[0].Address: got 0x%X, want 0x6060", step0.Address)
	}
	if step0.SubIndex != 0x00 {
		t.Errorf("op[0].Steps[0].SubIndex: got 0x%X, want 0x00", step0.SubIndex)
	}
	if step0.Value != "0x1" {
		t.Errorf("op[0].Steps[0].Value: got %q, want '0x1'", step0.Value)
	}
	if step0.DataType != "U8" {
		t.Errorf("op[0].Steps[0].DataType: got %q, want U8", step0.DataType)
	}
	if step0.IsBinary {
		t.Errorf("op[0].Steps[0].IsBinary: got true, want false")
	}
}

// ─── Step.GetValue helper integration ──────────────────────────────────────
//
// Once parsed, each Step has a GetValue() method that converts the
// string Value field into an int64 (handling hex 0x..., decimal, and
// binary "0b..." prefixes via strconv.ParseInt with base=0; binary
// without prefix when IsBinary=true).
//
// Test that parsed values can be successfully converted via GetValue.

func TestParseEthercatAddressConfigFromReader_StepGetValueWorks(t *testing.T) {
	cfg, _ := ParseEthercatAddressConfigFromReader(strings.NewReader(validEthercatYAML))

	// Test hex value: "0x1" should produce 1
	op0 := cfg.Operation[0]
	val, err := op0.Steps[0].GetValue()
	if err != nil {
		t.Errorf("GetValue on '0x1' failed: %v", err)
	}
	if val != 1 {
		t.Errorf("'0x1'.GetValue() = %d, want 1", val)
	}

	// Test decimal value: "100000" should produce 100000
	val, err = op0.Steps[1].GetValue()
	if err != nil {
		t.Errorf("GetValue on '100000' failed: %v", err)
	}
	if val != 100000 {
		t.Errorf("'100000'.GetValue() = %d, want 100000", val)
	}

	// Test binary value: IsBinary=true, value "11001111" → 0xCF = 207
	op1 := cfg.Operation[1]
	val, err = op1.Steps[0].GetValue()
	if err != nil {
		t.Errorf("GetValue on binary '11001111' failed: %v", err)
	}
	if val != 207 {
		t.Errorf("'11001111'.GetValue() = %d, want 207", val)
	}
}

// ─── GetOperation lookup ───────────────────────────────────────────────────

func TestParseEthercatAddressConfigFromReader_GetOperationByName(t *testing.T) {
	cfg, _ := ParseEthercatAddressConfigFromReader(strings.NewReader(validEthercatYAML))

	op, err := cfg.GetOperation("faultReset")
	if err != nil {
		t.Fatalf("GetOperation(faultReset): %v", err)
	}
	if op.Name != "faultReset" {
		t.Errorf("Name: got %q, want faultReset", op.Name)
	}
	if len(op.Steps) != 1 {
		t.Errorf("Steps: got %d, want 1", len(op.Steps))
	}
}

func TestParseEthercatAddressConfigFromReader_GetOperationNotFound(t *testing.T) {
	cfg, _ := ParseEthercatAddressConfigFromReader(strings.NewReader(validEthercatYAML))

	_, err := cfg.GetOperation("nonexistent")
	if err == nil {
		t.Errorf("GetOperation(nonexistent) should return error, got nil")
	}
}

// ─── Edge cases ────────────────────────────────────────────────────────────

func TestParseEthercatAddressConfigFromReader_EmptyInput(t *testing.T) {
	cfg, err := ParseEthercatAddressConfigFromReader(strings.NewReader(""))
	if err != nil {
		t.Fatalf("empty input should not error, got: %v", err)
	}
	if len(cfg.Operation) != 0 {
		t.Errorf("empty input should produce 0 operations, got %d", len(cfg.Operation))
	}
}

func TestParseEthercatAddressConfigFromReader_EmptyOperationsArray(t *testing.T) {
	yaml := `---
ethercat: []
`
	cfg, err := ParseEthercatAddressConfigFromReader(strings.NewReader(yaml))
	if err != nil {
		t.Fatalf("empty operations array should not error, got: %v", err)
	}
	if len(cfg.Operation) != 0 {
		t.Errorf("empty array should produce 0 operations, got %d", len(cfg.Operation))
	}
}

func TestParseEthercatAddressConfigFromReader_OperationWithNoSteps(t *testing.T) {
	// Some operations in a6minas.yml have all their steps commented out
	// (e.g. break_on, break_off, emergency). They should parse cleanly
	// with empty Steps slices.
	yaml := `---
ethercat:
    - operation:
      name: break_on
      steps:
`
	cfg, err := ParseEthercatAddressConfigFromReader(strings.NewReader(yaml))
	if err != nil {
		t.Fatalf("operation with no steps should parse, got: %v", err)
	}
	if len(cfg.Operation) != 1 {
		t.Fatalf("expected 1 operation, got %d", len(cfg.Operation))
	}
	if cfg.Operation[0].Name != "break_on" {
		t.Errorf("op name: got %q, want break_on", cfg.Operation[0].Name)
	}
	if len(cfg.Operation[0].Steps) != 0 {
		t.Errorf("steps: got %d, want 0", len(cfg.Operation[0].Steps))
	}
}

func TestParseEthercatAddressConfigFromReader_MalformedYAML(t *testing.T) {
	yaml := `this is { definitely not valid`
	_, err := ParseEthercatAddressConfigFromReader(strings.NewReader(yaml))
	if err == nil {
		t.Errorf("malformed YAML should produce an error, got nil")
	}
}

// ─── Read action vs Write action ───────────────────────────────────────────
//
// Some steps have action: "read", others action: "write", others don't
// specify (default empty string). Pin this behavior.

func TestParseEthercatAddressConfigFromReader_ActionField(t *testing.T) {
	yaml := `---
ethercat:
    - operation:
      name: test
      steps:
        - step:
          name: "no action specified"
          address: 0x6040
          subindex: 0x00
          value: "0"
          dataType: "U16"
        - step:
          name: "read action"
          address: 0x6040
          subindex: 0x00
          value: "0xFFFF"
          dataType: "U16"
          action: "read"
        - step:
          name: "write action"
          address: 0x6040
          subindex: 0x00
          value: "0"
          dataType: "U16"
          action: "write"
`
	cfg, err := ParseEthercatAddressConfigFromReader(strings.NewReader(yaml))
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	steps := cfg.Operation[0].Steps
	if steps[0].Action != "" {
		t.Errorf("step[0].Action: got %q, want empty string", steps[0].Action)
	}
	if steps[1].Action != "read" {
		t.Errorf("step[1].Action: got %q, want read", steps[1].Action)
	}
	if steps[2].Action != "write" {
		t.Errorf("step[2].Action: got %q, want write", steps[2].Action)
	}
}
