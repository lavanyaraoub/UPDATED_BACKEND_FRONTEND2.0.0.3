//go:build unit

package motordriver

// Unit tests for A6Minas — the pure logic paths only. Anything that calls
// SDODownload/SDOUpload2 (hasTargetReached, receivedECS/receivedECSZero,
// pollIOStat) needs a real EtherCAT master and is intentionally out of scope
// here; see MOTORDRIVER_FOLLOWUP.md for the hardware-gated list.

import (
	"testing"

	ethercatDevice "EtherCAT/ethercatdevicedatatypes"
)

// ─── IsTargetReached — bit 10 set AND bit 12 clear (A6-specific handshake) ────

func TestA6_IsTargetReached_Bit10SetBit12Clear_ReturnsTrue(t *testing.T) {
	a6 := A6Minas{}
	sw := uint16(1 << 10)
	if !a6.IsTargetReached(sw) {
		t.Error("bit10 set, bit12 clear: expected true")
	}
}

func TestA6_IsTargetReached_Bit10SetBit12Set_ReturnsFalse(t *testing.T) {
	a6 := A6Minas{}
	sw := uint16(1<<10 | 1<<12)
	if a6.IsTargetReached(sw) {
		t.Error("bit10 and bit12 both set (setpoint ack not yet cleared): expected false")
	}
}

func TestA6_IsTargetReached_Bit10Clear_ReturnsFalse(t *testing.T) {
	a6 := A6Minas{}
	if a6.IsTargetReached(0x0000) {
		t.Error("bit10 clear: expected false")
	}
}

// ─── JogControlword — A6 passes cwBase through unchanged ─────────────────────

func TestA6_JogControlword_ReturnsUnchanged(t *testing.T) {
	a6 := A6Minas{}
	for _, cw := range []uint16{0x000F, 0x0007, 0x0000, 0xFFFF} {
		if got := a6.JogControlword(cw); got != cw {
			t.Errorf("JogControlword(0x%04X) = 0x%04X, want unchanged", cw, got)
		}
	}
}

// ─── FaultResetControlword ─────────────────────────────────────────────────────

func TestA6_FaultResetControlword_Returns0x0080(t *testing.T) {
	a6 := A6Minas{}
	if got := a6.FaultResetControlword(); got != 0x0080 {
		t.Errorf("FaultResetControlword() = 0x%04X, want 0x0080", got)
	}
}

// ─── StandbyOpMode ──────────────────────────────────────────────────────────────

func TestA6_StandbyOpMode_ReturnsMode3(t *testing.T) {
	a6 := A6Minas{}
	if got := a6.StandbyOpMode(); got != 3 {
		t.Errorf("StandbyOpMode() = %d, want 3 (Profile Velocity)", got)
	}
}

// ─── SupportsMultiTurnReset ─────────────────────────────────────────────────────

func TestA6_SupportsMultiTurnReset_ReturnsTrue(t *testing.T) {
	a6 := A6Minas{}
	if !a6.SupportsMultiTurnReset() {
		t.Error("A6 should support multi-turn reset")
	}
}

// ─── potNotEnabled — digital input mapping (bits 0-1 via PDO) ─────────────────

func TestA6_PotNotEnabled_PdoReady_BothClear_ReturnsFalse(t *testing.T) {
	a6 := A6Minas{}
	d := stubDevice("A")
	d.PdoDIReady = true
	d.PDODI.Store(0)

	enabled, err := a6.potNotEnabled(d)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if enabled {
		t.Error("both POT/NOT bits clear: expected false")
	}
}

func TestA6_PotNotEnabled_PdoReady_POTBitSet_ReturnsTrue(t *testing.T) {
	a6 := A6Minas{}
	d := stubDevice("A")
	d.PdoDIReady = true
	d.PDODI.Store(0x01) // bit 0

	enabled, err := a6.potNotEnabled(d)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !enabled {
		t.Error("POT bit set: expected true")
	}
}

func TestA6_PotNotEnabled_PdoReady_NOTBitSet_ReturnsTrue(t *testing.T) {
	a6 := A6Minas{}
	d := stubDevice("A")
	d.PdoDIReady = true
	d.PDODI.Store(0x02) // bit 1

	enabled, err := a6.potNotEnabled(d)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !enabled {
		t.Error("NOT bit set: expected true")
	}
}

func TestA6_PotNotEnabled_PdoReady_UnrelatedBitsIgnored(t *testing.T) {
	a6 := A6Minas{}
	d := stubDevice("A")
	d.PdoDIReady = true
	d.PDODI.Store(0xFFFFFFFC) // every bit except 0 and 1

	enabled, err := a6.potNotEnabled(d)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if enabled {
		t.Error("only bits 0-1 matter for potNotEnabled; other bits set should not trigger it")
	}
}

func TestA6_PotNotEnabled_NotPdoReady_NoConfig_ReturnsError(t *testing.T) {
	a6 := A6Minas{}
	d := stubDevice("A") // PdoDIReady defaults false, no AddressConfigName loaded
	_, err := a6.potNotEnabled(d)
	if err == nil {
		t.Fatal("no PDO + no SDO config available: expected error, got nil")
	}
}

// ─── readDeclampSignal / readClampSignal — success path via PDO ──────────────

func TestA6_ReadDeclampSignal_BitAlreadySet_ReturnsTrueImmediately(t *testing.T) {
	a6 := A6Minas{}
	d := stubDevice("A")
	d.PdoDIReady = true
	d.PDODI.Store(1 << 7) // DCL bit already high

	ok, err := a6.readDeclampSignal(d, 50)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("DCL bit set: expected true")
	}
}

func TestA6_ReadClampSignal_BitAlreadySet_ReturnsTrueImmediately(t *testing.T) {
	a6 := A6Minas{}
	d := stubDevice("A")
	d.PdoDIReady = true
	d.PDODI.Store(1 << 6) // CL bit already high

	ok, err := a6.readClampSignal(d, 50)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("CL bit set: expected true")
	}
}

func TestA6_ReadDeclampSignal_TimesOut_ReturnsFalseAndError(t *testing.T) {
	withUIChannel(t)
	a6 := A6Minas{}
	d := stubDevice("A")
	d.PdoDIReady = true
	d.PDODI.Store(0) // DCL bit never set

	ok, err := a6.readDeclampSignal(d, 1) // 1ms timeout — fast test
	if err == nil {
		t.Error("declamp never asserted: expected error, got nil")
	}
	if ok {
		t.Error("declamp never asserted: expected false")
	}
}

func TestA6_ReadClampSignal_TimesOut_ReturnsFalseAndError(t *testing.T) {
	withUIChannel(t)
	a6 := A6Minas{}
	d := stubDevice("A")
	d.PdoDIReady = true
	d.PDODI.Store(0) // CL bit never set

	ok, err := a6.readClampSignal(d, 1) // 1ms timeout — fast test
	if err == nil {
		t.Error("clamp never asserted: expected error, got nil")
	}
	if ok {
		t.Error("clamp never asserted: expected false")
	}
}

// ─── sendFinishSignal — guard clauses ────────────────────────────────────────

func TestA6_SendFinishSignal_NoSteps_ReturnsNilImmediately(t *testing.T) {
	a6 := A6Minas{}
	d := stubDevice("A")
	err := a6.sendFinishSignal(d, ethercatDevice.Operation{Steps: nil})
	if err != nil {
		t.Errorf("no steps configured: expected nil, got %v", err)
	}
}

func TestA6_SendFinishSignal_DeviceNotInGlobalSlice_ReturnsNil(t *testing.T) {
	restore := setMasterDevicesForTest() // empty global slice
	defer restore()

	a6 := A6Minas{}
	d := stubDevice("A") // not registered in masterDevices
	op := ethercatDevice.Operation{Steps: []ethercatDevice.Step{{Value: "65536"}}}

	err := a6.sendFinishSignal(d, op)
	if err != nil {
		t.Errorf("device not found in global slice: expected nil, got %v", err)
	}
}

func TestA6_SendFinishSignal_UnparsableValue_ReturnsError(t *testing.T) {
	d := stubDevice("A")
	restore := setMasterDevicesForTest(d) // device IS in the global slice this time
	defer restore()

	a6 := A6Minas{}
	op := ethercatDevice.Operation{Steps: []ethercatDevice.Step{{Value: "not-a-number"}}}

	err := a6.sendFinishSignal(d, op)
	if err == nil {
		t.Error("unparsable finish-signal value: expected error, got nil")
	}
}
