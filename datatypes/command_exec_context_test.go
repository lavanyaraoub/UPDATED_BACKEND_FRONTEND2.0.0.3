//go:build unit

package datatypes

// Tests for Command and ExecutionContext datatypes.
//
// These are pure Go structs with no external dependencies — no CGo, no
// filesystem, no hardware. Every method is deterministic and can be
// exhaustively tested at compile time.
//
// Coverage targets:
//   Command:
//     - GetCommandFirstChar, GetValue, GetValueAsInt
//     - GetCommand (exact match, wildcard A**, multi-command pipe, fallback INVALID)
//     - commandMatch, isInMultiCommand (via GetCommand)
//   ExecutionContext:
//     - ExtractString, ExtractNumeric, ExtractNumericAsFloat, ExtractNumericAsInt
//     - MoveNextLine (with and without WaitingForECS)
//     - EndExecution, MoveToStart, Reset
//     - PrepareExecutingFile (same vs different file resets state)

import (
	"errors"
	"testing"
	"time"
)

// ─── Command.GetCommandFirstChar ──────────────────────────────────────────

func TestGetCommandFirstChar(t *testing.T) {
	tests := []struct {
		cmd  string
		want string
	}{
		{"A90", "A"},
		{"G01", "G"},
		{"M30", "M"},
		{"B-10", "B"},
		{"G90;", "G"}, // semicolons stripped
		{"F100;", "F"},
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			c := Command{Cmd: tt.cmd}
			if got := c.GetCommandFirstChar(); got != tt.want {
				t.Errorf("GetCommandFirstChar(%q) = %q, want %q", tt.cmd, got, tt.want)
			}
		})
	}
}

// ─── Command.GetValue ─────────────────────────────────────────────────────

func TestGetValue(t *testing.T) {
	tests := []struct {
		cmd  string
		want string
	}{
		{"A90", "90"},
		{"G01", "01"},
		{"B-10", "-10"},
		{"F100;", "100"}, // semicolon stripped
		{"A90.5", "90.5"},
		{"M30", "30"},
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			c := Command{Cmd: tt.cmd}
			if got := c.GetValue(); got != tt.want {
				t.Errorf("GetValue(%q) = %q, want %q", tt.cmd, got, tt.want)
			}
		})
	}
}

// ─── Command.GetValueAsInt ────────────────────────────────────────────────

func TestGetValueAsInt_ValidInteger(t *testing.T) {
	tests := []struct {
		cmd  string
		want int
	}{
		{"A90", 90},
		{"G01", 1},
		{"M30", 30},
		{"R5", 5},
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			c := Command{Cmd: tt.cmd}
			got, err := c.GetValueAsInt()
			if err != nil {
				t.Fatalf("GetValueAsInt(%q) unexpected error: %v", tt.cmd, err)
			}
			if got != tt.want {
				t.Errorf("GetValueAsInt(%q) = %d, want %d", tt.cmd, got, tt.want)
			}
		})
	}
}

func TestGetValueAsInt_FloatValueErrors(t *testing.T) {
	// "A90.5" → value "90.5" → strconv.Atoi fails
	c := Command{Cmd: "A90.5"}
	_, err := c.GetValueAsInt()
	if err == nil {
		t.Errorf("GetValueAsInt(A90.5) should return error for float value, got nil")
	}
}

// ─── Execution.GetCommand ─────────────────────────────────────────────────

func buildExecution() Execution {
	return Execution{Command: []Command{
		{Cmd: "G90", Func: "absoluteMode"},
		{Cmd: "G91", Func: "relativeMode"},
		{Cmd: "A**", Func: "moveRotaryA", DriveID: 0},
		{Cmd: "B**", Func: "moveRotaryB", DriveID: 1},
		{Cmd: "G53|G54|G55|G56|G57|G58", Func: "workoffset"},
		{Cmd: "M30", Func: "endProgram"},
		{Cmd: "INVALID", Func: "invalidCommand"},
	}}
}

func TestGetCommand_ExactMatch(t *testing.T) {
	e := buildExecution()
	cmd := e.GetCommand("G90")
	if cmd.Func != "absoluteMode" {
		t.Errorf("GetCommand(G90) Func = %q, want absoluteMode", cmd.Func)
	}
}

func TestGetCommand_CaseInsensitive(t *testing.T) {
	e := buildExecution()
	cmd := e.GetCommand("g90")
	if cmd.Func != "absoluteMode" {
		t.Errorf("GetCommand(g90) Func = %q, want absoluteMode (case-insensitive)", cmd.Func)
	}
}

func TestGetCommand_WildcardA(t *testing.T) {
	e := buildExecution()
	for _, input := range []string{"A90", "A-10", "A0", "A360", "A180.5"} {
		cmd := e.GetCommand(input)
		if cmd.Func != "moveRotaryA" {
			t.Errorf("GetCommand(%q) Func = %q, want moveRotaryA", input, cmd.Func)
		}
		if cmd.DriveID != 0 {
			t.Errorf("GetCommand(%q) DriveID = %d, want 0", input, cmd.DriveID)
		}
	}
}

func TestGetCommand_WildcardB(t *testing.T) {
	e := buildExecution()
	cmd := e.GetCommand("B45")
	if cmd.Func != "moveRotaryB" || cmd.DriveID != 1 {
		t.Errorf("GetCommand(B45) = {Func:%q DriveID:%d}, want {moveRotaryB, 1}", cmd.Func, cmd.DriveID)
	}
}

func TestGetCommand_MultiCommandPipe(t *testing.T) {
	e := buildExecution()
	for _, code := range []string{"G53", "G54", "G55", "G56", "G57", "G58"} {
		cmd := e.GetCommand(code)
		if cmd.Func != "workoffset" {
			t.Errorf("GetCommand(%q) Func = %q, want workoffset", code, cmd.Func)
		}
	}
}

func TestGetCommand_UnknownCommandFallsBackToInvalid(t *testing.T) {
	e := buildExecution()
	// Must not start with A or B — those match the A** and B** wildcards.
	// commandMatch checks only the first character against the wildcard,
	// so "BLAH" matches B** and would return moveRotaryB, not INVALID.
	for _, unknown := range []string{"Z99", "X123", "Q1", "W45"} {
		cmd := e.GetCommand(unknown)
		if cmd.Func != "invalidCommand" {
			t.Errorf("GetCommand(%q) Func = %q, want invalidCommand (fallback)", unknown, cmd.Func)
		}
	}
}

func TestGetCommand_SemicolonStripped(t *testing.T) {
	e := buildExecution()
	cmd := e.GetCommand("G90;")
	if cmd.Func != "absoluteMode" {
		t.Errorf("GetCommand(G90;) Func = %q, want absoluteMode (semicolon stripped)", cmd.Func)
	}
}

// ─── ExecutionContext.ExtractString ───────────────────────────────────────

func TestExtractString(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"A90", "A"},
		{"B-10", "B"},
		{"G01", "G"},
		{"MOVE", "MOVE"},
		{"A90.5", "A"},
	}
	ctx := &ExecutionContext{}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := ctx.ExtractString(tt.input); got != tt.want {
				t.Errorf("ExtractString(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ─── ExecutionContext.ExtractNumeric ──────────────────────────────────────

func TestExtractNumeric(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"A90", "90"},
		{"B-10", "-10"},
		{"G01", "01"},
		{"A90.5", "90.5"},
		{"F100", "100"},
		{"A-180.5", "-180.5"},
	}
	ctx := &ExecutionContext{}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := ctx.ExtractNumeric(tt.input); got != tt.want {
				t.Errorf("ExtractNumeric(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestExtractNumericAsFloat(t *testing.T) {
	ctx := &ExecutionContext{}
	tests := []struct {
		input string
		want  float64
	}{
		{"A90", 90},
		{"B-10", -10},
		{"A90.5", 90.5},
		{"F0", 0},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ctx.ExtractNumericAsFloat(tt.input)
			if err != nil {
				t.Fatalf("ExtractNumericAsFloat(%q) error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("ExtractNumericAsFloat(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestExtractNumericAsInt(t *testing.T) {
	ctx := &ExecutionContext{}
	got, err := ctx.ExtractNumericAsInt("R5")
	if err != nil {
		t.Fatalf("ExtractNumericAsInt(R5) error: %v", err)
	}
	if got != 5 {
		t.Errorf("ExtractNumericAsInt(R5) = %d, want 5", got)
	}
}

// ─── ExecutionContext.MoveNextLine ────────────────────────────────────────

func TestMoveNextLine_AdvancesCounterWhenNotWaiting(t *testing.T) {
	ctx := &ExecutionContext{
		CurrentExecCommandLine: 3,
		WaitingForECS:          false,
	}
	ctx.MoveNextLine()
	if ctx.NextCmdLineToExec != 4 {
		t.Errorf("NextCmdLineToExec = %d, want 4", ctx.NextCmdLineToExec)
	}
}

func TestMoveNextLine_DoesNotAdvanceWhenWaitingForECS(t *testing.T) {
	ctx := &ExecutionContext{
		CurrentExecCommandLine: 3,
		NextCmdLineToExec:      3,
		WaitingForECS:          true,
	}
	ctx.MoveNextLine()
	if ctx.NextCmdLineToExec != 3 {
		t.Errorf("NextCmdLineToExec = %d, want 3 (unchanged while waiting for ECS)", ctx.NextCmdLineToExec)
	}
}

// ─── ExecutionContext.EndExecution ────────────────────────────────────────

func TestEndExecution_SetsNegativeOne(t *testing.T) {
	ctx := &ExecutionContext{CurrentExecCommandLine: 5, Err: errors.New("some error")}
	ctx.EndExecution()
	if ctx.NextCmdLineToExec != -1 {
		t.Errorf("NextCmdLineToExec after EndExecution = %d, want -1", ctx.NextCmdLineToExec)
	}
	// Reset should have cleared error
	if ctx.Err != nil {
		t.Errorf("Err after EndExecution = %v, want nil", ctx.Err)
	}
}

// ─── ExecutionContext.MoveToStart ─────────────────────────────────────────

func TestMoveToStart_ResetsToZero(t *testing.T) {
	ctx := &ExecutionContext{
		CurrentExecCommandLine: 10,
		NextCmdLineToExec:      10,
		StopExecution:          true,
		Err:                    errors.New("boom"),
	}
	ctx.MoveToStart()
	if ctx.NextCmdLineToExec != 0 {
		t.Errorf("NextCmdLineToExec = %d, want 0", ctx.NextCmdLineToExec)
	}
	if ctx.StopExecution {
		t.Errorf("StopExecution = true, want false after MoveToStart")
	}
	if ctx.Err != nil {
		t.Errorf("Err = %v, want nil after MoveToStart", ctx.Err)
	}
}

// ─── ExecutionContext.Reset ───────────────────────────────────────────────

func TestReset_ClearsAllTransientState(t *testing.T) {
	ctx := &ExecutionContext{
		CurrentExecCommandLine:     7,
		RestartExecFromBegining:    true,
		LinearInterpolationEnabled: true,
		NextCmdLineToExec:          7,
		Err:                        errors.New("test"),
		StopExecution:              true,
		Divide360On:                1,
		LoopCount:                  5,
		CurrentLoopCounter:         3,
	}
	ctx.Reset()

	if ctx.CurrentExecCommandLine != 0 {
		t.Errorf("CurrentExecCommandLine = %d, want 0", ctx.CurrentExecCommandLine)
	}
	if ctx.RestartExecFromBegining {
		t.Errorf("RestartExecFromBegining = true, want false")
	}
	if ctx.LinearInterpolationEnabled {
		t.Errorf("LinearInterpolationEnabled = true, want false")
	}
	if ctx.NextCmdLineToExec != 0 {
		t.Errorf("NextCmdLineToExec = %d, want 0", ctx.NextCmdLineToExec)
	}
	if ctx.Err != nil {
		t.Errorf("Err = %v, want nil", ctx.Err)
	}
	if ctx.StopExecution {
		t.Errorf("StopExecution = true, want false")
	}
	if ctx.Divide360On != 0 {
		t.Errorf("Divide360On = %d, want 0", ctx.Divide360On)
	}
	if ctx.LoopCount != 0 {
		t.Errorf("LoopCount = %d, want 0", ctx.LoopCount)
	}
	if ctx.CurrentLoopCounter != 0 {
		t.Errorf("CurrentLoopCounter = %d, want 0", ctx.CurrentLoopCounter)
	}
}

// ─── ExecutionContext.PrepareExecutingFile ────────────────────────────────

func TestPrepareExecutingFile_SameFileLeavesStateIntact(t *testing.T) {
	ctx := &ExecutionContext{
		CommandFileName:        "program.nc",
		CurrentExecCommandLine: 5,
	}
	ctx.PrepareExecutingFile("program.nc")

	// Same file: Reset() must NOT be called — line number preserved.
	if ctx.CurrentExecCommandLine != 5 {
		t.Errorf("CurrentExecCommandLine = %d, want 5 (same file, no reset)", ctx.CurrentExecCommandLine)
	}
}

func TestPrepareExecutingFile_DifferentFileResetsAndUpdatesName(t *testing.T) {
	ctx := &ExecutionContext{
		CommandFileName:        "old.nc",
		CurrentExecCommandLine: 5,
		StopExecution:          true,
	}
	ctx.PrepareExecutingFile("new.nc")

	if ctx.CommandFileName != "new.nc" {
		t.Errorf("CommandFileName = %q, want new.nc", ctx.CommandFileName)
	}
	if ctx.CurrentExecCommandLine != 0 {
		t.Errorf("CurrentExecCommandLine = %d, want 0 (new file forces reset)", ctx.CurrentExecCommandLine)
	}
	if ctx.StopExecution {
		t.Errorf("StopExecution = true, want false after reset")
	}
}

func TestPrepareExecutingFile_AlwaysClearsStopFlag(t *testing.T) {
	// Even for the same file, StopExecution is cleared.
	ctx := &ExecutionContext{
		CommandFileName: "prog.nc",
		StopExecution:   true,
	}
	ctx.PrepareExecutingFile("prog.nc")
	if ctx.StopExecution {
		t.Errorf("StopExecution = true, want false (always cleared by PrepareExecutingFile)")
	}
}

// ─── ActivateTrialMode / DeActivateTrialMode ──────────────────────────────────

func TestActivateTrialMode_SetsFlag(t *testing.T) {
	ctx := &ExecutionContext{}
	ctx.ActivateTrialMode()
	if !ctx.TrialModeActive {
		t.Error("ActivateTrialMode: TrialModeActive should be true")
	}
}

func TestDeActivateTrialMode_ClearsFlag(t *testing.T) {
	ctx := &ExecutionContext{TrialModeActive: true}
	ctx.DeActivateTrialMode()
	if ctx.TrialModeActive {
		t.Error("DeActivateTrialMode: TrialModeActive should be false")
	}
}

func TestActivateDeActivate_RoundTrip(t *testing.T) {
	ctx := &ExecutionContext{}
	ctx.ActivateTrialMode()
	ctx.DeActivateTrialMode()
	if ctx.TrialModeActive {
		t.Error("after activate+deactivate: TrialModeActive should be false")
	}
}

// ─── WaitExecuteNextCommand — guard paths only ───────────────────────────────

// WaitExecuteNextCommand blocks on a channel when neither guard fires.
// We test only the two paths that return immediately.

func TestWaitExecuteNextCommand_TrialModeActive_ReturnsImmediately(t *testing.T) {
	ctx := &ExecutionContext{TrialModeActive: true}
	done := make(chan struct{})
	go func() { ctx.WaitExecuteNextCommand(); close(done) }()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Error("WaitExecuteNextCommand (TrialMode) blocked for >100ms")
	}
}

func TestWaitExecuteNextCommand_ContinuousMode_ReturnsImmediately(t *testing.T) {
	ctx := &ExecutionContext{ExecutionMode: "continuous"}
	done := make(chan struct{})
	go func() { ctx.WaitExecuteNextCommand(); close(done) }()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Error("WaitExecuteNextCommand (continuous) blocked for >100ms")
	}
}
