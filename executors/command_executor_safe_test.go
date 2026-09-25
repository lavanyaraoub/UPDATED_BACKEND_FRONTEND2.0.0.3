//go:build unit

package executors

// Unit tests for isAValidHandler, loadYmlConfig, notAPlugin, CompileProgram's
// file-error paths, and the resume/execution core (saveLastLine, readLastLine,
// ResetExecutingProgram, ResumeExecution, UpdateLastLineFromJSON,
// canExecuteGivenCommands, executeCommand, executeInLineParamFunction), plus
// the adopted resume architecture (resolveStartLine, ClearExecutionResumeState,
// runMu, replaySetupCommands).
//
// UPDATE: command_executor.go previously ran HAL's simpler resume design.
// A deliberate decision was made to adopt testenv's more robust design
// instead — see the MERGE NOTE at the top of command_executor.go for the
// full rationale and what was preserved from HAL (the fault-blocking check
// in RunCodeFile). The tests below now exercise the adopted design directly.
//
// REAL FILE SAFETY WARNING: this codebase persists resume state across
// THREE hardcoded absolute paths — last_line.txt, last_program.txt, and
// userline.json, all under /mnt/app/jamun/settings/ — not test temp files.
// On a real deployed Pi these may hold genuine in-progress/paused-program
// resume state. Every test that touches any of them snapshots and restores
// the exact original bytes of all three first (resumeStateSnapshot, below —
// same protective pattern as the fault-history file in
// motordriver/poll_driver_alarm_test.go) — never just clears or deletes them.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	dt "EtherCAT/datatypes"
)

// ─── isAValidHandler ─────────────────────────────────────────────────────

// TestIsAValidHandler_NilHandlerReturnsError verifies a nil handler is rejected.
func TestIsAValidHandler_NilHandlerReturnsError(t *testing.T) {
	err := isAValidHandler(nil, "someCommand")
	if err == nil {
		t.Errorf("isAValidHandler(nil) should return error, got nil")
	}
}

// TestIsAValidHandler_InvalidCommandNameReturnsError verifies "invalidCommand"
// is always rejected regardless of handler value.
func TestIsAValidHandler_InvalidCommandNameReturnsError(t *testing.T) {
	err := isAValidHandler(nil, "invalidCommand")
	if err == nil {
		t.Errorf("isAValidHandler with name=invalidCommand should return error, got nil")
	}
	if !strings.Contains(err.Error(), "invalidCommand") {
		t.Errorf("error = %q, want mention of 'invalidCommand'", err.Error())
	}
}

// TestIsAValidHandler_ValidHandlerReturnsNil verifies a real handler passes.
func TestIsAValidHandler_ValidHandlerReturnsNil(t *testing.T) {
	err := isAValidHandler(mockHandler{}, "G90")
	if err != nil {
		t.Errorf("isAValidHandler(mockHandler, G90) = %v, want nil", err)
	}
}

// mockHandler is a minimal h.Handler for testing isAValidHandler.
type mockHandler struct{}

func (m mockHandler) Handle(cmd dt.Command, ctx *dt.ExecutionContext) []dt.ExecutionResult {
	return nil
}
func (m mockHandler) CommandName() string { return "mock" }

// ─── loadYmlConfig ────────────────────────────────────────────────────────

// TestLoadYmlConfig_MissingConfigReturnsError verifies that when no
// execution.yml exists, loadYmlConfig returns a non-nil error.
func TestLoadYmlConfig_MissingConfigReturnsError(t *testing.T) {
	hasYmlLoaded = false
	t.Cleanup(func() { hasYmlLoaded = false })

	_, err := loadYmlConfig()
	if err == nil {
		t.Skip("execution.yml found next to test binary — skipping missing-config test")
	}
}

// TestLoadYmlConfig_CachesAfterFirstSuccessfulCall verifies the cache flag
// prevents re-parsing on subsequent calls.
func TestLoadYmlConfig_CachesAfterFirstSuccessfulCall(t *testing.T) {
	hasYmlLoaded = true
	yamlConfig = dt.YamlConfig{}
	t.Cleanup(func() {
		hasYmlLoaded = false
		yamlConfig = dt.YamlConfig{}
	})

	cfg, err := loadYmlConfig()
	if err != nil {
		t.Fatalf("loadYmlConfig() with cached state returned error: %v", err)
	}
	_ = cfg
	if !hasYmlLoaded {
		t.Errorf("hasYmlLoaded = false after cached call, want true")
	}
}

// ─── notAPlugin (plugin_loader.go) ───────────────────────────────────────

// TestNotAPlugin_DirectoryIsNotAPlugin verifies directories are excluded.
func TestNotAPlugin_DirectoryIsNotAPlugin(t *testing.T) {
	dir := t.TempDir()
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !notAPlugin(info) {
		t.Errorf("notAPlugin(directory) = false, want true")
	}
}

// TestNotAPlugin_GoFileIsNotAPlugin verifies .go source files are excluded.
func TestNotAPlugin_GoFileIsNotAPlugin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "handler.go")
	if err := os.WriteFile(path, []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !notAPlugin(info) {
		t.Errorf("notAPlugin(.go file) = false, want true")
	}
}

// TestNotAPlugin_SoFileIsAPlugin verifies .so shared library files are plugins.
func TestNotAPlugin_SoFileIsAPlugin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "g90.so")
	if err := os.WriteFile(path, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if notAPlugin(info) {
		t.Errorf("notAPlugin(.so file) = true, want false")
	}
}

// TestNotAPlugin_TxtFileIsNotAPlugin verifies non-.so regular files are excluded.
func TestNotAPlugin_TxtFileIsNotAPlugin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "readme.txt")
	if err := os.WriteFile(path, []byte("readme"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !notAPlugin(info) {
		t.Errorf("notAPlugin(.txt file) = false, want true")
	}
}

// ─── CompileProgram (file-error path) ─────────────────────────────────────

// TestCompileProgram_MissingFileReturnsError verifies CompileProgram returns
// an error immediately when the program file does not exist.
// This path only calls createCommands (pure file read) — no funcMap needed.
func TestCompileProgram_MissingFileReturnsError(t *testing.T) {
	err := CompileProgram("/nonexistent/program.nc")
	if err == nil {
		t.Errorf("CompileProgram missing file: expected error, got nil")
	}
}

// TestCompileProgram_SyntaxErrorReturnsError verifies a program with a missing
// semicolon returns "Syntax error" from createCommands before reaching funcMap.
func TestCompileProgram_SyntaxErrorReturnsError(t *testing.T) {
	f := writeTempFile(t, "G90\nM30;\n") // G90 missing semicolon
	err := CompileProgram(f)
	if err == nil {
		t.Errorf("CompileProgram syntax error: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "Syntax") {
		t.Errorf("error = %q, want 'Syntax error'", err.Error())
	}
}

// ─── HAL's resume/execution core ───────────────────────────────────────────

const lastLineFilePath = "/mnt/app/jamun/settings/last_line.txt"

type lastLineSnapshot struct {
	existed bool
	data    []byte
}

func snapshotLastLineFile(t *testing.T) lastLineSnapshot {
	t.Helper()
	data, err := os.ReadFile(lastLineFilePath)
	if err != nil {
		return lastLineSnapshot{existed: false}
	}
	return lastLineSnapshot{existed: true, data: data}
}

func (s lastLineSnapshot) restore(t *testing.T) {
	t.Helper()
	if s.existed {
		if err := os.WriteFile(lastLineFilePath, s.data, 0644); err != nil {
			t.Errorf("CRITICAL: failed to restore original last_line.txt: %v", err)
		}
	} else {
		_ = os.Remove(lastLineFilePath)
	}
}

// resetExecContextForTest clears execContext to a known-empty state so tests
// don't see leftover fields from a previous test's run.
func resetExecContextForTest() {
	execContext = dt.ExecutionContext{}
}

// TestResumeExecution_ZeroNextLine_ReturnsNilNoOp: pure logic, no file I/O.
func TestResumeExecution_ZeroNextLine_ReturnsNilNoOp(t *testing.T) {
	resetExecContextForTest()
	execContext.NextLineWhenStopped = 0

	err := ResumeExecution()
	if err != nil {
		t.Errorf("ResumeExecution with NextLineWhenStopped=0: got error %v, want nil", err)
	}
}

// requireWritableLastLineFile skips the test if the current process can't
// write to lastLineFile — on a real deployed Pi, /mnt/app/jamun/settings/
// is typically root-owned, and `go test` run as a normal user (e.g. `pi`)
// gets a permission error rather than a missing-file error. That's an
// environment/permissions fact, not a bug in the code under test, so we
// skip rather than fail red.
func requireWritableLastLineFile(t *testing.T) {
	t.Helper()
	f, err := os.OpenFile(lastLineFilePath, os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		t.Skipf("cannot write %s (%v) — likely running as a non-privileged "+
			"user against a root-owned settings directory; rerun with sudo "+
			"or as the same user the real app runs as to exercise this path", lastLineFilePath, err)
	}
	f.Close()
}

func TestSaveLastLine_ThenReadLastLine_RoundTrip(t *testing.T) {
	requireWritableLastLineFile(t)
	snap := snapshotLastLineFile(t)
	defer snap.restore(t)

	saveLastLine(42)
	got, err := readLastLine()
	if err != nil {
		t.Fatalf("readLastLine: unexpected error: %v", err)
	}
	if got != 42 {
		t.Errorf("readLastLine() = %d, want 42", got)
	}
}

func TestReadLastLine_MissingFile_ReturnsZeroNoError(t *testing.T) {
	requireWritableLastLineFile(t)
	snap := snapshotLastLineFile(t)
	_ = os.Remove(lastLineFilePath) // ensure it's absent for this test
	defer snap.restore(t)

	got, err := readLastLine()
	if err != nil {
		t.Errorf("readLastLine on missing file: unexpected error: %v", err)
	}
	if got != 0 {
		t.Errorf("readLastLine on missing file = %d, want 0", got)
	}
}

func TestReadLastLine_CorruptContent_ReturnsError(t *testing.T) {
	requireWritableLastLineFile(t)
	snap := snapshotLastLineFile(t)
	defer snap.restore(t)

	if err := os.WriteFile(lastLineFilePath, []byte("not-a-number"), 0644); err != nil {
		t.Fatalf("failed to write corrupt content for test setup: %v", err)
	}

	_, err := readLastLine()
	if err == nil {
		t.Error("readLastLine with corrupt content: expected error, got nil")
	}
}

// TestResetExecutingProgram_ResetsContext verifies ResetExecutingProgram's
// actual net effect. NOTE: ResetExecutingProgram sets StopExecution = true
// and then immediately calls execContext.Reset(), which unconditionally
// sets StopExecution back to false — so the net, observable result is
// StopExecution == false, NextLineWhenStopped == 0. (The intermediate
// `true` has no observable effect from outside the function — worth a
// second look in the source if the intent was for callers to see `true`
// at some point, but that's a question for you, not something to silently
// "fix" here.)
func TestResetExecutingProgram_ResetsContext(t *testing.T) {
	requireWritableLastLineFile(t)
	snap := snapshotLastLineFile(t)
	defer snap.restore(t)

	resetExecContextForTest()
	execContext.NextLineWhenStopped = 99

	ResetExecutingProgram()

	if execContext.StopExecution {
		t.Error("ResetExecutingProgram: StopExecution = true, want false (Reset() clears it back)")
	}
	if execContext.NextLineWhenStopped != 0 {
		t.Errorf("NextLineWhenStopped = %d, want 0", execContext.NextLineWhenStopped)
	}
	got, err := readLastLine()
	if err != nil {
		t.Fatalf("readLastLine after ResetExecutingProgram: %v", err)
	}
	if got != 0 {
		t.Errorf("last_line.txt after ResetExecutingProgram = %d, want 0", got)
	}
}

// TestUpdateLastLineFromJSON_DoesNotPanicAndRestoresLastLineFile: the
// adopted implementation reads userLineFile via readUserLine() and only
// acts (writing last_line.txt) if a valid positive user-selected line is
// present; otherwise it logs and returns without touching anything. This
// test only asserts no panic — restores last_line.txt regardless of which
// branch fires.
func TestUpdateLastLineFromJSON_DoesNotPanicAndRestoresLastLineFile(t *testing.T) {
	snap := snapshotLastLineFile(t)
	defer snap.restore(t)

	resetExecContextForTest()
	execContext.NextLineWhenStopped = -1 // sentinel to detect any change

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("UpdateLastLineFromJSON panicked: %v", r)
		}
	}()
	UpdateLastLineFromJSON()
}

// TestCanExecuteGivenCommands_EmptyCommands_ReturnsNil: the adopted
// canExecuteGivenCommands no longer calls UpdateLastLineFromJSON internally
// (that call was part of HAL's old design) — kept the snapshot/restore
// anyway since it's harmless and future-proofs against that changing again.
func TestCanExecuteGivenCommands_EmptyCommands_ReturnsNil(t *testing.T) {
	snap := snapshotLastLineFile(t)
	defer snap.restore(t)

	resetExecContextForTest()

	err := canExecuteGivenCommands([]dt.Command{})
	if err != nil {
		t.Errorf("canExecuteGivenCommands(empty): got error %v, want nil", err)
	}
	if execContext.TrialModeActive {
		t.Error("canExecuteGivenCommands should deactivate trial mode before returning")
	}
}

// ─── executeCommand / executeInLineParamFunction — pure logic ────────────

func TestExecuteCommand_InvalidCommandName_ReturnsError(t *testing.T) {
	resetExecContextForTest()
	cmd := dt.Command{Cmd: "invalidCommand", CodeLineNumber: 1}

	_, err := executeCommand(cmd, &execContext, 0)
	if err == nil {
		t.Error("executeCommand with invalidCommand: expected error, got nil")
	}
}

func TestExecuteCommand_UnknownCommand_ReturnsError(t *testing.T) {
	resetExecContextForTest()
	cmd := dt.Command{Cmd: "TOTALLY_UNKNOWN_CMD_XYZ", CodeLineNumber: 1}

	_, err := executeCommand(cmd, &execContext, 0)
	if err == nil {
		t.Error("executeCommand with unknown command: expected error, got nil")
	}
}

func TestExecuteInLineParamFunction_EmptyResults_ReturnsNil(t *testing.T) {
	resetExecContextForTest()
	err := executeInLineParamFunction(nil, dt.Command{}, &execContext, []dt.ExecutionResult{})
	if err != nil {
		t.Errorf("executeInLineParamFunction(empty results): got error %v, want nil", err)
	}
}

func TestExecuteInLineParamFunction_ResultNotShouldExecute_ReturnsNil(t *testing.T) {
	resetExecContextForTest()
	results := []dt.ExecutionResult{
		{ShouldExecute: false, Cmd: dt.Command{Cmd: "someCmd"}},
	}
	err := executeInLineParamFunction(nil, dt.Command{}, &execContext, results)
	if err != nil {
		t.Errorf("executeInLineParamFunction(ShouldExecute=false): got error %v, want nil", err)
	}
}

func TestExecuteInLineParamFunction_ResultShouldExecuteUnknownFunc_ReturnsNil(t *testing.T) {
	resetExecContextForTest()
	results := []dt.ExecutionResult{
		{ShouldExecute: true, Cmd: dt.Command{Cmd: "someCmd", Func: "NO_SUCH_FUNC_XYZ"}},
	}
	err := executeInLineParamFunction(nil, dt.Command{}, &execContext, results)
	if err != nil {
		t.Errorf("executeInLineParamFunction(unknown func): got error %v, want nil", err)
	}
}

// ─── Adopted resume architecture: resolveStartLine, ClearExecutionResumeState, runMu ─
//
// This section tests the resume design adopted from testenv (runMu guard,
// resolveStartLine, replaySetupCommands, multi-file persistent state,
// atomic writes) — see the MERGE NOTE at the top of command_executor.go for
// the full rationale.
//
// All three persisted files (last_line.txt, last_program.txt, userline.json)
// are hardcoded absolute paths under /mnt/app/jamun/settings/ — the same
// real-file-safety concern as lastLineFilePath above applies to all three.
// resumeStateSnapshot extends the existing single-file protection pattern
// to cover all three at once.

const lastProgramFilePath = "/mnt/app/jamun/settings/last_program.txt"
const userLineFilePath = "/mnt/app/jamun/settings/userline.json"

type resumeStateSnapshot struct {
	lastLine    lastLineSnapshot
	lastProgram fileSnapshot
	userLine    fileSnapshot
}

type fileSnapshot struct {
	path    string
	existed bool
	data    []byte
}

func snapshotFile(t *testing.T, path string) fileSnapshot {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return fileSnapshot{path: path, existed: false}
	}
	return fileSnapshot{path: path, existed: true, data: data}
}

func (s fileSnapshot) restore(t *testing.T) {
	t.Helper()
	if s.existed {
		if err := os.WriteFile(s.path, s.data, 0644); err != nil {
			t.Errorf("CRITICAL: failed to restore %s: %v", s.path, err)
		}
	} else {
		_ = os.Remove(s.path)
	}
}

func snapshotResumeState(t *testing.T) resumeStateSnapshot {
	t.Helper()
	return resumeStateSnapshot{
		lastLine:    snapshotLastLineFile(t),
		lastProgram: snapshotFile(t, lastProgramFilePath),
		userLine:    snapshotFile(t, userLineFilePath),
	}
}

func (s resumeStateSnapshot) restore(t *testing.T) {
	t.Helper()
	s.lastLine.restore(t)
	s.lastProgram.restore(t)
	s.userLine.restore(t)
}

// requireWritableResumeState skips the test if the current process can't
// write to the resume-state directory — same environment/permissions
// reasoning as requireWritableLastLineFile above.
func requireWritableResumeState(t *testing.T) {
	t.Helper()
	for _, path := range []string{lastLineFilePath, lastProgramFilePath, userLineFilePath} {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0644)
		if err != nil {
			t.Skipf("cannot write %s (%v) — likely running as a non-privileged "+
				"user against a root-owned settings directory; rerun with sudo "+
				"or as the same user the real app runs as", path, err)
		}
		f.Close()
	}
}

func TestResolveStartLine_RS232AlwaysStartsFresh(t *testing.T) {
	requireWritableResumeState(t)
	snap := snapshotResumeState(t)
	defer snap.restore(t)

	SetRS232Enabled(true)
	defer SetRS232Enabled(false)

	if got := resolveStartLine("/tmp/demo.nc"); got != 0 {
		t.Errorf("resolveStartLine in RS232 mode = %d, want 0", got)
	}
}

func TestResolveStartLine_UserSelectedLineTakesPriority(t *testing.T) {
	requireWritableResumeState(t)
	snap := snapshotResumeState(t)
	defer snap.restore(t)

	SetRS232Enabled(false)
	// Even with a resumable last_line for this same program, an explicit
	// user-selected line must win.
	saveLastProgram("demo.nc")
	saveLastLine(10)
	if err := os.WriteFile(userLineFilePath, []byte(`{"user_line":"5"}`), 0644); err != nil {
		t.Fatalf("test setup: %v", err)
	}

	got := resolveStartLine("/tmp/demo.nc")
	if got != 4 { // 1-based "5" -> 0-based index 4
		t.Errorf("resolveStartLine with user_line=5 = %d, want 4", got)
	}

	// One-shot: userline.json must be cleared after being consumed.
	data, _ := os.ReadFile(userLineFilePath)
	if strings.Contains(string(data), `"5"`) {
		t.Errorf("userline.json still contains the consumed value: %s", data)
	}
}

func TestResolveStartLine_SameProgramResumes(t *testing.T) {
	requireWritableResumeState(t)
	snap := snapshotResumeState(t)
	defer snap.restore(t)

	SetRS232Enabled(false)
	_ = os.WriteFile(userLineFilePath, []byte(`{"user_line":"0"}`), 0644) // no explicit selection
	saveLastProgram("demo.nc")
	saveLastLine(7)

	got := resolveStartLine("/tmp/demo.nc")
	if got != 7 {
		t.Errorf("resolveStartLine resuming same program = %d, want 7", got)
	}
}

func TestResolveStartLine_DifferentProgramStartsFresh(t *testing.T) {
	requireWritableResumeState(t)
	snap := snapshotResumeState(t)
	defer snap.restore(t)

	SetRS232Enabled(false)
	_ = os.WriteFile(userLineFilePath, []byte(`{"user_line":"0"}`), 0644)
	saveLastProgram("old_program.nc")
	saveLastLine(7)

	// Different filename — must NOT resume from old_program's saved line.
	got := resolveStartLine("/tmp/new_program.nc")
	if got != 0 {
		t.Errorf("resolveStartLine for a different program = %d, want 0 (fresh start)", got)
	}
}

func TestResolveStartLine_NoPriorState_StartsAtZero(t *testing.T) {
	requireWritableResumeState(t)
	snap := snapshotResumeState(t)
	defer snap.restore(t)

	SetRS232Enabled(false)
	clearLastLine()
	_ = os.WriteFile(userLineFilePath, []byte(`{"user_line":"0"}`), 0644)

	got := resolveStartLine("/tmp/fresh.nc")
	if got != 0 {
		t.Errorf("resolveStartLine with no prior state = %d, want 0", got)
	}
}

func TestClearExecutionResumeState_ClearsAllThreeFiles(t *testing.T) {
	requireWritableResumeState(t)
	snap := snapshotResumeState(t)
	defer snap.restore(t)

	saveLastProgram("demo.nc")
	saveLastLine(42)
	_ = os.WriteFile(userLineFilePath, []byte(`{"user_line":"3"}`), 0644)

	ClearExecutionResumeState()

	if got, _ := readLastLine(); got != 0 {
		t.Errorf("last_line.txt after ClearExecutionResumeState = %d, want 0", got)
	}
	if got := readLastProgram(); got != "" {
		t.Errorf("last_program.txt after ClearExecutionResumeState = %q, want empty", got)
	}
	if got := readUserLine(); got != 0 {
		t.Errorf("readUserLine after ClearExecutionResumeState = %d, want 0", got)
	}
}

// TestRunMu_PreventsConcurrentRunCodeFile verifies the mutex guard: a second
// RunCodeFile call while one is already holding the lock must be rejected
// immediately, not queued or run concurrently.
func TestRunMu_PreventsConcurrentRunCodeFile(t *testing.T) {
	if !runMu.TryLock() {
		t.Fatal("test setup: runMu was already locked by something else")
	}
	defer runMu.Unlock()

	err := RunCodeFile("/tmp/anything.nc")
	if err == nil {
		t.Fatal("RunCodeFile while runMu is held: expected rejection error, got nil")
	}
	if !strings.Contains(err.Error(), "already executing") {
		t.Errorf("RunCodeFile rejection message = %q, want it to mention 'already executing'", err.Error())
	}
}

// TestReplaySetupCommands_ZeroStartLine_IsNoOp verifies the guard: replay
// must do nothing when starting fresh from line 0.
func TestReplaySetupCommands_ZeroStartLine_IsNoOp(t *testing.T) {
	resetExecContextForTest()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("replaySetupCommands(startLine=0) panicked: %v", r)
		}
	}()
	replaySetupCommands([]dt.Command{{Cmd: "G90"}}, 0)
}

func TestReplaySetupCommands_EmptyCommands_IsNoOp(t *testing.T) {
	resetExecContextForTest()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("replaySetupCommands(empty commands) panicked: %v", r)
		}
	}()
	replaySetupCommands([]dt.Command{}, 5)
}
