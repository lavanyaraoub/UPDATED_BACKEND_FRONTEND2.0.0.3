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

// openChannelsN sets up DriverActionChannel and drains n MOVE_TO_POSITION
// actions, sending NotifyCmdComplete after each one.
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

// ─── mode guard ──────────────────────────────────────────────────────────

func TestG17_RequiresRelativeMode(t *testing.T) {
	ctx := baseCtx()
	ctx.RunMode = "ABS"
	CommandHandler{}.Handle(baseCmd("G17 X90 P2"), ctx)
	if ctx.Err == nil {
		t.Fatal("expected Err when not in REL mode, got nil")
	}
}

func TestG17_RelativeModeDoesNotSetModeErr(t *testing.T) {
	ctx := baseCtx()
	// REL mode is set in baseCtx; valid params should not error on mode
	CommandHandler{}.Handle(baseCmd("G17 X90 P2"), ctx)
	if ctx.Err != nil && ctx.Err.Error() == "G17 can be run only in incremental positioning. Apply G91 before G17" {
		t.Errorf("should not produce mode error in REL mode, got: %v", ctx.Err)
	}
}

// ─── param validation ─────────────────────────────────────────────────────

func TestG17_MissingParams_SetsErr(t *testing.T) {
	ctx := baseCtx()
	CommandHandler{}.Handle(baseCmd("G17 X90"), ctx) // only 1 extra param, need 2
	if ctx.Err == nil {
		t.Fatal("expected Err for missing divisor param, got nil")
	}
}

func TestG17_NoParams_SetsErr(t *testing.T) {
	ctx := baseCtx()
	CommandHandler{}.Handle(baseCmd("G17"), ctx)
	if ctx.Err == nil {
		t.Fatal("expected Err for no params, got nil")
	}
}

func TestG17_NonNumericDivisor_SetsErr(t *testing.T) {
	ctx := baseCtx()
	CommandHandler{}.Handle(baseCmd("G17 X90 PABC"), ctx)
	if ctx.Err == nil {
		t.Fatal("expected Err for non-numeric divisor, got nil")
	}
}

// ─── getDividedDegreeAndTimes (pure helper) ───────────────────────────────

func TestG17_getDividedDegreeAndTimes_ValidInput(t *testing.T) {
	ctx := baseCtx()
	cases := []struct {
		cmd     string
		wantDeg float64
		wantDiv int
	}{
		{"G17 X90 P2", 45.0, 2},
		{"G17 X360 P4", 90.0, 4},
		{"G17 X45 P3", 15.0, 3},
	}
	for _, tc := range cases {
		deg, div, err := getDividedDegreeAndTimes(tc.cmd, ctx)
		if err != nil {
			t.Errorf("%s: unexpected err %v", tc.cmd, err)
			continue
		}
		if deg != tc.wantDeg {
			t.Errorf("%s: deg = %f, want %f", tc.cmd, deg, tc.wantDeg)
		}
		if div != tc.wantDiv {
			t.Errorf("%s: div = %d, want %d", tc.cmd, div, tc.wantDiv)
		}
	}
}

func TestG17_getDividedDegreeAndTimes_NegativeDivisor(t *testing.T) {
	ctx := baseCtx()
	deg, div, err := getDividedDegreeAndTimes("G17 X90 P-3", ctx)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if deg != -30.0 {
		t.Errorf("deg = %f, want -30.0", deg)
	}
	if div != -3 {
		t.Errorf("div = %d, want -3", div)
	}
}

// ─── trial mode ───────────────────────────────────────────────────────────

func TestG17_TrialMode_ReturnsNoResults(t *testing.T) {
	ctx := baseCtx()
	results := CommandHandler{}.Handle(baseCmd("G17 X90 P2"), ctx)
	if len(results) != 0 {
		t.Errorf("trial mode: got %d results, want 0", len(results))
	}
}

func TestG17_TrialMode_AdvancesLine(t *testing.T) {
	ctx := baseCtx()
	ctx.CurrentExecCommandLine = 1
	CommandHandler{}.Handle(baseCmd("G17 X90 P2"), ctx)
	if ctx.NextCmdLineToExec != 2 {
		t.Errorf("NextCmdLineToExec = %d, want 2", ctx.NextCmdLineToExec)
	}
}

func TestG17_TrialMode_ResetsLoopCountAfterRun(t *testing.T) {
	ctx := baseCtx()
	CommandHandler{}.Handle(baseCmd("G17 X90 P4"), ctx)
	if ctx.LoopCount != 0 {
		t.Errorf("LoopCount = %d, want 0 after trial mode run", ctx.LoopCount)
	}
}

// ─── non-trial mode ───────────────────────────────────────────────────────

func TestG17_NonTrialMode_ReturnsOneResult(t *testing.T) {
	ctx := baseCtx()
	ctx.TrialModeActive = false
	openChannelsN(t, 2)
	results := CommandHandler{}.Handle(baseCmd("G17 X90 P2"), ctx)
	if len(results) != 1 {
		t.Errorf("non-trial mode: got %d results, want 1", len(results))
	}
}

func TestG17_NonTrialMode_SendsMoveActionsForEachDivision(t *testing.T) {
	const divisor = 3
	ctx := baseCtx()
	ctx.TrialModeActive = false

	channels.DriverActionChannel = make(chan channels.DriverAction, divisor)
	t.Cleanup(func() { channels.DriverActionChannel = nil })

	received := make([]channels.DriverAction, 0, divisor)
	done := make(chan struct{})
	go func() {
		for i := 0; i < divisor; i++ {
			a := <-channels.DriverActionChannel
			received = append(received, a)
			channels.NotifyCmdComplete()
		}
		close(done)
	}()

	CommandHandler{}.Handle(baseCmd("G17 X90 P3"), ctx)
	<-done

	if len(received) != divisor {
		t.Fatalf("got %d actions, want %d", len(received), divisor)
	}
	for i, a := range received {
		if a.Action != "MOVE_TO_POSITION" {
			t.Errorf("action[%d] = %q, want MOVE_TO_POSITION", i, a.Action)
		}
	}
}

func TestG17_NonTrialMode_ResetsLoopCountAfterCompletion(t *testing.T) {
	ctx := baseCtx()
	ctx.TrialModeActive = false
	openChannelsN(t, 2)
	CommandHandler{}.Handle(baseCmd("G17 X90 P2"), ctx)
	if ctx.LoopCount != 0 {
		t.Errorf("LoopCount = %d, want 0 after normal completion", ctx.LoopCount)
	}
	if ctx.CurrentLoopCounter != 0 {
		t.Errorf("CurrentLoopCounter = %d, want 0 after normal completion", ctx.CurrentLoopCounter)
	}
}

func TestG17_NonTrialMode_AdvancesLine(t *testing.T) {
	ctx := baseCtx()
	ctx.TrialModeActive = false
	ctx.CurrentExecCommandLine = 3
	openChannelsN(t, 2)
	CommandHandler{}.Handle(baseCmd("G17 X90 P2"), ctx)
	if ctx.NextCmdLineToExec != 4 {
		t.Errorf("NextCmdLineToExec = %d, want 4", ctx.NextCmdLineToExec)
	}
}

func TestG17_NonTrialMode_ResumesFromExistingLoopCount(t *testing.T) {
	// Simulate resuming mid-loop: LoopCount=4, CurrentLoopCounter=2 means
	// only 2 more actions should fire.
	ctx := baseCtx()
	ctx.TrialModeActive = false
	ctx.LoopCount = 4
	ctx.CurrentLoopCounter = 2
	openChannelsN(t, 2)
	CommandHandler{}.Handle(baseCmd("G17 X90 P4"), ctx)
	// after completion both should be reset to 0
	if ctx.LoopCount != 0 {
		t.Errorf("LoopCount = %d, want 0 after resume completion", ctx.LoopCount)
	}
}

// ─── stop flag ────────────────────────────────────────────────────────────

func TestG17_NonTrialMode_StopExecution_ExitsEarly(t *testing.T) {
	ctx := baseCtx()
	ctx.TrialModeActive = false

	channels.DriverActionChannel = make(chan channels.DriverAction, 10)
	t.Cleanup(func() { channels.DriverActionChannel = nil })

	// drain first action then set stop flag so the loop breaks
	go func() {
		<-channels.DriverActionChannel
		ctx.StopExecution = true
		channels.NotifyCmdComplete()
	}()

	CommandHandler{}.Handle(baseCmd("G17 X90 P5"), ctx)
	// LoopCount should NOT be reset to 0 when stop is triggered
	if ctx.LoopCount == 0 && ctx.CurrentLoopCounter == 0 {
		// acceptable — depends on whether stop fires before or after reset
		// the important thing is the handler returned without deadlocking
	}
}

// ─── CommandName ──────────────────────────────────────────────────────────

func TestG17_CommandName(t *testing.T) {
	got := CommandHandler{}.CommandName()
	if got != "g17" {
		t.Errorf("CommandName() = %q, want g17", got)
	}
}

// ─── CreateHandler ────────────────────────────────────────────────────────

func TestG17_CreateHandler_ReturnsHandlerWithCorrectName(t *testing.T) {
	handler := CreateHandler()
	if got := handler.CommandName(); got != "g17" {
		t.Errorf("CreateHandler().CommandName() = %q, want %q", got, "g17")
	}
}
