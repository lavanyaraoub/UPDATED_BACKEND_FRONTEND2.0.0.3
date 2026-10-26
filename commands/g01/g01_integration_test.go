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
		CommandMaps: dt.Execution{Command: []dt.Command{
			{Cmd: "F**", Func: "feedrate", ConsiderInBlockExecution: 0},
			{Cmd: "A**", Func: "moveRotary", ConsiderInBlockExecution: 1},
			{Cmd: "INVALID", Func: "invalidCommand"},
		}},
	}
}

func baseCmd(s string) dt.Command { return dt.Command{Cmd: s} }

func TestG01_NoParams_SetsErr(t *testing.T) {
	ctx := baseCtx()
	CommandHandler{}.Handle(baseCmd("G01"), ctx)
	if ctx.Err == nil {
		t.Fatalf("G01 with no params should set Err, got nil")
	}
	if !strings.Contains(ctx.Err.Error(), "Feed rate not specified") {
		t.Errorf("Err = %q, want 'Feed rate not specified'", ctx.Err.Error())
	}
}

func TestG01_WithFeedParam_ReturnsChildResult(t *testing.T) {
	ctx := baseCtx()
	results := CommandHandler{}.Handle(baseCmd("G01 F100"), ctx)
	childFound := false
	for _, r := range results {
		if r.ShouldExecute && r.Cmd.Cmd == "F100" {
			childFound = true
		}
	}
	if !childFound {
		t.Errorf("expected ShouldExecute child result for F100, got %+v", results)
	}
}

func TestG01_MultipleParams_AllSpawnedAsChildren(t *testing.T) {
	// "G01 F20 A90" — both F20 and A90 become sub-commands
	ctx := baseCtx()
	results := CommandHandler{}.Handle(baseCmd("G01 F20 A90"), ctx)
	found := map[string]bool{}
	for _, r := range results {
		if r.ShouldExecute {
			found[r.Cmd.Cmd] = true
		}
	}
	for _, want := range []string{"F20", "A90"} {
		if !found[want] {
			t.Errorf("child result %q not found in results %+v", want, results)
		}
	}
}

func TestG01_AdvancesLine(t *testing.T) {
	ctx := baseCtx()
	ctx.CurrentExecCommandLine = 1
	CommandHandler{}.Handle(baseCmd("G01 F20"), ctx)
	if ctx.NextCmdLineToExec != 2 {
		t.Errorf("NextCmdLineToExec = %d, want 2", ctx.NextCmdLineToExec)
	}
}

func TestG01_CommandName(t *testing.T) {
	got := CommandHandler{}.CommandName()
	if got != "g01" {
		t.Errorf("CommandName() = %q, want g01", got)
	}
}

// ─── CreateHandler ────────────────────────────────────────────────────────

func TestG01_CreateHandler_ReturnsHandlerWithCorrectName(t *testing.T) {
	handler := CreateHandler()
	if got := handler.CommandName(); got != "g01" {
		t.Errorf("CreateHandler().CommandName() = %q, want %q", got, "g01")
	}
}
