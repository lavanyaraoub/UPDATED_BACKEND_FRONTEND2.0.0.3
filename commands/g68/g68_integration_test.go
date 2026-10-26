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
		CurrentExecCommandLine: 2,
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

func TestG68_AdvancesLine(t *testing.T) {
	ctx := baseCtx()
	CommandHandler{}.Handle(baseCmd("G68"), ctx)
	if ctx.NextCmdLineToExec != 3 {
		t.Errorf("NextCmdLineToExec = %d, want 3", ctx.NextCmdLineToExec)
	}
}

func TestG68_TrialModeReturnsNoResults(t *testing.T) {
	ctx := baseCtx()
	results := CommandHandler{}.Handle(baseCmd("G68"), ctx)
	if len(results) != 0 {
		t.Errorf("trial mode: got %d results, want 0", len(results))
	}
}

func TestG68_DoesNotSetErr(t *testing.T) {
	ctx := baseCtx()
	CommandHandler{}.Handle(baseCmd("G68"), ctx)
	if ctx.Err != nil {
		t.Errorf("Err = %v, want nil", ctx.Err)
	}
}

func TestG68_CommandName(t *testing.T) {
	got := CommandHandler{}.CommandName()
	if got != "g68" {
		t.Errorf("CommandName() = %q, want g68", got)
	}
}

// ─── non-trial mode ───────────────────────────────────────────────────────

func TestG68_NonTrialMode_ReturnsOneResult(t *testing.T) {
	openChannels(t)
	ctx := baseCtx()
	ctx.TrialModeActive = false
	results := CommandHandler{}.Handle(baseCmd("G68"), ctx)
	if len(results) != 1 {
		t.Errorf("non-trial: got %d results, want 1", len(results))
	}
}

func TestG68_NonTrialMode_SendsCorrectDriverAction(t *testing.T) {
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
	CommandHandler{}.Handle(baseCmd("G68"), ctx)

	a := <-received
	if a.Action != "SHORTEST_PATH_ENABLED" {
		t.Errorf("Action = %q, want SHORTEST_PATH_ENABLED", a.Action)
	}
	if a.Value != "true" {
		t.Errorf("Value = %q, want true", a.Value)
	}
}

func TestG68_NonTrialMode_AdvancesLine(t *testing.T) {
	openChannels(t)
	ctx := baseCtx()
	ctx.TrialModeActive = false
	ctx.CurrentExecCommandLine = 4
	CommandHandler{}.Handle(baseCmd("G68"), ctx)
	if ctx.NextCmdLineToExec != 5 {
		t.Errorf("NextCmdLineToExec = %d, want 5", ctx.NextCmdLineToExec)
	}
}

func TestG68_NonTrialMode_DoesNotSetErr(t *testing.T) {
	openChannels(t)
	ctx := baseCtx()
	ctx.TrialModeActive = false
	CommandHandler{}.Handle(baseCmd("G68"), ctx)
	if ctx.Err != nil {
		t.Errorf("Err = %v after non-trial G68, want nil", ctx.Err)
	}
}

// ─── CreateHandler ────────────────────────────────────────────────────────

func TestG68_CreateHandler_ReturnsHandlerWithCorrectName(t *testing.T) {
	handler := CreateHandler()
	if got := handler.CommandName(); got != "g68" {
		t.Errorf("CreateHandler().CommandName() = %q, want %q", got, "g68")
	}
}
