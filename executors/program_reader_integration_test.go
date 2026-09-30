//go:build integration

package executors

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"EtherCAT/channels"
	h "EtherCAT/commands"
	dt "EtherCAT/datatypes"
	"EtherCAT/settings"
)

func writeTempProgram(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "program.nc")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp program: %v", err)
	}
	return path
}

func TestParserCommandIntegration_StripsCommentsAndKeepsSourceLineNumbers(t *testing.T) {
	path := writeTempProgram(t, `# full-line comment
/ controller comment

G90;
A90; move A axis to 90 degrees
G91;
A-10;
`)

	commands, err := ParserCommand(path)
	if err != nil {
		t.Fatalf("ParserCommand returned error: %v", err)
	}

	wantCmds := []string{"G90", "A90", "G91", "A-10"}
	wantLines := []int{3, 4, 5, 6}
	if len(commands) != len(wantCmds) {
		t.Fatalf("parsed %d commands, want %d: %+v", len(commands), len(wantCmds), commands)
	}
	for i := range wantCmds {
		if commands[i].Cmd != wantCmds[i] {
			t.Errorf("commands[%d].Cmd = %q, want %q", i, commands[i].Cmd, wantCmds[i])
		}
		if commands[i].CodeLineNumber != wantLines[i] {
			t.Errorf("commands[%d].CodeLineNumber = %d, want %d", i, commands[i].CodeLineNumber, wantLines[i])
		}
	}
}

func TestParserCommandIntegration_RejectsLineWithoutSemicolon(t *testing.T) {
	path := writeTempProgram(t, "G90;\nA90\n")

	_, err := ParserCommand(path)
	if err == nil {
		t.Fatalf("ParserCommand should reject a command line without a semicolon")
	}
	if err.Error() != "Syntax error" {
		t.Fatalf("ParserCommand error = %q, want %q", err.Error(), "Syntax error")
	}
}

func TestParserCommandIntegration_EmptyAndCommentOnlyPrograms(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "empty", body: ""},
		{name: "comments only", body: "# setup note\n/ operator note\n\n"},
		{name: "whitespace and comments", body: "\n# comment\n/ comment\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			program := writeTempProgram(t, tc.body)
			cmds, err := ParserCommand(program)
			if err != nil {
				t.Fatalf("ParserCommand returned error: %v", err)
			}
			if len(cmds) != 0 {
				t.Fatalf("len(commands) = %d, want 0: %#v", len(cmds), cmds)
			}
		})
	}
}

func TestParserCommandIntegration_NormalizesCaseButPreservesLineNumber(t *testing.T) {
	program := writeTempProgram(t, "# header\ng90; absolute\n\na90; move\n")
	cmds, err := ParserCommand(program)
	if err != nil {
		t.Fatalf("ParserCommand returned error: %v", err)
	}
	if len(cmds) != 2 {
		t.Fatalf("len(commands) = %d, want 2", len(cmds))
	}
	if cmds[0].Cmd != "G90" || cmds[0].CodeLineNumber != 1 {
		t.Fatalf("first command = %#v, want G90 at source line 1", cmds[0])
	}
	if cmds[1].Cmd != "A90" || cmds[1].CodeLineNumber != 3 {
		t.Fatalf("second command = %#v, want A90 at source line 3", cmds[1])
	}
}

// ─── redirectPersistenceFiles ─────────────────────────────────────────────────
// Swaps the three hardcoded /mnt/app/jamun/settings/ file-path vars to a temp
// dir for the duration of a test, so integration tests never touch real Pi
// files. Returns the three temp paths; all are restored in t.Cleanup.

func redirectPersistenceFiles(t *testing.T) (llPath, lpPath, ulPath string) {
	t.Helper()
	dir := t.TempDir()
	llPath = filepath.Join(dir, "last_line.txt")
	lpPath = filepath.Join(dir, "last_program.txt")
	ulPath = filepath.Join(dir, "userline.json")
	origLL, origLP, origUL := lastLineFile, lastProgramFile, userLineFile
	lastLineFile = llPath
	lastProgramFile = lpPath
	userLineFile = ulPath
	t.Cleanup(func() {
		lastLineFile = origLL
		lastProgramFile = origLP
		userLineFile = origUL
	})
	return llPath, lpPath, ulPath
}

// ─── SetRS232Enabled / IsRS232Enabled ─────────────────────────────────────────

func TestSetRS232Enabled_TrueAndFalse(t *testing.T) {
	SetRS232Enabled(true)
	if !IsRS232Enabled() {
		t.Error("expected true after SetRS232Enabled(true)")
	}
	SetRS232Enabled(false)
	if IsRS232Enabled() {
		t.Error("expected false after SetRS232Enabled(false)")
	}
}

// ─── writeFileAtomic ──────────────────────────────────────────────────────────

func TestWriteFileAtomic_WritesContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.txt")
	if err := writeFileAtomic(path, []byte("hello")); err != nil {
		t.Fatalf("writeFileAtomic: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "hello" {
		t.Errorf("got %q want \"hello\"", string(got))
	}
}

func TestWriteFileAtomic_OverwritesExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.txt")
	_ = writeFileAtomic(path, []byte("first"))
	_ = writeFileAtomic(path, []byte("second"))
	got, _ := os.ReadFile(path)
	if string(got) != "second" {
		t.Errorf("got %q want \"second\"", string(got))
	}
}

func TestWriteFileAtomic_BadDirectory_ReturnsError(t *testing.T) {
	if err := writeFileAtomic("/nonexistent/dir/file.txt", []byte("x")); err == nil {
		t.Error("expected error writing to nonexistent directory")
	}
}

// ─── clearLastLine / saveLastLine / readLastLine ──────────────────────────────

func TestClearLastLine_WritesZeroAndEmpty(t *testing.T) {
	redirectPersistenceFiles(t)
	_ = writeFileAtomic(lastLineFile, []byte("42"))
	_ = writeFileAtomic(lastProgramFile, []byte("prog.gm"))
	clearLastLine()
	line, err := readLastLine()
	if err != nil || line != 0 {
		t.Errorf("readLastLine after clear: line=%d err=%v", line, err)
	}
	if prog := readLastProgram(); prog != "" {
		t.Errorf("readLastProgram after clear: %q want empty", prog)
	}
}

func TestSaveLastLine_ThenReadLastLine(t *testing.T) {
	redirectPersistenceFiles(t)
	saveLastLine(7)
	got, err := readLastLine()
	if err != nil {
		t.Fatalf("readLastLine: %v", err)
	}
	if got != 7 {
		t.Errorf("got %d want 7", got)
	}
}

func TestReadLastLine_MissingFile_ReturnsZero(t *testing.T) {
	redirectPersistenceFiles(t)
	got, err := readLastLine()
	if err != nil || got != 0 {
		t.Errorf("readLastLine missing: got %d err=%v", got, err)
	}
}

func TestReadLastLine_EmptyFile_ReturnsZero(t *testing.T) {
	redirectPersistenceFiles(t)
	_ = writeFileAtomic(lastLineFile, []byte(""))
	got, err := readLastLine()
	if err != nil || got != 0 {
		t.Errorf("readLastLine empty: got %d err=%v", got, err)
	}
}

// ─── saveLastProgram / readLastProgram ────────────────────────────────────────

func TestSaveLastProgram_ThenReadLastProgram(t *testing.T) {
	redirectPersistenceFiles(t)
	saveLastProgram("/path/to/motion_test.nc")
	if got := readLastProgram(); got != "motion_test.nc" {
		t.Errorf("got %q want \"motion_test.nc\"", got)
	}
}

func TestReadLastProgram_MissingFile_ReturnsEmpty(t *testing.T) {
	redirectPersistenceFiles(t)
	if got := readLastProgram(); got != "" {
		t.Errorf("got %q want empty", got)
	}
}

// ─── ClearExecutionResumeState ────────────────────────────────────────────────

func TestClearExecutionResumeState_ClearsBothFiles(t *testing.T) {
	_, _, ul := redirectPersistenceFiles(t)
	saveLastLine(5)
	saveLastProgram("test.nc")
	_ = writeFileAtomic(ul, []byte(`{"user_line":"3"}`))
	ClearExecutionResumeState()
	line, _ := readLastLine()
	prog := readLastProgram()
	if line != 0 || prog != "" {
		t.Errorf("after ClearExecutionResumeState: line=%d prog=%q", line, prog)
	}
}

// ─── resolveStartLine ─────────────────────────────────────────────────────────

func TestResolveStartLine_RS232Enabled_AlwaysReturnsZero(t *testing.T) {
	redirectPersistenceFiles(t)
	SetRS232Enabled(true)
	defer SetRS232Enabled(false)
	saveLastLine(5)
	saveLastProgram("prog.nc")
	if got := resolveStartLine("prog.nc"); got != 0 {
		t.Errorf("RS232 mode: got %d want 0", got)
	}
}

func TestResolveStartLine_DifferentProgram_StartsFromZero(t *testing.T) {
	redirectPersistenceFiles(t)
	SetRS232Enabled(false)
	saveLastLine(4)
	saveLastProgram("old_prog.nc")
	if got := resolveStartLine("new_prog.nc"); got != 0 {
		t.Errorf("different program: got %d want 0", got)
	}
}

func TestResolveStartLine_SameProgramWithSavedLine_ResumesFromLine(t *testing.T) {
	_, _, ul := redirectPersistenceFiles(t)
	SetRS232Enabled(false)
	_ = writeFileAtomic(ul, []byte(`{"user_line":"0"}`))
	saveLastProgram("motion.nc")
	saveLastLine(3)
	if got := resolveStartLine("motion.nc"); got != 3 {
		t.Errorf("same program: got %d want 3", got)
	}
}

func TestResolveStartLine_UserLineSet_UsesUserLine(t *testing.T) {
	_, _, ul := redirectPersistenceFiles(t)
	SetRS232Enabled(false)
	saveLastProgram("motion.nc")
	saveLastLine(3)
	_ = writeFileAtomic(ul, []byte(`{"user_line":"5"}`))
	// user_line=5 → startIdx=4 (1-based → 0-based)
	if got := resolveStartLine("motion.nc"); got != 4 {
		t.Errorf("user line=5: got startIdx=%d want 4", got)
	}
}

func TestResolveStartLine_NoSavedState_ReturnsZero(t *testing.T) {
	redirectPersistenceFiles(t)
	SetRS232Enabled(false)
	if got := resolveStartLine("fresh.nc"); got != 0 {
		t.Errorf("no saved state: got %d want 0", got)
	}
}

// ─── ResetExecutingProgram ────────────────────────────────────────────────────

func TestResetExecutingProgram_ClearsPersistedState(t *testing.T) {
	redirectPersistenceFiles(t)
	saveLastLine(5)
	saveLastProgram("prog.nc")
	ResetExecutingProgram()
	line, _ := readLastLine()
	if line != 0 {
		t.Errorf("lastLine should be 0 after reset, got %d", line)
	}
	if prog := readLastProgram(); prog != "" {
		t.Errorf("lastProgram should be empty after reset, got %q", prog)
	}
}

// ─── UpdateLastLineFromJSON ───────────────────────────────────────────────────

func TestUpdateLastLineFromJSON_ValidUserLine_UpdatesContext(t *testing.T) {
	ll, _, ul := redirectPersistenceFiles(t)
	_ = writeFileAtomic(ul, []byte(`{"user_line":"6"}`))
	execContext.NextLineWhenStopped = 0
	UpdateLastLineFromJSON()
	if execContext.NextLineWhenStopped != 5 {
		t.Errorf("NextLineWhenStopped=%d want 5", execContext.NextLineWhenStopped)
	}
	content, _ := os.ReadFile(ll)
	if strings.TrimSpace(string(content)) != "5" {
		t.Errorf("last_line.txt=%q want \"5\"", string(content))
	}
}

func TestUpdateLastLineFromJSON_NoUserLine_IsNoOp(t *testing.T) {
	redirectPersistenceFiles(t)
	execContext.NextLineWhenStopped = 99
	UpdateLastLineFromJSON()
	if execContext.NextLineWhenStopped != 99 {
		t.Errorf("NextLineWhenStopped changed to %d, want 99", execContext.NextLineWhenStopped)
	}
}

// ─── clearUserLine / readUserLine ─────────────────────────────────────────────

func TestClearUserLine_WritesZeroUserLine(t *testing.T) {
	_, _, ul := redirectPersistenceFiles(t)
	_ = writeFileAtomic(ul, []byte(`{"user_line":"5"}`))
	clearUserLine()
	if got := readUserLine(); got != 0 {
		t.Errorf("readUserLine after clearUserLine: got %d want 0", got)
	}
}

func TestReadUserLine_ValidLine_ReturnsValue(t *testing.T) {
	_, _, ul := redirectPersistenceFiles(t)
	_ = writeFileAtomic(ul, []byte(`{"user_line":"7"}`))
	if got := readUserLine(); got != 7 {
		t.Errorf("got %d want 7", got)
	}
}

func TestReadUserLine_MissingFile_ReturnsZero(t *testing.T) {
	redirectPersistenceFiles(t)
	if got := readUserLine(); got != 0 {
		t.Errorf("got %d want 0", got)
	}
}

func TestReadUserLine_ZeroValue_ReturnsZero(t *testing.T) {
	_, _, ul := redirectPersistenceFiles(t)
	_ = writeFileAtomic(ul, []byte(`{"user_line":"0"}`))
	if got := readUserLine(); got != 0 {
		t.Errorf("got %d want 0", got)
	}
}

func TestReadUserLine_InvalidJSON_ReturnsZero(t *testing.T) {
	_, _, ul := redirectPersistenceFiles(t)
	_ = writeFileAtomic(ul, []byte(`not valid json`))
	if got := readUserLine(); got != 0 {
		t.Errorf("got %d want 0", got)
	}
}

// ─── replaySetupCommands — guard paths ───────────────────────────────────────

func TestReplaySetupCommands_ZeroStartLine_IsNoOp(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("replaySetupCommands(startLine=0) panicked: %v", r)
		}
	}()
	replaySetupCommands([]dt.Command{{Cmd: "G90"}}, 0)
}

func TestReplaySetupCommands_EmptyCommands_IsNoOp(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("replaySetupCommands(empty) panicked: %v", r)
		}
	}()
	replaySetupCommands(nil, 3)
}

// ─── readCommandsFile ─────────────────────────────────────────────────────────

func TestReadCommandsFile_ValidFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "prog.gm")
	if err := os.WriteFile(f, []byte("G90;\nA90;\nM30;\n"), 0644); err != nil {
		t.Fatal(err)
	}
	lines, err := readCommandsFile(f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(lines) != 3 || lines[0] != "G90;" {
		t.Errorf("got %v", lines)
	}
}

func TestReadCommandsFile_EmptyFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "empty.gm")
	_ = os.WriteFile(f, []byte(""), 0644)
	lines, err := readCommandsFile(f)
	if err != nil || len(lines) != 0 {
		t.Errorf("got lines=%v err=%v", lines, err)
	}
}

func TestReadCommandsFile_MissingFile(t *testing.T) {
	if _, err := readCommandsFile("/nonexistent/prog.gm"); err == nil {
		t.Error("expected error for missing file")
	}
}

// ─── executeInLineParamFunction ───────────────────────────────────────────────

type inlineRecHandler struct {
	name  string
	calls *[]string
}

func (r inlineRecHandler) CommandName() string { return r.name }
func (r inlineRecHandler) Handle(cmd dt.Command, ctx *dt.ExecutionContext) []dt.ExecutionResult {
	if r.calls != nil {
		*r.calls = append(*r.calls, cmd.Cmd)
	}
	ctx.MoveNextLine()
	return nil
}

func TestExecuteInLineParamFunction_EmptyResultsIsNoOp(t *testing.T) {
	ctx := &dt.ExecutionContext{}
	if err := executeInLineParamFunction(inlineRecHandler{name: "G90"}, dt.Command{Cmd: "G90"}, ctx, nil); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestExecuteInLineParamFunction_ShouldExecuteFalse_SkipsHandler(t *testing.T) {
	calls := []string{}
	ctx := &dt.ExecutionContext{}
	results := []dt.ExecutionResult{{ShouldExecute: false, Cmd: dt.Command{Cmd: "G90", Func: "G90"}}}
	if err := executeInLineParamFunction(inlineRecHandler{name: "G90", calls: &calls}, dt.Command{Cmd: "G90"}, ctx, results); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(calls) != 0 {
		t.Errorf("handler called %d times, want 0", len(calls))
	}
}

func TestExecuteInLineParamFunction_ShouldExecuteTrue_InvokesHandler(t *testing.T) {
	calls := []string{}
	child := inlineRecHandler{name: "G91", calls: &calls}
	savedFuncMap := funcMap
	funcMap = map[string]h.Handler{"G91": child}
	defer func() { funcMap = savedFuncMap }()
	ctx := &dt.ExecutionContext{}
	results := []dt.ExecutionResult{{ShouldExecute: true, Cmd: dt.Command{Cmd: "G91;", Func: "G91"}}}
	if err := executeInLineParamFunction(inlineRecHandler{name: "G91"}, dt.Command{Cmd: "G91;"}, ctx, results); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(calls) != 1 || calls[0] != "G91;" {
		t.Errorf("calls=%v, want [G91;]", calls)
	}
}

// ─── waitForProgramFileUpdate ─────────────────────────────────────────────────

func TestWaitForProgramFileUpdate_MissingFile_ReturnsImmediately(t *testing.T) {
	done := make(chan struct{})
	go func() { waitForProgramFileUpdate("/nonexistent/no.gm"); close(done) }()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Error("waitForProgramFileUpdate(missing file) blocked >500ms")
	}
}

func TestWaitForProgramFileUpdate_FileUpdated_ReturnsAfterUpdate(t *testing.T) {
	f := filepath.Join(t.TempDir(), "prog.gm")
	_ = os.WriteFile(f, []byte("G90;\n"), 0644)
	done := make(chan struct{})
	go func() { waitForProgramFileUpdate(f); close(done) }()
	go func() {
		time.Sleep(50 * time.Millisecond)
		now := time.Now()
		_ = os.Chtimes(f, now, now)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("waitForProgramFileUpdate did not return after file was touched")
	}
}

// ─── listenCommandExecInput — goroutine message paths ────────────────────────

func TestListenCommandExecInput_CommandExecMode(t *testing.T) {
	ctx := &dt.ExecutionContext{ExecutionMode: "continuous"}
	channels.CommandExecInputChannel = make(chan channels.CommandExecInput, 1)
	t.Cleanup(func() { channels.CommandExecInputChannel = nil })
	listenCommandExecInput(ctx)
	channels.CommandExecInputChannel <- channels.CommandExecInput{InputType: "command_exec_mode", Data: "single"}
	time.Sleep(30 * time.Millisecond)
	if ctx.ExecutionMode != "single" {
		t.Errorf("ExecutionMode=%q want single", ctx.ExecutionMode)
	}
}

func TestListenCommandExecInput_StopProgExec(t *testing.T) {
	channels.DriverActionChannel = make(chan channels.DriverAction, 1)
	channels.CommandExecStatusChannel = make(chan channels.CommandExecStatus, 1)
	channels.CommandExecInputChannel = make(chan channels.CommandExecInput, 1)
	t.Cleanup(func() {
		channels.DriverActionChannel = nil
		channels.CommandExecInputChannel = nil
	})
	ctx := &dt.ExecutionContext{StopExecution: false}
	listenCommandExecInput(ctx)
	channels.CommandExecInputChannel <- channels.CommandExecInput{InputType: "stop_prog_exec"}
	time.Sleep(30 * time.Millisecond)
	if !ctx.StopExecution {
		t.Error("StopExecution should be true after stop_prog_exec")
	}
}

func TestListenCommandExecInput_WaitingForECS(t *testing.T) {
	channels.CommandExecInputChannel = make(chan channels.CommandExecInput, 1)
	t.Cleanup(func() { channels.CommandExecInputChannel = nil })
	ctx := &dt.ExecutionContext{WaitingForECS: false}
	listenCommandExecInput(ctx)
	channels.CommandExecInputChannel <- channels.CommandExecInput{InputType: "waiting_for_ecs"}
	time.Sleep(30 * time.Millisecond)
	if !ctx.WaitingForECS {
		t.Error("WaitingForECS should be true after waiting_for_ecs")
	}
}

func TestListenCommandExecInput_ECSDone(t *testing.T) {
	channels.CommandExecInputChannel = make(chan channels.CommandExecInput, 1)
	t.Cleanup(func() { channels.CommandExecInputChannel = nil })
	ctx := &dt.ExecutionContext{WaitingForECS: true}
	listenCommandExecInput(ctx)
	channels.CommandExecInputChannel <- channels.CommandExecInput{InputType: "ecs_done"}
	time.Sleep(30 * time.Millisecond)
	if ctx.WaitingForECS {
		t.Error("WaitingForECS should be false after ecs_done")
	}
}

func TestListenCommandExecInput_Reset(t *testing.T) {
	redirectPersistenceFiles(t)

	channels.CommandExecInputChannel = make(chan channels.CommandExecInput, 1)
	channels.CommandExecStatusChannel = make(chan channels.CommandExecStatus, 1)

	t.Cleanup(func() {
		channels.CommandExecInputChannel = nil
		channels.CommandExecStatusChannel = nil
	})

	ctx := &dt.ExecutionContext{}
	listenCommandExecInput(ctx)

	channels.CommandExecInputChannel <- channels.CommandExecInput{InputType: "reset"}

	time.Sleep(50 * time.Millisecond)

	if !ctx.HasResetted {
		t.Error("HasResetted should be true after reset")
	}
}

// TestListenCommandExecInput_MoveNextLine covers the one remaining untested
// switch case: "move_next_line" calls channels.NotifySingleModeComplete(),
// which only actually sends on SingleModeChannel if OpenSingleModeChannel()
// was called first (isSingleModeOpen guard) — otherwise it's a safe no-op.
// SingleModeChannel is unbuffered, so a receiver must be ready before the
// input fires, or the listener goroutine would block on the send forever.
func TestListenCommandExecInput_MoveNextLine(t *testing.T) {
	channels.CommandExecInputChannel = make(chan channels.CommandExecInput, 1)
	channels.SingleModeChannel = make(chan channels.CommandExecStatus)
	channels.OpenSingleModeChannel()
	t.Cleanup(func() {
		channels.CommandExecInputChannel = nil
	})

	ctx := &dt.ExecutionContext{}
	listenCommandExecInput(ctx)

	received := make(chan struct{})
	go func() {
		<-channels.SingleModeChannel
		close(received)
	}()

	channels.CommandExecInputChannel <- channels.CommandExecInput{InputType: "move_next_line"}

	select {
	case <-received:
		// Confirmed: move_next_line correctly triggered NotifySingleModeComplete.
	case <-time.After(1 * time.Second):
		t.Error("move_next_line did not trigger a message on SingleModeChannel within 1s")
	}
}

// ─── executeCommands — integration with mock handlers ────────────────────────

type execRecHandler struct {
	name   string
	calls  *[]string
	result []dt.ExecutionResult
	err    error
	jumpTo *int
}

func (r execRecHandler) CommandName() string { return r.name }
func (r execRecHandler) Handle(cmd dt.Command, ctx *dt.ExecutionContext) []dt.ExecutionResult {
	if r.calls != nil {
		*r.calls = append(*r.calls, cmd.Cmd)
	}

	if r.jumpTo != nil {
		ctx.NextCmdLineToExec = *r.jumpTo
	} else {
		ctx.MoveNextLine()
	}

	if r.err != nil {
		ctx.Err = r.err
	}

	return r.result
}

type execModeHandler struct {
	name  string
	mode  string
	calls *[]string
}

func (h execModeHandler) CommandName() string { return h.name }
func (h execModeHandler) Handle(cmd dt.Command, ctx *dt.ExecutionContext) []dt.ExecutionResult {
	if h.calls != nil {
		*h.calls = append(*h.calls, cmd.Cmd)
	}
	ctx.RunMode = h.mode
	ctx.MoveNextLine()
	return nil
}

type execMotionHandler struct {
	name  string
	calls *[]string
}

func (h execMotionHandler) CommandName() string { return h.name }
func (h execMotionHandler) Handle(cmd dt.Command, ctx *dt.ExecutionContext) []dt.ExecutionResult {
	if h.calls != nil {
		*h.calls = append(*h.calls, cmd.Cmd+":"+ctx.RunMode+":"+ctx.ExtractNumeric(cmd.Cmd))
	}
	ctx.MoveNextLine()
	return nil
}

// setupExecTest wires funcMap, yamlConfig, execContext, and BroadCastUIChannel
// for a test and restores originals in t.Cleanup.
func setupExecTest(t *testing.T, cfg dt.YamlConfig, handlers map[string]h.Handler) {
	t.Helper()
	oldFuncMap := funcMap
	oldYamlConfig := yamlConfig
	oldHasYmlLoaded := hasYmlLoaded
	oldExecContext := execContext
	oldRS232 := IsRS232Enabled()
	oldBroadcast := channels.BroadCastUIChannel
	funcMap = handlers
	yamlConfig = cfg
	hasYmlLoaded = true
	execContext = dt.ExecutionContext{
		CommandMaps:   cfg.Execution,
		ExecutionMode: "continuous",
		DriveSettings: map[string]dt.DriveSetting{},
	}
	execContext.Reset()
	SetRS232Enabled(false)
	channels.BroadCastUIChannel = make(chan channels.SocketMessage, 100)
	t.Cleanup(func() {
		funcMap = oldFuncMap
		yamlConfig = oldYamlConfig
		hasYmlLoaded = oldHasYmlLoaded
		execContext = oldExecContext
		SetRS232Enabled(oldRS232)
		channels.BroadCastUIChannel = oldBroadcast
	})
}

func TestExecuteCommands_SequentialFlow(t *testing.T) {
	var calls []string
	cfg := dt.YamlConfig{Execution: dt.Execution{Command: []dt.Command{
		{Cmd: "G90", Func: "abs"}, {Cmd: "G91", Func: "rel"}, {Cmd: "M30", Func: "end"},
	}}}
	setupExecTest(t, cfg, map[string]h.Handler{
		"abs": execRecHandler{name: "abs", calls: &calls},
		"rel": execRecHandler{name: "rel", calls: &calls},
		"end": execRecHandler{name: "end", calls: &calls},
	})
	if err := executeCommands([]dt.Command{{Cmd: "G90"}, {Cmd: "G91"}, {Cmd: "M30"}}, 0); err != nil {
		t.Fatalf("executeCommands: %v", err)
	}
	if !reflect.DeepEqual(calls, []string{"G90", "G91", "M30"}) {
		t.Fatalf("calls=%v", calls)
	}
}

func TestExecuteCommands_StopsOnHandlerError(t *testing.T) {
	var calls []string
	boom := errors.New("boom")
	cfg := dt.YamlConfig{Execution: dt.Execution{Command: []dt.Command{
		{Cmd: "G90", Func: "ok"}, {Cmd: "BAD", Func: "bad"}, {Cmd: "M30", Func: "end"},
	}}}
	setupExecTest(t, cfg, map[string]h.Handler{
		"ok":  execRecHandler{name: "ok", calls: &calls},
		"bad": execRecHandler{name: "bad", calls: &calls, err: boom},
		"end": execRecHandler{name: "end", calls: &calls},
	})
	err := executeCommands([]dt.Command{{Cmd: "G90"}, {Cmd: "BAD"}, {Cmd: "M30"}}, 0)
	if !errors.Is(err, boom) {
		t.Fatalf("error=%v want %v", err, boom)
	}
	if !reflect.DeepEqual(calls, []string{"G90", "BAD"}) {
		t.Fatalf("calls=%v", calls)
	}
}

func TestExecuteCommands_HonoursJumpTarget(t *testing.T) {
	var calls []string
	jumpTo := 3
	cfg := dt.YamlConfig{Execution: dt.Execution{Command: []dt.Command{
		{Cmd: "G90", Func: "mode"}, {Cmd: "R2", Func: "jump"},
		{Cmd: "A90", Func: "move", ConsiderInBlockExecution: 1}, {Cmd: "M30", Func: "end"},
	}}}
	setupExecTest(t, cfg, map[string]h.Handler{
		"mode": execRecHandler{name: "mode", calls: &calls},
		"jump": execRecHandler{name: "jump", calls: &calls, jumpTo: &jumpTo},
		"move": execRecHandler{name: "move", calls: &calls},
		"end":  execRecHandler{name: "end", calls: &calls},
	})
	if err := executeCommands([]dt.Command{{Cmd: "G90"}, {Cmd: "R2"}, {Cmd: "A90"}, {Cmd: "M30"}}, 0); err != nil {
		t.Fatalf("executeCommands: %v", err)
	}
	if !reflect.DeepEqual(calls, []string{"G90", "R2", "M30"}) {
		t.Fatalf("calls=%v want [G90 R2 M30]", calls)
	}
}

func TestExecuteCommands_UnknownCommandFailsFast(t *testing.T) {
	var calls []string
	cfg := dt.YamlConfig{Execution: dt.Execution{Command: []dt.Command{
		{Cmd: "G90", Func: "abs"}, {Cmd: "INVALID", Func: "invalidCommand"},
	}}}
	setupExecTest(t, cfg, map[string]h.Handler{
		"abs": execRecHandler{name: "abs", calls: &calls},
	})
	err := executeCommands([]dt.Command{{Cmd: "G90"}, {Cmd: "X999"}}, 0)
	if err == nil || !strings.Contains(err.Error(), "X999") {
		t.Fatalf("error=%v, want it to mention X999", err)
	}
}

func TestExecuteCommands_StopFlagExitsImmediately(t *testing.T) {
	var calls []string
	cfg := dt.YamlConfig{Execution: dt.Execution{Command: []dt.Command{
		{Cmd: "G90", Func: "abs"}, {Cmd: "INVALID", Func: "invalidCommand"},
	}}}
	setupExecTest(t, cfg, map[string]h.Handler{
		"abs": execRecHandler{name: "abs", calls: &calls},
	})
	execContext.StopExecution = true
	if err := executeCommands([]dt.Command{{Cmd: "G90"}, {Cmd: "G90"}}, 0); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(calls) != 0 {
		t.Errorf("expected 0 calls with stop flag, got %d", len(calls))
	}
}

func TestExecuteCommands_AbsRelMotionFlow(t *testing.T) {
	var calls []string
	cfg := dt.YamlConfig{Execution: dt.Execution{Command: []dt.Command{
		{Cmd: "G90", Func: "abs"}, {Cmd: "A**", Func: "move"},
		{Cmd: "G91", Func: "rel"}, {Cmd: "M30", Func: "end"},
		{Cmd: "INVALID", Func: "invalidCommand"},
	}}}
	setupExecTest(t, cfg, map[string]h.Handler{
		"abs":  execModeHandler{name: "abs", mode: "ABS", calls: &calls},
		"rel":  execModeHandler{name: "rel", mode: "REL", calls: &calls},
		"move": execMotionHandler{name: "move", calls: &calls},
		"end":  execRecHandler{name: "end", calls: &calls},
	})
	if err := executeCommands([]dt.Command{{Cmd: "G90"}, {Cmd: "A90"}, {Cmd: "G91"}, {Cmd: "A10"}, {Cmd: "M30"}}, 0); err != nil {
		t.Fatalf("executeCommands: %v", err)
	}
	want := []string{"G90", "A90:ABS:90", "G91", "A10:REL:10", "M30"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%v want %v", calls, want)
	}
}

// ─── replaySetupCommands — integration ───────────────────────────────────────

func TestReplaySetupCommands_ReplaysOnlySetupCmds(t *testing.T) {
	var calls []string
	cfg := dt.YamlConfig{Execution: dt.Execution{Command: []dt.Command{
		{Cmd: "G90", Func: "abs", ConsiderInBlockExecution: 0},
		{Cmd: "F**", Func: "feed", ConsiderInBlockExecution: 0},
		{Cmd: "A**", Func: "move", ConsiderInBlockExecution: 1},
		{Cmd: "M30", Func: "end", ConsiderInBlockExecution: 0},
		{Cmd: "INVALID", Func: "invalidCommand"},
	}}}
	setupExecTest(t, cfg, map[string]h.Handler{
		"abs":  execRecHandler{name: "abs", calls: &calls},
		"feed": execRecHandler{name: "feed", calls: &calls},
		"move": execRecHandler{name: "move", calls: &calls},
		"end":  execRecHandler{name: "end", calls: &calls},
	})
	replaySetupCommands([]dt.Command{{Cmd: "G90"}, {Cmd: "F100"}, {Cmd: "A90"}, {Cmd: "M30"}}, 4)
	if !reflect.DeepEqual(calls, []string{"G90", "F100"}) {
		t.Fatalf("calls=%v want [G90 F100]", calls)
	}
}

// ─── CompileProgram ───────────────────────────────────────────────────────────

func TestCompileProgram_ValidProgram_ReturnsNil(t *testing.T) {
	cfg := dt.YamlConfig{Execution: dt.Execution{Command: []dt.Command{
		{Cmd: "G90", Func: "abs"}, {Cmd: "M30", Func: "end"},
		{Cmd: "INVALID", Func: "invalidCommand"},
	}}}
	calls := []string{}
	setupExecTest(t, cfg, map[string]h.Handler{
		"abs": execRecHandler{name: "abs", calls: &calls},
		"end": execRecHandler{name: "end", calls: &calls},
	})
	path := filepath.Join(t.TempDir(), "prog.nc")
	_ = os.WriteFile(path, []byte("G90;\nM30;\n"), 0o644)
	if err := CompileProgram(path); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// ─── ResumeExecution ──────────────────────────────────────────────────────────

func TestResumeExecution_ZeroNextLine_IsNoOp(t *testing.T) {
	cfg := dt.YamlConfig{Execution: dt.Execution{}}
	setupExecTest(t, cfg, map[string]h.Handler{})
	execContext.NextLineWhenStopped = 0
	if err := ResumeExecution(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestResumeExecution_NonZero_ResumesFromThatLine(t *testing.T) {
	var calls []string
	cfg := dt.YamlConfig{Execution: dt.Execution{Command: []dt.Command{
		{Cmd: "G90", Func: "rec"}, {Cmd: "G91", Func: "rec"},
		{Cmd: "M30", Func: "rec"}, {Cmd: "INVALID", Func: "invalidCommand"},
	}}}
	setupExecTest(t, cfg, map[string]h.Handler{
		"rec": execRecHandler{name: "rec", calls: &calls},
	})
	path := filepath.Join(t.TempDir(), "prog.nc")
	_ = os.WriteFile(path, []byte("G90;\nG91;\nM30;\n"), 0o644)
	commands, err := createCommands(path)
	if err != nil {
		t.Fatalf("createCommands: %v", err)
	}
	execContext.Commands = commands
	execContext.NextLineWhenStopped = 1 // skip G90
	execContext.StopExecution = false
	if err := ResumeExecution(); err != nil {
		t.Fatalf("ResumeExecution: %v", err)
	}
	for _, c := range calls {
		if c == "G90" {
			t.Errorf("G90 was called but resume started from line 1")
		}
	}
}

// ─── setDriveSettings ─────────────────────────────────────────────────────────

func TestSetDriveSettings_MapsSettingsIntoExecContext(t *testing.T) {
	cfg := dt.YamlConfig{Execution: dt.Execution{}}
	setupExecTest(t, cfg, map[string]h.Handler{})
	settings.SetDriverSettings("A", settings.DriverSettings{POT: 180, NOT: -180})
	setDriveSettings()
	ds, ok := execContext.DriveSettings["A"]
	if !ok {
		t.Fatalf("DriveSettings[A] not set")
	}
	if ds.POTLimit != 180 || ds.NOTLimit != -180 {
		t.Errorf("DriveSettings[A]=%+v", ds)
	}
}

// ─── canExecuteGivenCommands ──────────────────────────────────────────────────

func TestCanExecuteGivenCommands_EmptyCommands_ReturnsNil(t *testing.T) {
	cfg := dt.YamlConfig{Execution: dt.Execution{}}
	setupExecTest(t, cfg, map[string]h.Handler{})
	if err := canExecuteGivenCommands([]dt.Command{}); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if execContext.TrialModeActive {
		t.Error("trial mode should be deactivated after canExecuteGivenCommands")
	}
}

// ─── loadYmlConfig — cache path ───────────────────────────────────────────────

func TestLoadYmlConfig_ReturnsCache_WhenAlreadyLoaded(t *testing.T) {
	old, oldCfg := hasYmlLoaded, yamlConfig
	hasYmlLoaded = true
	yamlConfig = dt.YamlConfig{Execution: dt.Execution{Command: []dt.Command{{Cmd: "G90", Func: "abs"}}}}
	defer func() { hasYmlLoaded = old; yamlConfig = oldCfg }()
	cfg, err := loadYmlConfig()
	if err != nil {
		t.Fatalf("loadYmlConfig: %v", err)
	}
	if len(cfg.Execution.Command) != 1 {
		t.Errorf("expected 1 cached command, got %d", len(cfg.Execution.Command))
	}
}

// ─── UpdateLastLineFromJSON / ResetExecutingProgram ───────────────────────────

func TestUpdateLastLineFromJSON_ValidUserLine(t *testing.T) {
	ll, _, ul := redirectPersistenceFiles(t)
	data, _ := json.Marshal(map[string]string{"user_line": "6"})
	_ = os.WriteFile(ul, data, 0o644)
	cfg := dt.YamlConfig{Execution: dt.Execution{}}
	setupExecTest(t, cfg, map[string]h.Handler{})
	UpdateLastLineFromJSON()
	if execContext.NextLineWhenStopped != 5 {
		t.Errorf("NextLineWhenStopped=%d want 5", execContext.NextLineWhenStopped)
	}
	content, _ := os.ReadFile(ll)
	if strings.TrimSpace(string(content)) != "5" {
		t.Errorf("last_line.txt=%q want 5", string(content))
	}
}

func TestResetExecutingProgram_ClearsContextAndFiles(t *testing.T) {
	redirectPersistenceFiles(t)
	cfg := dt.YamlConfig{Execution: dt.Execution{}}
	setupExecTest(t, cfg, map[string]h.Handler{})
	execContext.CurrentExecCommandLine = 5
	execContext.LoopCount = 3
	ResetExecutingProgram()
	if execContext.CurrentExecCommandLine != 0 || execContext.LoopCount != 0 {
		t.Errorf("context not zeroed: line=%d loop=%d",
			execContext.CurrentExecCommandLine, execContext.LoopCount)
	}
}
