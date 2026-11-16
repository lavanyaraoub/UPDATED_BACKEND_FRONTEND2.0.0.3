package ethercatdevicedatatypes

// Unit tests for the pure logic in ethercat_device_addressing.go —
// Step.GetValue/GetValue16 (binary vs decimal/hex parsing) and
// Ethercat.GetOperation (name-based lookup). No build tag needed: this
// package has zero external dependencies (no cgo, no hardware, no
// filesystem), so these tests always run with a plain `go test`.

import (
	"testing"
)

// ─── Step.GetValue ─────────────────────────────────────────────────────────

func TestStepGetValue_DecimalString(t *testing.T) {
	s := Step{Value: "1234", IsBinary: false}
	got, err := s.GetValue()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 1234 {
		t.Errorf("GetValue() = %d, want 1234", got)
	}
}

func TestStepGetValue_HexString(t *testing.T) {
	s := Step{Value: "0x1F", IsBinary: false}
	got, err := s.GetValue()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 31 {
		t.Errorf("GetValue() = %d, want 31", got)
	}
}

func TestStepGetValue_OctalString(t *testing.T) {
	// base 0 in ParseInt treats a leading "0" as octal.
	s := Step{Value: "010", IsBinary: false}
	got, err := s.GetValue()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 8 {
		t.Errorf("GetValue() = %d, want 8 (octal 010)", got)
	}
}

func TestStepGetValue_BinaryString(t *testing.T) {
	s := Step{Value: "1010", IsBinary: true}
	got, err := s.GetValue()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 10 {
		t.Errorf("GetValue() = %d, want 10 (binary 1010)", got)
	}
}

func TestStepGetValue_NegativeDecimal(t *testing.T) {
	s := Step{Value: "-42", IsBinary: false}
	got, err := s.GetValue()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != -42 {
		t.Errorf("GetValue() = %d, want -42", got)
	}
}

func TestStepGetValue_InvalidStringReturnsError(t *testing.T) {
	s := Step{Value: "not-a-number", IsBinary: false}
	_, err := s.GetValue()
	if err == nil {
		t.Error("GetValue() with invalid string: expected error, got nil")
	}
}

func TestStepGetValue_InvalidBinaryStringReturnsError(t *testing.T) {
	s := Step{Value: "1012", IsBinary: true} // '2' is not a valid binary digit
	_, err := s.GetValue()
	if err == nil {
		t.Error("GetValue() with invalid binary string: expected error, got nil")
	}
}

func TestStepGetValue_EmptyStringReturnsError(t *testing.T) {
	s := Step{Value: "", IsBinary: false}
	_, err := s.GetValue()
	if err == nil {
		t.Error("GetValue() with empty string: expected error, got nil")
	}
}

// ─── Step.GetValue16 ────────────────────────────────────────────────────────

func TestStepGetValue16_DecimalString(t *testing.T) {
	s := Step{Value: "5000", IsBinary: false}
	got, err := s.GetValue16()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 5000 {
		t.Errorf("GetValue16() = %d, want 5000", got)
	}
}

func TestStepGetValue16_HexString(t *testing.T) {
	s := Step{Value: "0x00FF", IsBinary: false}
	got, err := s.GetValue16()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 0x00FF {
		t.Errorf("GetValue16() = 0x%04X, want 0x00FF", got)
	}
}

func TestStepGetValue16_BinaryString(t *testing.T) {
	s := Step{Value: "11111111", IsBinary: true}
	got, err := s.GetValue16()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 255 {
		t.Errorf("GetValue16() = %d, want 255", got)
	}
}

func TestStepGetValue16_InvalidStringReturnsError(t *testing.T) {
	s := Step{Value: "garbage", IsBinary: false}
	_, err := s.GetValue16()
	if err == nil {
		t.Error("GetValue16() with invalid string: expected error, got nil")
	}
}

func TestStepGetValue16_ZeroValue(t *testing.T) {
	s := Step{Value: "0", IsBinary: false}
	got, err := s.GetValue16()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 0 {
		t.Errorf("GetValue16() = %d, want 0", got)
	}
}

// ─── Ethercat.GetOperation ──────────────────────────────────────────────────

func TestEthercatGetOperation_FoundByName(t *testing.T) {
	e := Ethercat{
		Operation: []Operation{
			{Name: "powerOn", Steps: []Step{{Name: "step1", Address: 0x6040}}},
			{Name: "powerOff", Steps: []Step{{Name: "step1", Address: 0x6040}}},
		},
	}

	op, err := e.GetOperation("powerOff")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if op.Name != "powerOff" {
		t.Errorf("GetOperation('powerOff').Name = %q, want powerOff", op.Name)
	}
	if len(op.Steps) != 1 {
		t.Errorf("GetOperation('powerOff') returned %d steps, want 1", len(op.Steps))
	}
}

func TestEthercatGetOperation_NotFoundReturnsError(t *testing.T) {
	e := Ethercat{
		Operation: []Operation{
			{Name: "powerOn"},
		},
	}

	_, err := e.GetOperation("nonexistent_operation")
	if err == nil {
		t.Error("GetOperation with unknown name: expected error, got nil")
	}
}

func TestEthercatGetOperation_EmptyOperationsList_ReturnsError(t *testing.T) {
	e := Ethercat{}
	_, err := e.GetOperation("anything")
	if err == nil {
		t.Error("GetOperation on empty Ethercat: expected error, got nil")
	}
}

func TestEthercatGetOperation_CaseSensitiveMatch(t *testing.T) {
	e := Ethercat{
		Operation: []Operation{
			{Name: "powerOn"},
		},
	}
	// Name matching is exact-case (no case-insensitive fallback in the source).
	_, err := e.GetOperation("POWERON")
	if err == nil {
		t.Error("GetOperation should be case-sensitive — 'POWERON' should not match 'powerOn'")
	}
}

func TestEthercatGetOperation_DuplicateNames_ReturnsFirst(t *testing.T) {
	e := Ethercat{
		Operation: []Operation{
			{Name: "dup", Steps: []Step{{Address: 1}}},
			{Name: "dup", Steps: []Step{{Address: 2}}},
		},
	}
	op, err := e.GetOperation("dup")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if op.Steps[0].Address != 1 {
		t.Errorf("GetOperation with duplicate names: got Address=%d, want 1 (first match)", op.Steps[0].Address)
	}
}
