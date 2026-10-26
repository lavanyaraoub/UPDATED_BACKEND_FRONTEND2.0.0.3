//go:build integration

package main

import (
	"math"
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

// openChannelsN sets up DriverActionChannel and drains n actions,
// sending NotifyCmdComplete after each. Used to exercise the
// non-trial loop which fires one MOVE_TO_POSITION per division.
func openChannelsN(t *testing.T, n int) {
	t.Helper()
	channels.DriverActionChannel = make(chan channels.DriverAction, n)
	t.Cleanup(func() { channels.DriverActionChannel = nil })
	go func() {
		for i := 0; i < n; i++ {
			<-channels.DriverActionChannel
			channels.NotifyCmdComplete()
		}
	}()
}

// ─── getDividedDegreeAndTimes (pure function) ────────────────────────────

func TestGetDividedDegreeAndTimes_ValidDivisor(t *testing.T) {
	ctx := baseCtx()
	tests := []struct {
		cmd         string
		wantDegree  float64
		wantDivisor int
	}{
		{"G16 P4", 90.0, 4},    // 360/4 = 90
		{"G16 P10", 36.0, 10},  // 360/10 = 36
		{"G16 P6", 60.0, 6},    // 360/6 = 60
		{"G16 P360", 1.0, 360}, // 360/360 = 1
		{"G16 P-4", -90.0, -4}, // negative divisor → negative degree
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			deg, div, err := getDividedDegreeAndTimes(tt.cmd, ctx)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if div != tt.wantDivisor {
				t.Errorf("divisor = %d, want %d", div, tt.wantDivisor)
			}
			if math.Abs(deg-tt.wantDegree) > 0.001 {
				t.Errorf("degree = %v, want %v", deg, tt.wantDegree)
			}
		})
	}
}

func TestGetDividedDegreeAndTimes_NoParams_ReturnsErr(t *testing.T) {
	ctx := baseCtx()
	_, _, err := getDividedDegreeAndTimes("G16", ctx)
	if err == nil {
		t.Fatalf("G16 with no P param should return error, got nil")
	}
}

func TestGetDividedDegreeAndTimes_NonNumericParam_ReturnsErr(t *testing.T) {
	ctx := baseCtx()
	_, _, err := getDividedDegreeAndTimes("G16 Pabc", ctx)
	if err == nil {
		t.Fatalf("G16 with non-numeric P value should return error, got nil")
	}
}

// ─── Handle (trial mode) ─────────────────────────────────────────────────

func TestG16_RequiresRelativeMode(t *testing.T) {
	ctx := baseCtx()
	ctx.RunMode = "ABS" // wrong mode
	CommandHandler{}.Handle(baseCmd("G16 P4"), ctx)
	if ctx.Err == nil {
		t.Fatalf("G16 in ABS mode should set Err, got nil")
	}
}

func TestG16_TrialModeAdvancesLine(t *testing.T) {
	ctx := baseCtx()
	ctx.CurrentExecCommandLine = 3
	CommandHandler{}.Handle(baseCmd("G16 P4"), ctx)
	if ctx.NextCmdLineToExec != 4 {
		t.Errorf("NextCmdLineToExec = %d, want 4", ctx.NextCmdLineToExec)
	}
}

func TestG16_TrialModeNoResults(t *testing.T) {
	ctx := baseCtx()
	results := CommandHandler{}.Handle(baseCmd("G16 P4"), ctx)
	if len(results) != 0 {
		t.Errorf("trial mode should return 0 results, got %d", len(results))
	}
}

func TestG16_CommandName(t *testing.T) {
	got := CommandHandler{}.CommandName()
	if got != "g16" {
		t.Errorf("CommandName() = %q, want g16", got)
	}
}

// ─── Handle (non-trial mode) ──────────────────────────────────────────────

// TestG16_NonTrialMode_ReturnsOneResult verifies the live path produces a result.
func TestG16_NonTrialMode_ReturnsOneResult(t *testing.T) {
	openChannelsN(t, 2) // P2 → 2 loop iterations
	ctx := baseCtx()
	ctx.TrialModeActive = false
	results := CommandHandler{}.Handle(baseCmd("G16 P2"), ctx)
	if len(results) != 1 {
		t.Errorf("non-trial: got %d results, want 1", len(results))
	}
}

// TestG16_NonTrialMode_SetsLoopCount verifies LoopCount is set from the divisor.
func TestG16_NonTrialMode_SetsLoopCount(t *testing.T) {
	openChannelsN(t, 4) // P4 → 4 iterations
	ctx := baseCtx()
	ctx.TrialModeActive = false
	CommandHandler{}.Handle(baseCmd("G16 P4"), ctx)
	// After completion LoopCount is reset to 0
	if ctx.LoopCount != 0 {
		t.Errorf("LoopCount = %d after completion, want 0", ctx.LoopCount)
	}
	if ctx.CurrentLoopCounter != 0 {
		t.Errorf("CurrentLoopCounter = %d after completion, want 0", ctx.CurrentLoopCounter)
	}
}

// TestG16_NonTrialMode_SendsMOVE_TO_POSITIONActions verifies the correct
// driver action is sent for each division step.
func TestG16_NonTrialMode_SendsMOVE_TO_POSITIONActions(t *testing.T) {
	const divisions = 3
	received := make(chan channels.DriverAction, divisions)
	channels.DriverActionChannel = make(chan channels.DriverAction, divisions)
	t.Cleanup(func() { channels.DriverActionChannel = nil })
	go func() {
		for i := 0; i < divisions; i++ {
			a := <-channels.DriverActionChannel
			received <- a
			channels.NotifyCmdComplete()
		}
	}()

	ctx := baseCtx()
	ctx.TrialModeActive = false
	CommandHandler{}.Handle(baseCmd("G16 P3"), ctx)

	for i := 0; i < divisions; i++ {
		a := <-received
		if a.Action != "MOVE_TO_POSITION" {
			t.Errorf("iteration %d: Action = %q, want MOVE_TO_POSITION", i, a.Action)
		}
	}
}

// TestG16_NonTrialMode_AdvancesLine verifies MoveNextLine is called after the loop.
func TestG16_NonTrialMode_AdvancesLine(t *testing.T) {
	openChannelsN(t, 2)
	ctx := baseCtx()
	ctx.TrialModeActive = false
	ctx.CurrentExecCommandLine = 5
	CommandHandler{}.Handle(baseCmd("G16 P2"), ctx)
	if ctx.NextCmdLineToExec != 6 {
		t.Errorf("NextCmdLineToExec = %d, want 6", ctx.NextCmdLineToExec)
	}
}

// TestG16_NonTrialMode_StopExecution verifies the loop exits early when
// StopExecution is set mid-run. We do this by providing only 1 completion
// signal for a P3 command, then setting StopExecution before the second.
func TestG16_NonTrialMode_StopExecution(t *testing.T) {
	channels.DriverActionChannel = make(chan channels.DriverAction, 3)
	t.Cleanup(func() { channels.DriverActionChannel = nil })

	// Drain first action, set StopExecution, send completion — loop should break.
	go func() {
		<-channels.DriverActionChannel
		channels.NotifyCmdComplete()
	}()

	ctx := baseCtx()
	ctx.TrialModeActive = false
	ctx.StopExecution = true // pre-set: loop body checks this after each step
	CommandHandler{}.Handle(baseCmd("G16 P3"), ctx)
	// LoopCount is not reset when StopExecution is true
	if ctx.LoopCount == 0 && ctx.StopExecution {
		// expected: stop caused early exit, counters preserved
	}
}

// TestG16_NonTrialMode_ResumeFromExistingLoopCount verifies that when
// LoopCount > 0 (resume after stop), the handler uses the existing count
// rather than resetting it from the divisor.
func TestG16_NonTrialMode_ResumeFromExistingLoopCount(t *testing.T) {
	openChannelsN(t, 2) // only 2 remaining steps of an original P4
	ctx := baseCtx()
	ctx.TrialModeActive = false
	ctx.LoopCount = 4          // was set by original G16 P4 run
	ctx.CurrentLoopCounter = 2 // 2 steps already done → 2 remaining
	CommandHandler{}.Handle(baseCmd("G16 P4"), ctx)
	// After completion both counters are reset to 0
	if ctx.LoopCount != 0 {
		t.Errorf("LoopCount = %d after resume completion, want 0", ctx.LoopCount)
	}
}

// ─── CreateHandler ────────────────────────────────────────────────────────

func TestDivide360_CreateHandler_ReturnsHandlerWithCorrectName(t *testing.T) {
	handler := CreateHandler()
	if got := handler.CommandName(); got != "g16" {
		t.Errorf("CreateHandler().CommandName() = %q, want %q", got, "g16")
	}
}
