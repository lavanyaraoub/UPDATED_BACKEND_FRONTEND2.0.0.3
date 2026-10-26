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

// M99 in trial mode ends execution (does not loop back — loops would run forever).
func TestM99_TrialModeEndsExecution(t *testing.T) {
	ctx := baseCtx()
	CommandHandler{}.Handle(baseCmd("M99"), ctx)
	if ctx.NextCmdLineToExec != -1 {
		t.Errorf("NextCmdLineToExec = %d, want -1 (trial mode ends rather than loops)", ctx.NextCmdLineToExec)
	}
}

func TestM99_TrialModeReturnsNoResults(t *testing.T) {
	ctx := baseCtx()
	results := CommandHandler{}.Handle(baseCmd("M99"), ctx)
	if len(results) != 0 {
		t.Errorf("trial mode should return 0 results, got %d", len(results))
	}
}

func TestM99_CommandName(t *testing.T) {
	got := CommandHandler{}.CommandName()
	if got != "m99" {
		t.Errorf("CommandName() = %q, want m99", got)
	}
}

// ─── non-trial mode ───────────────────────────────────────────────────────

// TestM99_NonTrialMode_MovesToStart verifies M99 resets the executor to line 0
// in live mode (infinite loop behaviour). No channels are used by M99.
func TestM99_NonTrialMode_MovesToStart(t *testing.T) {
	ctx := baseCtx()
	ctx.TrialModeActive = false
	ctx.CurrentExecCommandLine = 8
	CommandHandler{}.Handle(baseCmd("M99"), ctx)
	if ctx.NextCmdLineToExec != 0 {
		t.Errorf("NextCmdLineToExec = %d, want 0 (MoveToStart)", ctx.NextCmdLineToExec)
	}
}

// TestM99_NonTrialMode_ReturnsOneResult verifies the live path produces a result.
func TestM99_NonTrialMode_ReturnsOneResult(t *testing.T) {
	ctx := baseCtx()
	ctx.TrialModeActive = false
	results := CommandHandler{}.Handle(baseCmd("M99"), ctx)
	if len(results) != 1 {
		t.Errorf("non-trial mode: got %d results, want 1", len(results))
	}
}

// TestM99_NonTrialMode_ResultDescriptionCorrect verifies the result description.
func TestM99_NonTrialMode_ResultDescriptionCorrect(t *testing.T) {
	ctx := baseCtx()
	ctx.TrialModeActive = false
	results := CommandHandler{}.Handle(baseCmd("M99"), ctx)
	if len(results) < 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Description != "start run code from beginning" {
		t.Errorf("Description = %q, want \"start run code from beginning\"", results[0].Description)
	}
}

// ─── CreateHandler ────────────────────────────────────────────────────────

func TestM99_CreateHandler_ReturnsHandlerWithCorrectName(t *testing.T) {
	handler := CreateHandler()
	if got := handler.CommandName(); got != "m99" {
		t.Errorf("CreateHandler().CommandName() = %q, want %q", got, "m99")
	}
}
