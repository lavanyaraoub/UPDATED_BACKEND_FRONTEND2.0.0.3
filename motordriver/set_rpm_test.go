//go:build unit

package motordriver

// Unit tests for setRpm's guard clauses and non-cgo branches.
//
// The Profile Velocity SDO branch (masterDevice.PdoVelSdoReady && PdoVelSdoReq
// != nil) calls C.trigger_profile_vel_request / C.get_profile_vel_state and
// needs a real ec_sdo_request_t — left PdoVelSdoReq nil in every test here so
// that branch is never entered; this is a valid production state too
// (PdoVelSdoReady only ever gets set true alongside a real request).

import (
	"testing"

	ethercatDevice "EtherCAT/ethercatdevicedatatypes"
)

// ─── PDO not active / RxPDO not ready guard ───────────────────────────────────

func TestSetRpm_PDONotActive_ReturnsError(t *testing.T) {
	setPDOInactive(t)
	d := stubDevice("A")
	d.PdoJogReady = true

	if err := setRpm(d, 100); err == nil {
		t.Fatal("PDO not active: expected error, got nil")
	}
}

func TestSetRpm_PdoJogNotReady_ReturnsError(t *testing.T) {
	activatePDO(t)
	d := stubDevice("A")
	d.PdoJogReady = false

	if err := setRpm(d, 100); err == nil {
		t.Fatal("PdoJogReady=false: expected error, got nil")
	}
}

// ─── Happy path — PdoVelSdoReady false (SDO velocity update skipped) ──────────

func TestSetRpm_PositiveRPM_CachesScaledVelocity(t *testing.T) {
	activatePDO(t)
	d := stubDevice("A")
	d.PdoJogReady = true
	d.Device = ethercatDevice.Device{RPMConst: 100}

	if err := setRpm(d, 5); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := d.desiredTargetVelocity.Load(); got != 500 {
		t.Errorf("desiredTargetVelocity = %d, want 500 (100*5)", got)
	}
}

func TestSetRpm_ZeroRPM_CachesZeroVelocity(t *testing.T) {
	activatePDO(t)
	d := stubDevice("A")
	d.PdoJogReady = true
	d.Device = ethercatDevice.Device{RPMConst: 100}

	if err := setRpm(d, 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := d.desiredTargetVelocity.Load(); got != 0 {
		t.Errorf("desiredTargetVelocity = %d, want 0", got)
	}
}

func TestSetRpm_NegativeRPM_CachesNegativeVelocity(t *testing.T) {
	activatePDO(t)
	d := stubDevice("A")
	d.PdoJogReady = true
	d.Device = ethercatDevice.Device{RPMConst: 100}

	if err := setRpm(d, -5); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := d.desiredTargetVelocity.Load(); got != -500 {
		t.Errorf("desiredTargetVelocity = %d, want -500", got)
	}
}

func TestSetRpm_RPMConstZero_ScaledVelocityIsZero(t *testing.T) {
	activatePDO(t)
	d := stubDevice("A")
	d.PdoJogReady = true
	d.Device = ethercatDevice.Device{RPMConst: 0} // no motion.rpm_const configured

	if err := setRpm(d, 500); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := d.desiredTargetVelocity.Load(); got != 0 {
		t.Errorf("desiredTargetVelocity = %d, want 0 when RPMConst=0", got)
	}
}

func TestSetRpm_PdoVelSdoReadyFalse_SkipsSDOBranchWithoutError(t *testing.T) {
	activatePDO(t)
	d := stubDevice("A")
	d.PdoJogReady = true
	d.PdoVelSdoReady = false // explicit, though this is also the zero value
	d.Device = ethercatDevice.Device{RPMConst: 10}

	if err := setRpm(d, 10); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSetRpm_PdoVelSdoReadyTrueButRequestNil_SkipsSDOBranchWithoutError(t *testing.T) {
	// PdoVelSdoReady=true alone should not be enough to enter the cgo
	// branch — PdoVelSdoReq must also be non-nil. Guards against a future
	// regression where the `&&` becomes an `||`.
	activatePDO(t)
	d := stubDevice("A")
	d.PdoJogReady = true
	d.PdoVelSdoReady = true
	d.PdoVelSdoReq = nil
	d.Device = ethercatDevice.Device{RPMConst: 10}

	if err := setRpm(d, 10); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// setPDOInactive sets pdoActive=false for the duration of a test and restores
// the previous value on cleanup. Complements the package's existing
// activatePDO(t) helper (which only ever sets true) for tests that need to
// explicitly exercise the "PDO not active" guard from a clean state.
func setPDOInactive(t *testing.T) {
	t.Helper()
	prev := pdoActive.Load()
	pdoActive.Store(false)
	t.Cleanup(func() { pdoActive.Store(prev) })
}
