//go:build integration

package main

import (
	"testing"
	"time"

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

// ─── trial mode ───────────────────────────────────────────────────────────

func TestDelay_TrialMode_ReturnsNoResults(t *testing.T) {
	ctx := baseCtx()
	results := CommandHandler{}.Handle(baseCmd("D1"), ctx)
	if len(results) != 0 {
		t.Errorf("trial mode: got %d results, want 0", len(results))
	}
}

func TestDelay_TrialMode_AdvancesLine(t *testing.T) {
	ctx := baseCtx()
	ctx.CurrentExecCommandLine = 2
	CommandHandler{}.Handle(baseCmd("D1"), ctx)
	if ctx.NextCmdLineToExec != 3 {
		t.Errorf("NextCmdLineToExec = %d, want 3", ctx.NextCmdLineToExec)
	}
}

func TestDelay_TrialMode_DoesNotSleep(t *testing.T) {
	// In trial mode time.Sleep is skipped — the call should return near-instantly.
	ctx := baseCtx()
	start := time.Now()
	CommandHandler{}.Handle(baseCmd("D5"), ctx)
	if time.Since(start) > 200*time.Millisecond {
		t.Errorf("trial mode took too long — sleep should be skipped")
	}
}

func TestDelay_TrialMode_InvalidValue_SetsErr(t *testing.T) {
	// GetValueAsInt fails on a non-integer value string
	ctx := baseCtx()
	CommandHandler{}.Handle(baseCmd("DNOT_AN_INT"), ctx)
	if ctx.Err == nil {
		t.Error("expected Err for non-integer delay value, got nil")
	}
}

func TestDelay_TrialMode_NoErrForValidValue(t *testing.T) {
	ctx := baseCtx()
	CommandHandler{}.Handle(baseCmd("D2"), ctx)
	if ctx.Err != nil {
		t.Errorf("Err = %v, want nil for valid delay", ctx.Err)
	}
}

// ─── non-trial mode ───────────────────────────────────────────────────────

func TestDelay_NonTrialMode_ReturnsOneResult(t *testing.T) {
	ctx := baseCtx()
	ctx.TrialModeActive = false
	// Use D0 so the sleep is instant (0 seconds)
	results := CommandHandler{}.Handle(baseCmd("D0"), ctx)
	if len(results) != 1 {
		t.Errorf("non-trial mode: got %d results, want 1", len(results))
	}
}

func TestDelay_NonTrialMode_ResultDescriptionCorrect(t *testing.T) {
	ctx := baseCtx()
	ctx.TrialModeActive = false
	results := CommandHandler{}.Handle(baseCmd("D0"), ctx)
	if len(results) < 1 {
		t.Fatal("expected 1 result, got 0")
	}
	want := "Delay for 0 seconds"
	if results[0].Description != want {
		t.Errorf("Description = %q, want %q", results[0].Description, want)
	}
}

func TestDelay_NonTrialMode_AdvancesLine(t *testing.T) {
	ctx := baseCtx()
	ctx.TrialModeActive = false
	ctx.CurrentExecCommandLine = 4
	CommandHandler{}.Handle(baseCmd("D0"), ctx)
	if ctx.NextCmdLineToExec != 5 {
		t.Errorf("NextCmdLineToExec = %d, want 5", ctx.NextCmdLineToExec)
	}
}

func TestDelay_NonTrialMode_ActuallyDelays(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping sleep test in short mode")
	}
	ctx := baseCtx()
	ctx.TrialModeActive = false
	start := time.Now()
	// D1 = 1 second sleep
	CommandHandler{}.Handle(baseCmd("D1"), ctx)
	elapsed := time.Since(start)
	if elapsed < 900*time.Millisecond {
		t.Errorf("expected ~1s delay, elapsed = %v", elapsed)
	}
}

// ─── CommandName ──────────────────────────────────────────────────────────

func TestDelay_CommandName(t *testing.T) {
	got := CommandHandler{}.CommandName()
	if got != "delay" {
		t.Errorf("CommandName() = %q, want delay", got)
	}
}

// ─── CreateHandler ────────────────────────────────────────────────────────

func TestDelay_CreateHandler_ReturnsHandlerWithCorrectName(t *testing.T) {
	handler := CreateHandler()
	if got := handler.CommandName(); got != "delay" {
		t.Errorf("CreateHandler().CommandName() = %q, want %q", got, "delay")
	}
}
