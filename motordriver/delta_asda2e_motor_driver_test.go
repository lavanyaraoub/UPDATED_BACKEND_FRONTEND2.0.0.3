//go:build unit

package motordriver

// Unit tests for DeltaASDA2E — the pure logic paths only. hasTargetReached,
// pollIOStat, and ResetMultiTurn's SDO-writing branches need a real EtherCAT
// master and are intentionally out of scope here.

import (
	"testing"

	ethercatDevice "EtherCAT/ethercatdevicedatatypes"
)

// ─── IsTargetReached — standard CiA-402, bit 10 only ──────────────────────────

func TestDelta_IsTargetReached_Bit10Set_ReturnsTrue(t *testing.T) {
	d := DeltaASDA2E{}
	if !d.IsTargetReached(1 << 10) {
		t.Error("bit10 set: expected true")
	}
}

func TestDelta_IsTargetReached_Bit10Clear_ReturnsFalse(t *testing.T) {
	d := DeltaASDA2E{}
	if d.IsTargetReached(0x0000) {
		t.Error("bit10 clear: expected false")
	}
}

func TestDelta_IsTargetReached_Bit12IsIrrelevant(t *testing.T) {
	// Unlike A6, Delta only checks bit 10 — bit 12 state must not matter.
	d := DeltaASDA2E{}
	if !d.IsTargetReached(1<<10 | 1<<12) {
		t.Error("Delta should ignore bit12 (set-point ack); bit10 alone is sufficient")
	}
}

// ─── JogControlword — ramp bits 4-6 set, Halt bit 8 cleared ───────────────────

func TestDelta_JogControlword_SetsRampBitsAndClearsHalt(t *testing.T) {
	d := DeltaASDA2E{}
	got := d.JogControlword(0x000F)
	if got&0x0070 != 0x0070 {
		t.Errorf("JogControlword(0x000F) = 0x%04X, ramp bits 4-6 (0x0070) not all set", got)
	}
	if got&0x0100 != 0 {
		t.Errorf("JogControlword(0x000F) = 0x%04X, Halt bit 8 should be cleared", got)
	}
}

func TestDelta_JogControlword_HaltBitAlreadySet_GetsCleared(t *testing.T) {
	d := DeltaASDA2E{}
	got := d.JogControlword(0x010F) // Halt bit pre-set
	if got&0x0100 != 0 {
		t.Errorf("JogControlword(0x010F) = 0x%04X, Halt bit should be cleared", got)
	}
}

func TestDelta_JogControlword_PreservesOtherBits(t *testing.T) {
	d := DeltaASDA2E{}
	got := d.JogControlword(0x0001) // enable-voltage bit
	if got&0x0001 == 0 {
		t.Errorf("JogControlword(0x0001) = 0x%04X, bit0 should be preserved", got)
	}
}

// ─── FaultResetControlword — 0x008F, distinct from A6's 0x0080 ────────────────

func TestDelta_FaultResetControlword_Returns0x008F(t *testing.T) {
	d := DeltaASDA2E{}
	if got := d.FaultResetControlword(); got != 0x008F {
		t.Errorf("FaultResetControlword() = 0x%04X, want 0x008F", got)
	}
}

// ─── StandbyOpMode ──────────────────────────────────────────────────────────────

func TestDelta_StandbyOpMode_ReturnsMode3(t *testing.T) {
	d := DeltaASDA2E{}
	if got := d.StandbyOpMode(); got != 3 {
		t.Errorf("StandbyOpMode() = %d, want 3 (Profile Velocity)", got)
	}
}

// ─── SupportsMultiTurnReset ─────────────────────────────────────────────────────

func TestDelta_SupportsMultiTurnReset_ReturnsTrue(t *testing.T) {
	d := DeltaASDA2E{}
	if !d.SupportsMultiTurnReset() {
		t.Error("Delta should support multi-turn reset (P2-08/P2-71)")
	}
}

// ─── potNotEnabled — digital input mapping (bits 0-1 via PDO, active-HIGH) ────

func TestDelta_PotNotEnabled_BothClear_ReturnsFalse(t *testing.T) {
	d := DeltaASDA2E{}
	dev := stubDevice("A")
	dev.PDODI.Store(0)

	enabled, err := d.potNotEnabled(dev)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if enabled {
		t.Error("both limit bits clear: expected false")
	}
}

func TestDelta_PotNotEnabled_NegativeLimitBitSet_ReturnsTrue(t *testing.T) {
	d := DeltaASDA2E{}
	dev := stubDevice("A")
	dev.PDODI.Store(1 << 0) // bit 0: negative limit

	enabled, err := d.potNotEnabled(dev)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !enabled {
		t.Error("negative limit bit set: expected true")
	}
}

func TestDelta_PotNotEnabled_PositiveLimitBitSet_ReturnsTrue(t *testing.T) {
	d := DeltaASDA2E{}
	dev := stubDevice("A")
	dev.PDODI.Store(1 << 1) // bit 1: positive limit

	enabled, err := d.potNotEnabled(dev)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !enabled {
		t.Error("positive limit bit set: expected true")
	}
}

func TestDelta_PotNotEnabled_NeverReturnsError(t *testing.T) {
	// Unlike A6, Delta's potNotEnabled reads the PDO cache directly — there
	// is no SDO fallback path, so it should never return a non-nil error.
	d := DeltaASDA2E{}
	dev := stubDevice("A")
	dev.PDODI.Store(0xFFFFFFFF)

	_, err := d.potNotEnabled(dev)
	if err != nil {
		t.Errorf("potNotEnabled should never error, got %v", err)
	}
}

// ─── readDeclampSignal / readClampSignal — Delta has no clamp/declamp PDO ─────

func TestDelta_ReadDeclampSignal_AlwaysReturnsTrueImmediately(t *testing.T) {
	d := DeltaASDA2E{}
	dev := stubDevice("A")
	ok, err := d.readDeclampSignal(dev, 0)
	if err != nil || !ok {
		t.Errorf("readDeclampSignal() = (%v, %v), want (true, nil)", ok, err)
	}
}

func TestDelta_ReadClampSignal_AlwaysReturnsTrueImmediately(t *testing.T) {
	d := DeltaASDA2E{}
	dev := stubDevice("A")
	ok, err := d.readClampSignal(dev, 0)
	if err != nil || !ok {
		t.Errorf("readClampSignal() = (%v, %v), want (true, nil)", ok, err)
	}
}

// ─── receivedECS / receivedECSZero — Delta has no ECS input mapped ────────────

func TestDelta_ReceivedECS_AlwaysReturns1(t *testing.T) {
	d := DeltaASDA2E{}
	dev := stubDevice("A")
	got := d.receivedECS(dev, ethercatDevice.Operation{}, nil)
	if got != 1 {
		t.Errorf("receivedECS() = %d, want 1", got)
	}
}

func TestDelta_ReceivedECSZero_AlwaysReturns1(t *testing.T) {
	d := DeltaASDA2E{}
	dev := stubDevice("A")
	got := d.receivedECSZero(dev, ethercatDevice.Operation{}, nil)
	if got != 1 {
		t.Errorf("receivedECSZero() = %d, want 1", got)
	}
}

// ─── sendFinishSignal — guard clauses ────────────────────────────────────────

func TestDelta_SendFinishSignal_PdoDigOutNotReady_ReturnsNil(t *testing.T) {
	d := DeltaASDA2E{}
	dev := stubDevice("A")
	dev.PdoDigOutReady = false

	err := d.sendFinishSignal(dev, ethercatDevice.Operation{})
	if err != nil {
		t.Errorf("PdoDigOutReady=false: expected nil (non-critical), got %v", err)
	}
}

func TestDelta_SendFinishSignal_DeviceNotInGlobalSlice_ReturnsNil(t *testing.T) {
	restore := setMasterDevicesForTest() // empty
	defer restore()

	d := DeltaASDA2E{}
	dev := stubDevice("A")
	dev.PdoDigOutReady = true

	err := d.sendFinishSignal(dev, ethercatDevice.Operation{})
	if err != nil {
		t.Errorf("device not found in global slice: expected nil, got %v", err)
	}
}
