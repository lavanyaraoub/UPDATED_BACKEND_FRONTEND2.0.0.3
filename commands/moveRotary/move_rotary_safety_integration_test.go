//go:build integration

package main

import (
	"testing"

	dt "EtherCAT/datatypes"
)

func newMotionSafetyContext() *dt.ExecutionContext {
	ctx := &dt.ExecutionContext{
		RunMode:           "ABS",
		CurrentWorkOffSet: "G53",
		DriveSettings: map[string]dt.DriveSetting{
			"A": {POTLimit: 100, NOTLimit: -100, ConfiguredWorkOffset: map[string]float64{"G53": 0}, DestinationPosition: 0},
			"B": {POTLimit: 100, NOTLimit: -100, ConfiguredWorkOffset: map[string]float64{"G53": 0}, DestinationPosition: 0},
		},
	}
	return ctx
}

func TestMoveRotarySafetyIntegration_AbsoluteMoveUpdatesOnlySelectedDrive(t *testing.T) {
	ctx := newMotionSafetyContext()
	if err := checkPotNotLimit(ctx, 90, "A"); err != nil {
		t.Fatalf("checkPotNotLimit returned error: %v", err)
	}
	if got := ctx.DriveSettings["A"].DestinationPosition; got != 90 {
		t.Fatalf("A destination = %v, want 90", got)
	}
	if got := ctx.DriveSettings["B"].DestinationPosition; got != 0 {
		t.Fatalf("B destination = %v, want unchanged 0", got)
	}
}

func TestMoveRotarySafetyIntegration_RelativeMoveUsesExistingDestination(t *testing.T) {
	ctx := newMotionSafetyContext()
	ctx.RunMode = "REL"
	setting := ctx.DriveSettings["A"]
	setting.DestinationPosition = 40
	ctx.DriveSettings["A"] = setting

	if err := checkPotNotLimit(ctx, 15, "A"); err != nil {
		t.Fatalf("checkPotNotLimit returned error: %v", err)
	}
	if got := ctx.DriveSettings["A"].DestinationPosition; got != 55 {
		t.Fatalf("A destination = %v, want 55", got)
	}
}

func TestMoveRotarySafetyIntegration_BlocksPOTLimit(t *testing.T) {
	ctx := newMotionSafetyContext()
	err := checkPotNotLimit(ctx, 100, "A")
	if err == nil || err.Error() != "POT Limit exceeded" {
		t.Fatalf("error = %v, want POT Limit exceeded", err)
	}
}

// TestMoveRotarySafetyIntegration_NOTLimitCurrentBehavior previously documented
// a known defect where a REL move breaching the NOT limit was not blocked.
// The fix is now in place — this test verifies the correct behaviour.
func TestMoveRotarySafetyIntegration_NOTLimitCurrentBehavior(t *testing.T) {
	ctx := newMotionSafetyContext()
	ctx.RunMode = "REL"

	setting := ctx.DriveSettings["A"]
	setting.DestinationPosition = -90
	setting.NOTLimit = -100
	setting.POTLimit = 100
	ctx.DriveSettings["A"] = setting

	// -90 + (-20) = -110, below NOTLimit=-100 — must now be blocked.
	err := checkPotNotLimit(ctx, -20, "A")
	if err == nil {
		t.Fatalf("REL -20° from -90° reaches -110°, breaches NOTLimit=-100°: expected error, got nil")
	}
	if err.Error() != "NOT Limit exceeded" {
		t.Fatalf("error = %q, want \"NOT Limit exceeded\"", err.Error())
	}
}
func TestMoveRotarySafetyIntegration_DisabledLimitsDoNotBlock(t *testing.T) {
	ctx := newMotionSafetyContext()
	setting := ctx.DriveSettings["A"]
	setting.POTLimit = 0
	setting.NOTLimit = 0
	ctx.DriveSettings["A"] = setting

	if err := checkPotNotLimit(ctx, 100000, "A"); err != nil {
		t.Fatalf("disabled limits should not block movement, got error: %v", err)
	}
}

// ─── NOT-limit: correct expected behaviour (currently broken) ────────────────
//
// The tests below document what checkPotNotLimit SHOULD do on the NOT side.
// They are marked t.Skip() so they do not fail CI while the bug exists.
// Remove the t.Skip() call once the fix is implemented.
// The pinned-defect test above documents the current broken behaviour.

func TestNOTLimit_AbsoluteMove_AtLimit_ShouldBeBlocked(t *testing.T) {
	ctx := newMotionSafetyContext() // NOTLimit = -100
	err := checkPotNotLimit(ctx, -100, "A")
	if err == nil {
		t.Fatalf("ABS move to -100° (= NOTLimit) should be blocked, got nil error")
	}
}

func TestNOTLimit_AbsoluteMove_BeyondLimit_ShouldBeBlocked(t *testing.T) {
	ctx := newMotionSafetyContext() // NOTLimit = -100
	err := checkPotNotLimit(ctx, -150, "A")
	if err == nil {
		t.Fatalf("ABS move to -150° (beyond NOTLimit -100°) should be blocked, got nil error")
	}
}

func TestNOTLimit_RelativeMove_Cumulative_ShouldBeBlocked(t *testing.T) {
	ctx := newMotionSafetyContext()
	ctx.RunMode = "REL"
	s := ctx.DriveSettings["A"]
	s.DestinationPosition = -90
	ctx.DriveSettings["A"] = s
	// -90 + (-20) = -110, below NOTLimit=-100 → must be blocked
	err := checkPotNotLimit(ctx, -20, "A")
	if err == nil {
		t.Fatalf("REL -20° from -90° reaches -110°, breaches NOT limit -100°: expected error, got nil")
	}
}

// ─── NOT-limit: safe negative moves that must still be ALLOWED ───────────────

func TestNOTLimit_AbsoluteMove_WithinRange_Allowed(t *testing.T) {
	ctx := newMotionSafetyContext() // NOTLimit = -100
	if err := checkPotNotLimit(ctx, -50, "A"); err != nil {
		t.Fatalf("ABS move to -50° (within NOT limit -100°) should be allowed, got: %v", err)
	}
}

func TestNOTLimit_AbsoluteMove_JustInsideLimit_Allowed(t *testing.T) {
	ctx := newMotionSafetyContext() // NOTLimit = -100
	if err := checkPotNotLimit(ctx, -99, "A"); err != nil {
		t.Fatalf("ABS move to -99° (one inside NOT limit -100°) should be allowed, got: %v", err)
	}
}

// ─── POT-limit: edge cases missing from the original suite ───────────────────

func TestPOTLimit_ZeroTarget_Allowed(t *testing.T) {
	ctx := newMotionSafetyContext()
	if err := checkPotNotLimit(ctx, 0, "A"); err != nil {
		t.Fatalf("move to 0° should always be allowed, got: %v", err)
	}
}

// TestPOTLimit_RelativeAccumulation_EventuallyBreaches verifies that two
// successive REL moves each of +5° from 90° eventually breach the 100° POT
// limit when the second push lands on or past it.
func TestPOTLimit_RelativeAccumulation_EventuallyBreaches(t *testing.T) {
	ctx := newMotionSafetyContext()
	ctx.RunMode = "REL"
	s := ctx.DriveSettings["A"]
	s.DestinationPosition = 90
	ctx.DriveSettings["A"] = s

	// First +5°: destination reaches 95° — within limit
	if err := checkPotNotLimit(ctx, 5, "A"); err != nil {
		t.Fatalf("REL +5° from 90° = 95°, within POT 100° — should be allowed: %v", err)
	}
	// Second +5°: destination reaches 100° — must be blocked
	if err := checkPotNotLimit(ctx, 5, "A"); err == nil {
		t.Fatalf("REL +5° from 95° = 100° — should be blocked at POT limit 100°, got nil")
	}
}

// TestPOTLimit_MultiDrive_IndependentLimits verifies that checking drive A
// does not affect drive B's destination or limits.
func TestPOTLimit_MultiDrive_IndependentLimits(t *testing.T) {
	ctx := newMotionSafetyContext() // both A and B: POT=100, NOT=-100
	// Move A to 90 — within limit
	if err := checkPotNotLimit(ctx, 90, "A"); err != nil {
		t.Fatalf("A: 90° within POT 100° should be allowed: %v", err)
	}
	// B must be unaffected — still at destination 0
	if got := ctx.DriveSettings["B"].DestinationPosition; got != 0 {
		t.Errorf("B DestinationPosition = %v after moving A, want 0 (unaffected)", got)
	}
	// B can independently accept 90° too
	if err := checkPotNotLimit(ctx, 90, "B"); err != nil {
		t.Fatalf("B: 90° within POT 100° should be allowed: %v", err)
	}
}
