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
		CurrentExecCommandLine: 5,
		DriveSettings:          map[string]dt.DriveSetting{},
	}
}

func baseCmd(s string) dt.Command { return dt.Command{Cmd: s} }

// ─── trial mode ───────────────────────────────────────────────────────────

func TestM30_EndsExecution(t *testing.T) {
	ctx := baseCtx()
	CommandHandler{}.Handle(baseCmd("M30"), ctx)
	if ctx.NextCmdLineToExec != -1 {
		t.Errorf("NextCmdLineToExec = %d, want -1 (EndExecution)", ctx.NextCmdLineToExec)
	}
}

func TestM30_ClearsErr(t *testing.T) {
	ctx := baseCtx()
	CommandHandler{}.Handle(baseCmd("M30"), ctx)
	if ctx.Err != nil {
		t.Errorf("Err = %v, want nil after M30", ctx.Err)
	}
}

func TestM30_TrialModeReturnsNoResults(t *testing.T) {
	ctx := baseCtx()
	results := CommandHandler{}.Handle(baseCmd("M30"), ctx)
	if len(results) != 0 {
		t.Errorf("trial mode should return 0 results, got %d", len(results))
	}
}

func TestM30_CommandName(t *testing.T) {
	got := CommandHandler{}.CommandName()
	if got != "m30" {
		t.Errorf("CommandName() = %q, want m30", got)
	}
}

// ─── non-trial mode ───────────────────────────────────────────────────────

// TestM30_NonTrialMode_ReturnsOneResult verifies the live path produces a result.
// M30 has no channel operations — the non-trial branch only appends a result
// and calls EndExecution(), so no channel setup is required.
func TestM30_NonTrialMode_ReturnsOneResult(t *testing.T) {
	ctx := baseCtx()
	ctx.TrialModeActive = false
	results := CommandHandler{}.Handle(baseCmd("M30"), ctx)
	if len(results) != 1 {
		t.Errorf("non-trial mode: got %d results, want 1", len(results))
	}
}

// TestM30_NonTrialMode_ResultDescriptionCorrect verifies the result description.
func TestM30_NonTrialMode_ResultDescriptionCorrect(t *testing.T) {
	ctx := baseCtx()
	ctx.TrialModeActive = false
	results := CommandHandler{}.Handle(baseCmd("M30"), ctx)
	if len(results) < 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Description != "end of program execution" {
		t.Errorf("Description = %q, want \"end of program execution\"", results[0].Description)
	}
}

// TestM30_NonTrialMode_EndsExecution verifies EndExecution() is called in live mode.
func TestM30_NonTrialMode_EndsExecution(t *testing.T) {
	ctx := baseCtx()
	ctx.TrialModeActive = false
	ctx.CurrentExecCommandLine = 10
	CommandHandler{}.Handle(baseCmd("M30"), ctx)
	if ctx.NextCmdLineToExec != -1 {
		t.Errorf("NextCmdLineToExec = %d, want -1 (EndExecution)", ctx.NextCmdLineToExec)
	}
}

// ─── CreateHandler ────────────────────────────────────────────────────────

func TestM30_CreateHandler_ReturnsHandlerWithCorrectName(t *testing.T) {
	handler := CreateHandler()
	if got := handler.CommandName(); got != "m30" {
		t.Errorf("CreateHandler().CommandName() = %q, want %q", got, "m30")
	}
}
