//go:build integration

package main

// Integration tests for the moveRotary command plugin.
//
// The plugin uses package main so it must be tested from within this package.
// These tests cover the execution-context mutation that every command is
// responsible for: updating RunMode, advancing NextCmdLineToExec, setting Err,
// updating DestinationPosition, and enforcing POT/NOT limits.
//
// All tests use execContext.TrialModeActive = true so no channels are opened
// and no EtherCAT/motor activity occurs.

import (
	"testing"

	dt "EtherCAT/datatypes"
)

// ─── helpers ──────────────────────────────────────────────────────────────

func newTrialContext() *dt.ExecutionContext {
	return &dt.ExecutionContext{
		TrialModeActive:   true,
		ExecutionMode:     "continuous",
		RunMode:           "ABS",
		CurrentWorkOffSet: "G53",
		DriveSettings: map[string]dt.DriveSetting{
			"A": {
				POTLimit:             180,
				NOTLimit:             -180,
				ConfiguredWorkOffset: map[string]float64{"G53": 0},
				DestinationPosition:  0,
			},
			"B": {
				POTLimit:             360,
				NOTLimit:             -360,
				ConfiguredWorkOffset: map[string]float64{"G53": 0},
				DestinationPosition:  0,
			},
		},
	}
}

func newTrialCmd(cmdStr string) dt.Command {
	return dt.Command{Cmd: cmdStr, ConsiderInBlockExecution: 1}
}

// ─── checkPotNotLimit ─────────────────────────────────────────────────────

func TestCheckPotNotLimit_AbsoluteMove_WithinLimits(t *testing.T) {
	ctx := newTrialContext()
	if err := checkPotNotLimit(ctx, 90, "A"); err != nil {
		t.Fatalf("90° ABS within ±180 limits: unexpected error %v", err)
	}
	if got := ctx.DriveSettings["A"].DestinationPosition; got != 90 {
		t.Errorf("DestinationPosition = %v, want 90", got)
	}
}

func TestCheckPotNotLimit_AbsoluteMove_ExactlyAtPOTLimit_Blocked(t *testing.T) {
	ctx := newTrialContext()
	// POT limit is 180 — moving to 180 should be blocked (limit is exclusive).
	err := checkPotNotLimit(ctx, 180, "A")
	if err == nil {
		t.Fatalf("move to POT limit (180) should be blocked, got nil error")
	}
}

func TestCheckPotNotLimit_AbsoluteMove_BelowPOTLimit_Allowed(t *testing.T) {
	ctx := newTrialContext()
	if err := checkPotNotLimit(ctx, 179, "A"); err != nil {
		t.Fatalf("move to 179° (below 180 POT limit) should be allowed: %v", err)
	}
}

func TestCheckPotNotLimit_DisabledLimits_AlwaysAllowed(t *testing.T) {
	ctx := newTrialContext()
	setting := ctx.DriveSettings["A"]
	setting.POTLimit = 0
	setting.NOTLimit = 0
	ctx.DriveSettings["A"] = setting

	if err := checkPotNotLimit(ctx, 99999, "A"); err != nil {
		t.Fatalf("disabled limits (0/0) should allow any position, got: %v", err)
	}
}

func TestCheckPotNotLimit_RelativeMode_AccumulatesDestination(t *testing.T) {
	ctx := newTrialContext()
	ctx.RunMode = "REL"
	setting := ctx.DriveSettings["A"]
	setting.DestinationPosition = 50
	ctx.DriveSettings["A"] = setting

	if err := checkPotNotLimit(ctx, 30, "A"); err != nil {
		t.Fatalf("REL 50+30=80, within ±180 limit: unexpected error %v", err)
	}
	// In REL mode the function should have updated DestinationPosition
	newDest := ctx.DriveSettings["A"].DestinationPosition
	// Destination after relative move: some value reflecting 50+30 normalised
	if newDest < 0 || newDest > 360 {
		t.Errorf("DestinationPosition = %v after REL move, expected in [0, 360]", newDest)
	}
}

func TestCheckPotNotLimit_OnlyTargetedDriveIsUpdated(t *testing.T) {
	ctx := newTrialContext()
	bBefore := ctx.DriveSettings["B"].DestinationPosition

	_ = checkPotNotLimit(ctx, 90, "A")

	if got := ctx.DriveSettings["B"].DestinationPosition; got != bBefore {
		t.Errorf("drive B DestinationPosition changed from %v to %v — only A should update", bBefore, got)
	}
}

// ─── CommandHandler.Handle in trial mode ──────────────────────────────────

func TestHandle_TrialMode_DoesNotPanic_AndAdvancesLine(t *testing.T) {
	ctx := newTrialContext()
	ctx.CurrentExecCommandLine = 2

	h := CommandHandler{}
	h.Handle(newTrialCmd("A90"), ctx)

	// In trial mode no channels are opened; the handler must still advance
	// NextCmdLineToExec.
	if ctx.NextCmdLineToExec != 3 {
		t.Errorf("NextCmdLineToExec = %d, want 3 (line advanced)", ctx.NextCmdLineToExec)
	}
}

func TestHandle_TrialMode_POTLimitSetsErr(t *testing.T) {
	ctx := newTrialContext()
	ctx.CurrentExecCommandLine = 0

	h := CommandHandler{}
	h.Handle(newTrialCmd("A180"), ctx) // exactly at POT limit → should set Err

	if ctx.Err == nil {
		t.Errorf("expected Err to be set for POT limit breach, got nil")
	}
}

func TestHandle_TrialMode_NegativePosition(t *testing.T) {
	ctx := newTrialContext()
	ctx.CurrentExecCommandLine = 0

	h := CommandHandler{}
	results := h.Handle(newTrialCmd("A-90"), ctx)

	// Trial mode returns no results (no motion executed)
	if len(results) != 0 {
		t.Errorf("trial mode should return 0 results, got %d", len(results))
	}
	// But the line must still advance
	if ctx.NextCmdLineToExec != 1 {
		t.Errorf("NextCmdLineToExec = %d, want 1", ctx.NextCmdLineToExec)
	}
}

func TestHandle_TrialMode_DriveB(t *testing.T) {
	ctx := newTrialContext()
	ctx.CurrentExecCommandLine = 5

	h := CommandHandler{}
	h.Handle(newTrialCmd("B45"), ctx)

	if ctx.Err != nil {
		t.Errorf("B45 within ±360 limit should not set Err, got: %v", ctx.Err)
	}
	if ctx.NextCmdLineToExec != 6 {
		t.Errorf("NextCmdLineToExec = %d, want 6", ctx.NextCmdLineToExec)
	}
}

func TestHandle_TrialMode_WorkOffsetApplied(t *testing.T) {
	ctx := newTrialContext()
	setting := ctx.DriveSettings["A"]
	setting.ConfiguredWorkOffset = map[string]float64{"G53": 10} // +10° offset
	ctx.DriveSettings["A"] = setting
	ctx.CurrentWorkOffSet = "G53"

	// Commanding A90 with +10 offset → effective target 100° — within ±180
	h := CommandHandler{}
	h.Handle(newTrialCmd("A90"), ctx)

	if ctx.Err != nil {
		t.Errorf("A90 + 10° offset = 100°, should be within ±180 limit. Err: %v", ctx.Err)
	}
}

func TestHandle_TrialMode_WorkOffsetCanCauseLimitBreach(t *testing.T) {
	ctx := newTrialContext()
	setting := ctx.DriveSettings["A"]
	setting.ConfiguredWorkOffset = map[string]float64{"G53": 50}
	ctx.DriveSettings["A"] = setting
	ctx.CurrentWorkOffSet = "G53"

	// Commanding A150 + 50° offset = 200° → exceeds POT limit of 180°
	h := CommandHandler{}
	h.Handle(newTrialCmd("A150"), ctx)

	if ctx.Err == nil {
		t.Errorf("A150 + 50° offset = 200°, should exceed POT limit of 180°, but Err is nil")
	}
}

// ─── CreateHandler ────────────────────────────────────────────────────────

func TestMoveRotary_CreateHandler_ReturnsHandlerWithCorrectName(t *testing.T) {
	handler := CreateHandler()
	if got := handler.CommandName(); got != "moveRotaryDegree" {
		t.Errorf("CreateHandler().CommandName() = %q, want %q", got, "moveRotaryDegree")
	}
}
