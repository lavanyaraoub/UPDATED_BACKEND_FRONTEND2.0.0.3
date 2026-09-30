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
		CommandMaps: dt.Execution{Command: []dt.Command{
			{Cmd: "A**", Func: "moveRotary", ConsiderInBlockExecution: 1},
			{Cmd: "INVALID", Func: "invalidCommand"},
		}},
	}
}

func baseCmd(s string) dt.Command { return dt.Command{Cmd: s} }

func TestWorkoffset_SetsCurrentWorkOffset(t *testing.T) {
	ctx := baseCtx()
	CommandHandler{}.Handle(baseCmd("G53"), ctx)
	if ctx.CurrentWorkOffSet != "G53" {
		t.Errorf("CurrentWorkOffSet = %q, want G53", ctx.CurrentWorkOffSet)
	}
}

func TestWorkoffset_AllOffsetCodes(t *testing.T) {
	for _, code := range []string{"G53", "G54", "G55", "G56", "G57", "G58"} {
		ctx := baseCtx()
		CommandHandler{}.Handle(baseCmd(code), ctx)
		if ctx.CurrentWorkOffSet != code {
			t.Errorf("CurrentWorkOffSet = %q, want %q", ctx.CurrentWorkOffSet, code)
		}
	}
}

func TestWorkoffset_AdvancesLine(t *testing.T) {
	ctx := baseCtx()
	ctx.CurrentExecCommandLine = 2
	CommandHandler{}.Handle(baseCmd("G54"), ctx)
	if ctx.NextCmdLineToExec != 3 {
		t.Errorf("NextCmdLineToExec = %d, want 3", ctx.NextCmdLineToExec)
	}
}

func TestWorkoffset_WithInlineAxisCommand_ReturnsChildResult(t *testing.T) {
	// "G54 A90" — workoffset sets the offset then returns A90 as a sub-command
	ctx := baseCtx()
	results := CommandHandler{}.Handle(baseCmd("G54 A90"), ctx)
	if len(results) < 2 {
		t.Fatalf("expected ≥2 results for 'G54 A90', got %d", len(results))
	}
	childFound := false
	for _, r := range results {
		if r.ShouldExecute && r.Cmd.Cmd == "A90" {
			childFound = true
		}
	}
	if !childFound {
		t.Errorf("expected ShouldExecute child result for A90, none found in %+v", results)
	}
}

func TestWorkoffset_CommandName(t *testing.T) {
	got := CommandHandler{}.CommandName()
	if got != "workoffset" {
		t.Errorf("CommandName() = %q, want workoffset", got)
	}
}

// ─── CreateHandler ────────────────────────────────────────────────────────

func TestWorkoffset_CreateHandler_ReturnsHandlerWithCorrectName(t *testing.T) {
	handler := CreateHandler()
	if got := handler.CommandName(); got != "workoffset" {
		t.Errorf("CreateHandler().CommandName() = %q, want %q", got, "workoffset")
	}
}
