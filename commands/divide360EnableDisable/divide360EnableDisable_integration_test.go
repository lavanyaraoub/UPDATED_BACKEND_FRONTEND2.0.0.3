//go:build integration

package main

import (
	"testing"

	dt "EtherCAT/datatypes"
)

func baseCtx() *dt.ExecutionContext {
	return &dt.ExecutionContext{
		TrialModeActive:        true,
		ExecutionMode:          "continuous",
		CurrentExecCommandLine: 0,
		DriveSettings:          map[string]dt.DriveSetting{},
	}
}

func baseCmd(s string) dt.Command { return dt.Command{Cmd: s} }

// ─── G16 (enable divide360) ───────────────────────────────────────────────

func TestDivide360EnableDisable_G16_SetsDivide360OnTo1(t *testing.T) {
	ctx := baseCtx()
	ctx.Divide360On = 0
	CommandHandler{}.Handle(baseCmd("G16"), ctx)
	if ctx.Divide360On != 1 {
		t.Errorf("Divide360On = %d, want 1 after G16", ctx.Divide360On)
	}
}

func TestDivide360EnableDisable_G16_ReturnsOneResult(t *testing.T) {
	ctx := baseCtx()
	results := CommandHandler{}.Handle(baseCmd("G16"), ctx)
	if len(results) != 1 {
		t.Errorf("got %d results, want 1", len(results))
	}
}

func TestDivide360EnableDisable_G16_AdvancesLine(t *testing.T) {
	ctx := baseCtx()
	ctx.CurrentExecCommandLine = 2
	CommandHandler{}.Handle(baseCmd("G16"), ctx)
	if ctx.NextCmdLineToExec != 3 {
		t.Errorf("NextCmdLineToExec = %d, want 3", ctx.NextCmdLineToExec)
	}
}

// ─── G10 (disable divide360) ─────────────────────────────────────────────

func TestDivide360EnableDisable_G10_SetsDivide360OnTo2(t *testing.T) {
	ctx := baseCtx()
	ctx.Divide360On = 1
	CommandHandler{}.Handle(baseCmd("G10"), ctx)
	if ctx.Divide360On != 2 {
		t.Errorf("Divide360On = %d, want 2 after G10", ctx.Divide360On)
	}
}

func TestDivide360EnableDisable_G10_ReturnsOneResult(t *testing.T) {
	ctx := baseCtx()
	results := CommandHandler{}.Handle(baseCmd("G10"), ctx)
	if len(results) != 1 {
		t.Errorf("got %d results, want 1", len(results))
	}
}

func TestDivide360EnableDisable_G10_AdvancesLine(t *testing.T) {
	ctx := baseCtx()
	ctx.CurrentExecCommandLine = 7
	CommandHandler{}.Handle(baseCmd("G10"), ctx)
	if ctx.NextCmdLineToExec != 8 {
		t.Errorf("NextCmdLineToExec = %d, want 8", ctx.NextCmdLineToExec)
	}
}

// ─── state transitions ────────────────────────────────────────────────────

func TestDivide360EnableDisable_G16ThenG10_StateTransition(t *testing.T) {
	ctx := baseCtx()
	CommandHandler{}.Handle(baseCmd("G16"), ctx)
	if ctx.Divide360On != 1 {
		t.Fatalf("after G16: Divide360On = %d, want 1", ctx.Divide360On)
	}
	CommandHandler{}.Handle(baseCmd("G10"), ctx)
	if ctx.Divide360On != 2 {
		t.Errorf("after G10: Divide360On = %d, want 2", ctx.Divide360On)
	}
}

func TestDivide360EnableDisable_UnknownCmd_DoesNotChangeState(t *testing.T) {
	ctx := baseCtx()
	ctx.Divide360On = 0
	// Neither G16 nor G10 — state should not change
	CommandHandler{}.Handle(baseCmd("G99"), ctx)
	if ctx.Divide360On != 0 {
		t.Errorf("Divide360On = %d, want 0 for unknown command", ctx.Divide360On)
	}
}

func TestDivide360EnableDisable_UnknownCmd_StillReturnsOneResult(t *testing.T) {
	ctx := baseCtx()
	results := CommandHandler{}.Handle(baseCmd("G99"), ctx)
	if len(results) != 1 {
		t.Errorf("got %d results, want 1 even for unknown command", len(results))
	}
}

func TestDivide360EnableDisable_DoesNotSetErr(t *testing.T) {
	for _, cmd := range []string{"G16", "G10", "G99"} {
		ctx := baseCtx()
		CommandHandler{}.Handle(baseCmd(cmd), ctx)
		if ctx.Err != nil {
			t.Errorf("%s: Err = %v, want nil", cmd, ctx.Err)
		}
	}
}

// ─── CommandName ──────────────────────────────────────────────────────────

func TestDivide360EnableDisable_CommandName(t *testing.T) {
	got := CommandHandler{}.CommandName()
	if got != "divideBy360EnableDisable" {
		t.Errorf("CommandName() = %q, want divideBy360EnableDisable", got)
	}
}

// ─── CreateHandler ────────────────────────────────────────────────────────

func TestDivide360EnableDisable_CreateHandler_ReturnsHandlerWithCorrectName(t *testing.T) {
	handler := CreateHandler()
	if got := handler.CommandName(); got != "divideBy360EnableDisable" {
		t.Errorf("CreateHandler().CommandName() = %q, want %q", got, "divideBy360EnableDisable")
	}
}
