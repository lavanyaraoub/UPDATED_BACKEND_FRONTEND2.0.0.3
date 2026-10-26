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

// ─── loopStart (R command) ────────────────────────────────────────────────

func TestLoopStart_SetsLoopCountMinusOne(t *testing.T) {
	// R3; means loop 3 times — loopStart records LoopCount = 3-1 = 2
	// (the loop body runs once immediately, so remaining = N-1)
	ctx := baseCtx()
	CommandHandler{}.Handle(dt.Command{Cmd: "R3"}, ctx)
	if ctx.LoopCount != 2 {
		t.Errorf("LoopCount = %d, want 2 (3-1)", ctx.LoopCount)
	}
}

func TestLoopStart_RecordsWhereLoopStarted(t *testing.T) {
	ctx := baseCtx()
	ctx.CurrentExecCommandLine = 4
	CommandHandler{}.Handle(dt.Command{Cmd: "R5"}, ctx)
	if ctx.WhereLoopStarted != 4 {
		t.Errorf("WhereLoopStarted = %d, want 4 (current line)", ctx.WhereLoopStarted)
	}
}

func TestLoopStart_AdvancesLine(t *testing.T) {
	ctx := baseCtx()
	ctx.CurrentExecCommandLine = 2
	CommandHandler{}.Handle(dt.Command{Cmd: "R2"}, ctx)
	if ctx.NextCmdLineToExec != 3 {
		t.Errorf("NextCmdLineToExec = %d, want 3", ctx.NextCmdLineToExec)
	}
}

func TestLoopStart_InvalidValueSetsErr(t *testing.T) {
	ctx := baseCtx()
	// "R" with no number — GetValueAsInt will fail
	CommandHandler{}.Handle(dt.Command{Cmd: "R"}, ctx)
	// The handler sets ctx.Err = err from GetValueAsInt
	// A single-char command has empty value string, so Atoi("") errors.
	if ctx.Err == nil {
		t.Errorf("Err should be set for R with no number, got nil")
	}
}

func TestLoopStart_TrialModeReturnsNoResults(t *testing.T) {
	ctx := baseCtx()
	results := CommandHandler{}.Handle(dt.Command{Cmd: "R3"}, ctx)
	if len(results) != 0 {
		t.Errorf("trial mode should return 0 results, got %d", len(results))
	}
}

func TestLoopStart_CommandName(t *testing.T) {
	got := CommandHandler{}.CommandName()
	if got != "loopStart" {
		t.Errorf("CommandName() = %q, want loopStart", got)
	}
}

// ─── non-trial mode ───────────────────────────────────────────────────────
//
// loopStart has no channel operations — the non-trial branch only appends
// a result with the command description. No openChannels needed.

// TestLoopStart_NonTrialMode_ReturnsOneResult verifies live path produces a result.
func TestLoopStart_NonTrialMode_ReturnsOneResult(t *testing.T) {
	ctx := baseCtx()
	ctx.TrialModeActive = false
	results := CommandHandler{}.Handle(dt.Command{Cmd: "R3", Description: "loop 3 times"}, ctx)
	if len(results) != 1 {
		t.Errorf("non-trial: got %d results, want 1", len(results))
	}
}

// TestLoopStart_NonTrialMode_ResultDescriptionMatchesCommand verifies the result
// description is taken from the command's own Description field.
func TestLoopStart_NonTrialMode_ResultDescriptionMatchesCommand(t *testing.T) {
	ctx := baseCtx()
	ctx.TrialModeActive = false
	results := CommandHandler{}.Handle(dt.Command{Cmd: "R3", Description: "loop 3 times"}, ctx)
	if len(results) < 1 {
		t.Fatalf("expected 1 result, got 0")
	}
	if results[0].Description != "loop 3 times" {
		t.Errorf("Description = %q, want \"loop 3 times\"", results[0].Description)
	}
}

// TestLoopStart_NonTrialMode_StillSetsLoopCount verifies LoopCount is set
// in non-trial mode the same as in trial mode.
func TestLoopStart_NonTrialMode_StillSetsLoopCount(t *testing.T) {
	ctx := baseCtx()
	ctx.TrialModeActive = false
	CommandHandler{}.Handle(dt.Command{Cmd: "R5"}, ctx)
	if ctx.LoopCount != 4 {
		t.Errorf("LoopCount = %d, want 4 (5-1)", ctx.LoopCount)
	}
}

// TestLoopStart_NonTrialMode_StillAdvancesLine verifies MoveNextLine is
// called in non-trial mode.
func TestLoopStart_NonTrialMode_StillAdvancesLine(t *testing.T) {
	ctx := baseCtx()
	ctx.TrialModeActive = false
	ctx.CurrentExecCommandLine = 6
	CommandHandler{}.Handle(dt.Command{Cmd: "R2"}, ctx)
	if ctx.NextCmdLineToExec != 7 {
		t.Errorf("NextCmdLineToExec = %d, want 7", ctx.NextCmdLineToExec)
	}
}

// ─── CreateHandler ────────────────────────────────────────────────────────

func TestLoopStart_CreateHandler_ReturnsHandlerWithCorrectName(t *testing.T) {
	handler := CreateHandler()
	if got := handler.CommandName(); got != "loopStart" {
		t.Errorf("CreateHandler().CommandName() = %q, want %q", got, "loopStart")
	}
}
