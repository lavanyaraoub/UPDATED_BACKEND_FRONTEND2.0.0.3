//go:build integration

package main

import (
	"strings"
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

func TestInvalidCommand_SetsErrWithCommandName(t *testing.T) {
	ctx := baseCtx()
	CommandHandler{}.Handle(dt.Command{Cmd: "Z999"}, ctx)
	if ctx.Err == nil {
		t.Fatalf("Err should be set for invalid command, got nil")
	}
	if !strings.Contains(ctx.Err.Error(), "Z999") {
		t.Errorf("Err = %q, want it to mention the command name", ctx.Err.Error())
	}
}

func TestInvalidCommand_AdvancesLine(t *testing.T) {
	ctx := baseCtx()
	ctx.CurrentExecCommandLine = 3
	CommandHandler{}.Handle(dt.Command{Cmd: "Z999"}, ctx)
	if ctx.NextCmdLineToExec != 4 {
		t.Errorf("NextCmdLineToExec = %d, want 4 (still advances)", ctx.NextCmdLineToExec)
	}
}

func TestInvalidCommand_CommandName(t *testing.T) {
	got := CommandHandler{}.CommandName()
	if got != "invalidCommand" {
		t.Errorf("CommandName() = %q, want invalidCommand", got)
	}
}

// ─── CreateHandler ────────────────────────────────────────────────────────

func TestInvalidCommand_CreateHandler_ReturnsHandlerWithCorrectName(t *testing.T) {
	handler := CreateHandler()
	if got := handler.CommandName(); got != "invalidCommand" {
		t.Errorf("CreateHandler().CommandName() = %q, want %q", got, "invalidCommand")
	}
}
