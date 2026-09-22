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
		RunMode:                "REL",
		CurrentExecCommandLine: 0,
		DriveSettings:          map[string]dt.DriveSetting{},
	}
}

func baseCmd(s string) dt.Command { return dt.Command{Cmd: s} }

// openChannels initialises DriverActionChannel and runs a goroutine that
// drains it and sends the completion signal. This lets non-trial tests
// exercise the live execution path without a real motor driver.
func openChannels(t *testing.T) {
	t.Helper()
	channels.DriverActionChannel = make(chan channels.DriverAction, 1)
	t.Cleanup(func() { channels.DriverActionChannel = nil })
	go func() {
		a := <-channels.DriverActionChannel
		t.Logf("driver action: %+v", a)
		channels.NotifyCmdComplete()
	}()
}

// ─── trial mode ───────────────────────────────────────────────────────────

func TestG90_SetsAbsoluteMode(t *testing.T) {
	ctx := baseCtx()
	ctx.RunMode = "REL"
	CommandHandler{}.Handle(baseCmd("G90"), ctx)
	if ctx.RunMode != "ABS" {
		t.Errorf("RunMode = %q, want ABS", ctx.RunMode)
	}
}

func TestG90_AdvancesLine(t *testing.T) {
	ctx := baseCtx()
	ctx.CurrentExecCommandLine = 3
	CommandHandler{}.Handle(baseCmd("G90"), ctx)
	if ctx.NextCmdLineToExec != 4 {
		t.Errorf("NextCmdLineToExec = %d, want 4", ctx.NextCmdLineToExec)
	}
}

func TestG90_TrialModeReturnsNoResults(t *testing.T) {
	ctx := baseCtx()
	results := CommandHandler{}.Handle(baseCmd("G90"), ctx)
	if len(results) != 0 {
		t.Errorf("trial mode: got %d results, want 0", len(results))
	}
}

func TestG90_CommandName(t *testing.T) {
	got := CommandHandler{}.CommandName()
	if got != "g90" {
		t.Errorf("CommandName() = %q, want g90", got)
	}
}

// ─── non-trial mode ───────────────────────────────────────────────────────

func TestG90_NonTrialMode_SetsAbsoluteMode(t *testing.T) {
	openChannels(t)
	ctx := baseCtx()
	ctx.TrialModeActive = false
	ctx.RunMode = "REL"
	CommandHandler{}.Handle(baseCmd("G90"), ctx)
	if ctx.RunMode != "ABS" {
		t.Errorf("RunMode = %q, want ABS", ctx.RunMode)
	}
}

func TestG90_NonTrialMode_ReturnsOneResult(t *testing.T) {
	openChannels(t)
	ctx := baseCtx()
	ctx.TrialModeActive = false
	results := CommandHandler{}.Handle(baseCmd("G90"), ctx)
	if len(results) != 1 {
		t.Errorf("non-trial: got %d results, want 1", len(results))
	}
}

func TestG90_NonTrialMode_SendsCorrectDriverAction(t *testing.T) {
	received := make(chan channels.DriverAction, 1)
	channels.DriverActionChannel = make(chan channels.DriverAction, 1)
	t.Cleanup(func() { channels.DriverActionChannel = nil })
	go func() {
		a := <-channels.DriverActionChannel
		received <- a
		channels.NotifyCmdComplete()
	}()

	ctx := baseCtx()
	ctx.TrialModeActive = false
	CommandHandler{}.Handle(baseCmd("G90"), ctx)

	a := <-received
	if a.Action != "POSITION_MODE" {
		t.Errorf("Action = %q, want POSITION_MODE", a.Action)
	}
	if a.Value != "ABS" {
		t.Errorf("Value = %q, want ABS", a.Value)
	}
}

func TestG90_NonTrialMode_AdvancesLine(t *testing.T) {
	openChannels(t)
	ctx := baseCtx()
	ctx.TrialModeActive = false
	ctx.CurrentExecCommandLine = 7
	CommandHandler{}.Handle(baseCmd("G90"), ctx)
	if ctx.NextCmdLineToExec != 8 {
		t.Errorf("NextCmdLineToExec = %d, want 8", ctx.NextCmdLineToExec)
	}
}

// ─── CreateHandler ────────────────────────────────────────────────────────

func TestG90_CreateHandler_ReturnsHandlerWithCorrectName(t *testing.T) {
	handler := CreateHandler()
	if got := handler.CommandName(); got != "g90" {
		t.Errorf("CreateHandler().CommandName() = %q, want %q", got, "g90")
	}
}
