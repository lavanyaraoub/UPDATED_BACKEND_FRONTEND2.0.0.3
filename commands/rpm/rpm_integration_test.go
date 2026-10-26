//go:build integration

package main

import (
	"testing"

	"EtherCAT/channels"
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

func openChannel(t *testing.T) {
	t.Helper()
	channels.DriverActionChannel = make(chan channels.DriverAction, 1)
	t.Cleanup(func() { channels.DriverActionChannel = nil })
	go func() {
		<-channels.DriverActionChannel
		channels.NotifyCmdComplete()
	}()
}

// ─── feedrate validation ──────────────────────────────────────────────────

func TestRPM_ExceedMaxFeedrate_SetsErr(t *testing.T) {
	ctx := baseCtx()
	CommandHandler{}.Handle(baseCmd("F21"), ctx)
	if ctx.Err == nil {
		t.Fatal("expected Err for feedrate > 20, got nil")
	}
	want := "Feed rate value must be within 1 to 20"
	if ctx.Err.Error() != want {
		t.Errorf("Err = %q, want %q", ctx.Err.Error(), want)
	}
}

func TestRPM_ExactlyAtMax_DoesNotSetErr(t *testing.T) {
	ctx := baseCtx()
	// trial mode — no channel needed
	CommandHandler{}.Handle(baseCmd("F20"), ctx)
	if ctx.Err != nil {
		t.Errorf("F20 should be valid, got Err = %v", ctx.Err)
	}
}

func TestRPM_ValidFeedrate_DoesNotSetErr(t *testing.T) {
	ctx := baseCtx()
	CommandHandler{}.Handle(baseCmd("F10"), ctx)
	if ctx.Err != nil {
		t.Errorf("Err = %v, want nil for F10", ctx.Err)
	}
}

func TestRPM_ZeroFeedrate_DoesNotSetErr(t *testing.T) {
	// v = 0 is ≤ 20, so no error expected per current implementation
	ctx := baseCtx()
	CommandHandler{}.Handle(baseCmd("F0"), ctx)
	if ctx.Err != nil {
		t.Errorf("Err = %v, want nil for F0", ctx.Err)
	}
}

// ─── trial mode ───────────────────────────────────────────────────────────

func TestRPM_TrialMode_ReturnsNoResults(t *testing.T) {
	ctx := baseCtx()
	results := CommandHandler{}.Handle(baseCmd("F10"), ctx)
	if len(results) != 0 {
		t.Errorf("trial mode: got %d results, want 0", len(results))
	}
}

func TestRPM_TrialMode_DoesNotAdvanceLine(t *testing.T) {
	// rpm does not call MoveNextLine — line pointer stays at current value
	ctx := baseCtx()
	ctx.CurrentExecCommandLine = 5
	CommandHandler{}.Handle(baseCmd("F10"), ctx)
	if ctx.NextCmdLineToExec != 0 {
		t.Errorf("NextCmdLineToExec = %d, want 0 (rpm does not advance)", ctx.NextCmdLineToExec)
	}
}

// ─── non-trial mode ───────────────────────────────────────────────────────

func TestRPM_NonTrialMode_ReturnsOneResult(t *testing.T) {
	ctx := baseCtx()
	ctx.TrialModeActive = false
	openChannel(t)
	results := CommandHandler{}.Handle(baseCmd("F5"), ctx)
	if len(results) != 1 {
		t.Errorf("non-trial mode: got %d results, want 1", len(results))
	}
}

func TestRPM_NonTrialMode_ResultDescriptionContainsValue(t *testing.T) {
	ctx := baseCtx()
	ctx.TrialModeActive = false
	openChannel(t)
	results := CommandHandler{}.Handle(baseCmd("F7"), ctx)
	if len(results) < 1 {
		t.Fatal("expected 1 result, got 0")
	}
	want := "Set rpm to 7"
	if results[0].Description != want {
		t.Errorf("Description = %q, want %q", results[0].Description, want)
	}
}

func TestRPM_NonTrialMode_SendsSetRPMAction(t *testing.T) {
	ctx := baseCtx()
	ctx.TrialModeActive = false
	channels.DriverActionChannel = make(chan channels.DriverAction, 1)
	t.Cleanup(func() { channels.DriverActionChannel = nil })

	done := make(chan channels.DriverAction, 1)
	go func() {
		action := <-channels.DriverActionChannel
		done <- action
		channels.NotifyCmdComplete()
	}()

	CommandHandler{}.Handle(baseCmd("F15"), ctx)

	action := <-done
	if action.Action != "SET_RPM" {
		t.Errorf("Action = %q, want SET_RPM", action.Action)
	}
	if action.Value != "15" {
		t.Errorf("Value = %q, want 15", action.Value)
	}
}

// ─── CommandName ──────────────────────────────────────────────────────────

func TestRPM_CommandName(t *testing.T) {
	got := CommandHandler{}.CommandName()
	if got != "rpm" {
		t.Errorf("CommandName() = %q, want rpm", got)
	}
}

// ─── CreateHandler ────────────────────────────────────────────────────────

func TestRpm_CreateHandler_ReturnsHandlerWithCorrectName(t *testing.T) {
	handler := CreateHandler()
	if got := handler.CommandName(); got != "rpm" {
		t.Errorf("CreateHandler().CommandName() = %q, want %q", got, "rpm")
	}
}
