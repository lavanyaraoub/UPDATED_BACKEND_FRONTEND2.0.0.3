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

// openChannel sets up DriverActionChannel and drains 1 action,
// sending NotifyCmdComplete once. G0 fires exactly one SET_RPM action.
func openChannel(t *testing.T) {
	t.Helper()
	channels.DriverActionChannel = make(chan channels.DriverAction, 1)
	t.Cleanup(func() { channels.DriverActionChannel = nil })
	go func() {
		<-channels.DriverActionChannel
		channels.NotifyCmdComplete()
	}()
}

// ─── trial mode ───────────────────────────────────────────────────────────

func TestG0_TrialModeReturnsNoResults(t *testing.T) {
	ctx := baseCtx()
	results := CommandHandler{}.Handle(baseCmd("G0"), ctx)
	if len(results) != 0 {
		t.Errorf("trial mode should return 0 results, got %d", len(results))
	}
}

func TestG0_TrialModeAdvancesLine(t *testing.T) {
	ctx := baseCtx()
	ctx.CurrentExecCommandLine = 3
	CommandHandler{}.Handle(baseCmd("G0"), ctx)
	if ctx.NextCmdLineToExec != 4 {
		t.Errorf("NextCmdLineToExec = %d, want 4", ctx.NextCmdLineToExec)
	}
}

func TestG0_TrialModeDoesNotSetErr(t *testing.T) {
	ctx := baseCtx()
	CommandHandler{}.Handle(baseCmd("G0"), ctx)
	if ctx.Err != nil {
		t.Errorf("Err = %v, want nil", ctx.Err)
	}
}

// ─── non-trial mode ───────────────────────────────────────────────────────

func TestG0_NonTrialMode_ReturnsOneResult(t *testing.T) {
	ctx := baseCtx()
	ctx.TrialModeActive = false
	openChannel(t)
	results := CommandHandler{}.Handle(baseCmd("G0"), ctx)
	if len(results) != 1 {
		t.Errorf("non-trial mode: got %d results, want 1", len(results))
	}
}

func TestG0_NonTrialMode_AdvancesLine(t *testing.T) {
	ctx := baseCtx()
	ctx.TrialModeActive = false
	ctx.CurrentExecCommandLine = 2
	openChannel(t)
	CommandHandler{}.Handle(baseCmd("G0"), ctx)
	if ctx.NextCmdLineToExec != 3 {
		t.Errorf("NextCmdLineToExec = %d, want 3", ctx.NextCmdLineToExec)
	}
}

func TestG0_NonTrialMode_SendsSetRPMAction(t *testing.T) {
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

	CommandHandler{}.Handle(baseCmd("G0"), ctx)

	action := <-done
	if action.Action != "SET_RPM" {
		t.Errorf("Action = %q, want SET_RPM", action.Action)
	}
	if action.Value != "20" {
		t.Errorf("Value = %q, want 20", action.Value)
	}
}

// ─── CommandName ──────────────────────────────────────────────────────────

func TestG0_CommandName(t *testing.T) {
	got := CommandHandler{}.CommandName()
	if got != "g0" {
		t.Errorf("CommandName() = %q, want g0", got)
	}
}

// ─── CreateHandler ────────────────────────────────────────────────────────

func TestG0_CreateHandler_ReturnsHandlerWithCorrectName(t *testing.T) {
	handler := CreateHandler()
	if got := handler.CommandName(); got != "g0" {
		t.Errorf("CreateHandler().CommandName() = %q, want %q", got, "g0")
	}
}
