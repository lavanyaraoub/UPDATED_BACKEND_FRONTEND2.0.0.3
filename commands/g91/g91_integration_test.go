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
		RunMode:                "ABS",
		CurrentExecCommandLine: 0,
		DriveSettings:          map[string]dt.DriveSetting{},
	}
}

func baseCmd(s string) dt.Command { return dt.Command{Cmd: s} }

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

func TestG91_SetsRelativeMode(t *testing.T) {
	ctx := baseCtx()
	ctx.RunMode = "ABS"
	CommandHandler{}.Handle(baseCmd("G91"), ctx)
	if ctx.RunMode != "REL" {
		t.Errorf("RunMode = %q, want REL", ctx.RunMode)
	}
}

func TestG91_AdvancesLine(t *testing.T) {
	ctx := baseCtx()
	ctx.CurrentExecCommandLine = 5
	CommandHandler{}.Handle(baseCmd("G91"), ctx)
	if ctx.NextCmdLineToExec != 6 {
		t.Errorf("NextCmdLineToExec = %d, want 6", ctx.NextCmdLineToExec)
	}
}

func TestG91_TrialModeReturnsNoResults(t *testing.T) {
	ctx := baseCtx()
	results := CommandHandler{}.Handle(baseCmd("G91"), ctx)
	if len(results) != 0 {
		t.Errorf("trial mode: got %d results, want 0", len(results))
	}
}

func TestG91_CommandName(t *testing.T) {
	got := CommandHandler{}.CommandName()
	if got != "g91" {
		t.Errorf("CommandName() = %q, want g91", got)
	}
}

// ─── non-trial mode ───────────────────────────────────────────────────────

func TestG91_NonTrialMode_SetsRelativeMode(t *testing.T) {
	openChannels(t)
	ctx := baseCtx()
	ctx.TrialModeActive = false
	ctx.RunMode = "ABS"
	CommandHandler{}.Handle(baseCmd("G91"), ctx)
	if ctx.RunMode != "REL" {
		t.Errorf("RunMode = %q, want REL", ctx.RunMode)
	}
}

func TestG91_NonTrialMode_ReturnsOneResult(t *testing.T) {
	openChannels(t)
	ctx := baseCtx()
	ctx.TrialModeActive = false
	results := CommandHandler{}.Handle(baseCmd("G91"), ctx)
	if len(results) != 1 {
		t.Errorf("non-trial: got %d results, want 1", len(results))
	}
}

func TestG91_NonTrialMode_SendsCorrectDriverAction(t *testing.T) {
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
	CommandHandler{}.Handle(baseCmd("G91"), ctx)

	a := <-received
	if a.Action != "POSITION_MODE" {
		t.Errorf("Action = %q, want POSITION_MODE", a.Action)
	}
	if a.Value != "REL" {
		t.Errorf("Value = %q, want REL", a.Value)
	}
}

func TestG91_NonTrialMode_AdvancesLine(t *testing.T) {
	openChannels(t)
	ctx := baseCtx()
	ctx.TrialModeActive = false
	ctx.CurrentExecCommandLine = 2
	CommandHandler{}.Handle(baseCmd("G91"), ctx)
	if ctx.NextCmdLineToExec != 3 {
		t.Errorf("NextCmdLineToExec = %d, want 3", ctx.NextCmdLineToExec)
	}
}

// ─── CreateHandler ────────────────────────────────────────────────────────

func TestG91_CreateHandler_ReturnsHandlerWithCorrectName(t *testing.T) {
	handler := CreateHandler()
	if got := handler.CommandName(); got != "g91" {
		t.Errorf("CreateHandler().CommandName() = %q, want %q", got, "g91")
	}
}
