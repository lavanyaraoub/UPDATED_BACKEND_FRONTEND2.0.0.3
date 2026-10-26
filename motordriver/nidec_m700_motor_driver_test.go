//go:build unit

package motordriver

// Unit tests for NidecM700 — the pure logic paths only. hasTargetReached's
// SDO-polling loop (once past the 2-step guard) and pollIOStat need a real
// EtherCAT master and are intentionally out of scope here.

import (
	"testing"

	ethercatDevice "EtherCAT/ethercatdevicedatatypes"
)

// ─── hasTargetReached — guard clause only (needs 2 steps in config) ──────────

func TestNidec_HasTargetReached_FewerThanTwoSteps_ReturnsError(t *testing.T) {
	n := NidecM700{}
	dev := stubDevice("A")

	err := n.hasTargetReached(dev, 0, 0, ethercatDevice.Operation{Steps: nil})
	if err == nil {
		t.Fatal("zero steps: expected error, got nil")
	}

	err = n.hasTargetReached(dev, 0, 0, ethercatDevice.Operation{
		Steps: []ethercatDevice.Step{{Name: "only-one"}},
	})
	if err == nil {
		t.Fatal("one step: expected error, got nil")
	}
}

// ─── IsTargetReached — standard CiA-402, bit 10 only ──────────────────────────

func TestNidec_IsTargetReached_Bit10Set_ReturnsTrue(t *testing.T) {
	n := NidecM700{}
	if !n.IsTargetReached(1 << 10) {
		t.Error("bit10 set: expected true")
	}
}

func TestNidec_IsTargetReached_Bit10Clear_ReturnsFalse(t *testing.T) {
	n := NidecM700{}
	if n.IsTargetReached(0x0000) {
		t.Error("bit10 clear: expected false")
	}
}

// ─── JogControlword — clears Halt bit 8 only ──────────────────────────────────

func TestNidec_JogControlword_ClearsHaltBit(t *testing.T) {
	n := NidecM700{}
	got := n.JogControlword(0x010F)
	if got&0x0100 != 0 {
		t.Errorf("JogControlword(0x010F) = 0x%04X, Halt bit 8 should be cleared", got)
	}
}

func TestNidec_JogControlword_PreservesOtherBits(t *testing.T) {
	n := NidecM700{}
	got := n.JogControlword(0x000F)
	if got != 0x000F {
		t.Errorf("JogControlword(0x000F) = 0x%04X, want 0x000F unchanged (Halt already clear)", got)
	}
}

func TestNidec_JogControlword_DoesNotSetRampBits(t *testing.T) {
	// Unlike Delta, Nidec must NOT OR in the 0x0070 ramp bits.
	n := NidecM700{}
	got := n.JogControlword(0x0000)
	if got&0x0070 != 0 {
		t.Errorf("JogControlword(0x0000) = 0x%04X, ramp bits should not be set for Nidec", got)
	}
}

// ─── FaultResetControlword — 0x008F ────────────────────────────────────────────

func TestNidec_FaultResetControlword_Returns0x008F(t *testing.T) {
	n := NidecM700{}
	if got := n.FaultResetControlword(); got != 0x008F {
		t.Errorf("FaultResetControlword() = 0x%04X, want 0x008F", got)
	}
}

// ─── StandbyOpMode — Mode 8 (CSP), distinct from A6/Delta's Mode 3 ────────────

func TestNidec_StandbyOpMode_ReturnsMode8(t *testing.T) {
	n := NidecM700{}
	if got := n.StandbyOpMode(); got != 8 {
		t.Errorf("StandbyOpMode() = %d, want 8 (Cyclic Synchronous Position)", got)
	}
}

// ─── potNotEnabled — digital input mapping (bits 0-1 via PDO) ─────────────────

func TestNidec_PotNotEnabled_BothClear_ReturnsFalse(t *testing.T) {
	n := NidecM700{}
	dev := stubDevice("A")
	dev.PDODI.Store(0)

	enabled, err := n.potNotEnabled(dev)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if enabled {
		t.Error("both limit bits clear: expected false")
	}
}

func TestNidec_PotNotEnabled_NegativeLimitBitSet_ReturnsTrue(t *testing.T) {
	n := NidecM700{}
	dev := stubDevice("A")
	dev.PDODI.Store(1 << 0)

	enabled, err := n.potNotEnabled(dev)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !enabled {
		t.Error("negative limit bit set: expected true")
	}
}

func TestNidec_PotNotEnabled_PositiveLimitBitSet_ReturnsTrue(t *testing.T) {
	n := NidecM700{}
	dev := stubDevice("A")
	dev.PDODI.Store(1 << 1)

	enabled, err := n.potNotEnabled(dev)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !enabled {
		t.Error("positive limit bit set: expected true")
	}
}

// ─── readDeclampSignal / readClampSignal — Nidec has no clamp/declamp PDO ─────

func TestNidec_ReadDeclampSignal_AlwaysReturnsTrueImmediately(t *testing.T) {
	n := NidecM700{}
	dev := stubDevice("A")
	ok, err := n.readDeclampSignal(dev, 0)
	if err != nil || !ok {
		t.Errorf("readDeclampSignal() = (%v, %v), want (true, nil)", ok, err)
	}
}

func TestNidec_ReadClampSignal_AlwaysReturnsTrueImmediately(t *testing.T) {
	n := NidecM700{}
	dev := stubDevice("A")
	ok, err := n.readClampSignal(dev, 0)
	if err != nil || !ok {
		t.Errorf("readClampSignal() = (%v, %v), want (true, nil)", ok, err)
	}
}

// ─── receivedECS / receivedECSZero — Nidec has no ECS input mapped ────────────

func TestNidec_ReceivedECS_AlwaysReturns1(t *testing.T) {
	n := NidecM700{}
	dev := stubDevice("A")
	got := n.receivedECS(dev, ethercatDevice.Operation{}, nil)
	if got != 1 {
		t.Errorf("receivedECS() = %d, want 1", got)
	}
}

func TestNidec_ReceivedECSZero_AlwaysReturns1(t *testing.T) {
	n := NidecM700{}
	dev := stubDevice("A")
	got := n.receivedECSZero(dev, ethercatDevice.Operation{}, nil)
	if got != 1 {
		t.Errorf("receivedECSZero() = %d, want 1", got)
	}
}

// ─── sendFinishSignal — guard clauses ────────────────────────────────────────

func TestNidec_SendFinishSignal_PdoDigOutNotReady_ReturnsNil(t *testing.T) {
	n := NidecM700{}
	dev := stubDevice("A")
	dev.PdoDigOutReady = false

	err := n.sendFinishSignal(dev, ethercatDevice.Operation{})
	if err != nil {
		t.Errorf("PdoDigOutReady=false: expected nil, got %v", err)
	}
}

func TestNidec_SendFinishSignal_DeviceNotInGlobalSlice_ReturnsNil(t *testing.T) {
	restore := setMasterDevicesForTest() // empty
	defer restore()

	n := NidecM700{}
	dev := stubDevice("A")
	dev.PdoDigOutReady = true

	err := n.sendFinishSignal(dev, ethercatDevice.Operation{})
	if err != nil {
		t.Errorf("device not found in global slice: expected nil, got %v", err)
	}
}

// ─── ResetMultiTurn — no-config and empty-steps guard paths ───────────────────

func TestNidec_ResetMultiTurn_NilDeviceInSlice_SkippedWithoutPanic(t *testing.T) {
	n := NidecM700{}
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("ResetMultiTurn panicked on nil device: %v", r)
		}
	}()
	// dev.Device.AddressConfigName is empty, so GetEtherCATOperation cannot
	// find a "resetMultiTurn" config and ResetMultiTurn surfaces that as an
	// error. The important behavior under test is that the nil entry ahead
	// of it in the slice is skipped rather than causing a nil-pointer panic.
	dev := stubDevice("A")
	err := n.ResetMultiTurn([]*MasterDevice{nil, dev})
	if err == nil {
		t.Fatal("unconfigured device: expected error (no resetMultiTurn config), got nil")
	}
}
