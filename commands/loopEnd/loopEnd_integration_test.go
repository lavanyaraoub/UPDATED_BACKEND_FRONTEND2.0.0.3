//go:build integration

package main

import (
	"testing"

	dt "EtherCAT/datatypes"
)

func baseCtx() *dt.ExecutionContext {
	return &dt.ExecutionContext{
		TrialModeActive:        false, // loopEnd tests need real execution
		ExecutionMode:          "continuous",
		CurrentExecCommandLine: 5,
		DriveSettings:          map[string]dt.DriveSetting{},
	}
}

func baseCmd(s string) dt.Command { return dt.Command{Cmd: s} }

// ─── loopEnd (G10) ───────────────────────────────────────────────────────

func TestLoopEnd_DecrementsAndJumpsBackWhenCountRemaining(t *testing.T) {
	ctx := baseCtx()
	ctx.LoopCount = 2
	ctx.WhereLoopStarted = 2 // loop body starts at line 3 (WhereLoopStarted+1)
	ctx.CurrentExecCommandLine = 5

	CommandHandler{}.Handle(baseCmd("G10"), ctx)

	if ctx.LoopCount != 1 {
		t.Errorf("LoopCount = %d, want 1 (decremented)", ctx.LoopCount)
	}
	if ctx.NextCmdLineToExec != 3 { // WhereLoopStarted + 1
		t.Errorf("NextCmdLineToExec = %d, want 3 (jump back to loop body)", ctx.NextCmdLineToExec)
	}
}

func TestLoopEnd_AdvancesLineWhenCountExhausted(t *testing.T) {
	ctx := baseCtx()
	ctx.LoopCount = 0
	ctx.CurrentExecCommandLine = 5

	CommandHandler{}.Handle(baseCmd("G10"), ctx)

	if ctx.NextCmdLineToExec != 6 {
		t.Errorf("NextCmdLineToExec = %d, want 6 (loop done, advance)", ctx.NextCmdLineToExec)
	}
}

func TestLoopEnd_TrialModeJustAdvances(t *testing.T) {
	ctx := baseCtx()
	ctx.TrialModeActive = true
	ctx.LoopCount = 5 // would normally jump back, but trial mode skips loop
	ctx.CurrentExecCommandLine = 3

	CommandHandler{}.Handle(baseCmd("G10"), ctx)

	// In trial mode loopEnd just calls MoveNextLine — no jump
	if ctx.NextCmdLineToExec != 4 {
		t.Errorf("NextCmdLineToExec = %d, want 4 (trial mode ignores loop)", ctx.NextCmdLineToExec)
	}
	// LoopCount should be unchanged — no decrement in trial mode
	if ctx.LoopCount != 5 {
		t.Errorf("LoopCount = %d, want 5 (unchanged in trial mode)", ctx.LoopCount)
	}
}

func TestLoopEnd_CommandName(t *testing.T) {
	got := CommandHandler{}.CommandName()
	if got != "loopEnd" {
		t.Errorf("CommandName() = %q, want loopEnd", got)
	}
}

// ─── CreateHandler ────────────────────────────────────────────────────────

func TestLoopEnd_CreateHandler_ReturnsHandlerWithCorrectName(t *testing.T) {
	handler := CreateHandler()
	if got := handler.CommandName(); got != "loopEnd" {
		t.Errorf("CreateHandler().CommandName() = %q, want %q", got, "loopEnd")
	}
}
