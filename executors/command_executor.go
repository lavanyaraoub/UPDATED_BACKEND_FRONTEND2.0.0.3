package executors

// MERGE NOTE (resume-architecture adoption):
// This file previously ran HAL's simpler resume design (single last_line.txt,
// no concurrency guard, no timeout on waitForProgramFileUpdate). Per a
// deliberate decision to adopt testenv's more robust design, this file now
// uses testenv's architecture:
//   - runMu: prevents two RunCodeFile calls from ever overlapping
//   - resolveStartLine / replaySetupCommands: correctly decides where to
//     resume and replays non-motion setup commands (feedrate, ABS/REL mode,
//     work offset) so resuming mid-program doesn't run at the wrong speed
//     or in the wrong mode
//   - Persistent state across three files (last_line.txt, last_program.txt,
//     userline.json) with atomic writes (writeFileAtomic) instead of a
//     single plain os.WriteFile
//   - A 60-second timeout on waitForProgramFileUpdate — HAL's old version
//     could block forever if an expected RS232 file update never arrived
//
// ONE FIX FROM HAL WAS CARRIED FORWARD, since testenv's version lacked it:
// RunCodeFile now blocks starting a program entirely (and keeps the fault
// alarm visible) if the drive already has an active fault, rather than
// unconditionally clearing the alarm banner to "No Alarms" the moment Run
// is pressed. See the comment on that check below for the full incident
// history (Err80.4 alarm-hiding bug).
//
// Initialize()'s fault-preserving check ("only send No Alarms if no active
// fault") was already present identically in testenv's version — no merge
// needed there.

import (
	channels "EtherCAT/channels"
	h "EtherCAT/commands"
	parsers "EtherCAT/configparser"
	dt "EtherCAT/datatypes"
	"EtherCAT/licensechecker"
	logger "EtherCAT/logger"
	motor "EtherCAT/motordriver"
	"EtherCAT/settings"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var funcMap map[string]h.Handler
var yamlConfig dt.YamlConfig
var hasYmlLoaded bool
var execContext dt.ExecutionContext
var RS232Enabled atomic.Bool

// runMu ensures only one RunCodeFile goroutine is active at any time.
var runMu sync.Mutex

var lastLineFile = "/mnt/app/jamun/settings/last_line.txt"

// lastProgramFile stores the name of the last-running program so
// resolveStartLine can detect when the operator switches programs.
var lastProgramFile = "/mnt/app/jamun/settings/last_program.txt"

// userLineFile is written by the UI when the operator explicitly selects
// a start line. One-shot: consumed once by resolveStartLine then cleared.
var userLineFile = "/mnt/app/jamun/settings/userline.json"

func SetRS232Enabled(enable bool) {
	RS232Enabled.Store(enable)
}

func IsRS232Enabled() bool {
	return RS232Enabled.Load()
}

// ---------------------------------------------------------------------------
// Initialize
// ---------------------------------------------------------------------------

// Initialize executors to load plugins and parse execution yaml file.
func Initialize() error {
	var err error
	funcMap, err = loadCommandPlugins()
	if err != nil {
		return err
	}
	_, err = loadYmlConfig()
	if err != nil {
		channels.SendAlarm(err.Error())
		return err
	}

	execContext.CommandMaps = yamlConfig.Execution
	execContext.ExecutionMode = "continuous"
	listenCommandExecInput(&execContext)

	// Only send "No Alarms" if no drive fault has been detected yet.
	//
	// ROOT CAUSE OF ALARM BEING INVISIBLE ON GUI:
	//
	// InitMaster() (called before Initialize()) starts the PDO cyclic task and
	// the error poller. If the drive has a stale fault from a previous session
	// (e.g. Err80.4 "ESM unauthorized request error protection"), the poller
	// fires statusnotifier.Alarm("ESM unauthorized request error protection")
	// within ~400ms of PDO activation.
	//
	// Initialize() runs ~800ms after PDO activation — AFTER the fault alarm has
	// already been sent to the UI. The unconditional SendAlarm("No Alarms") here
	// overwrites the fault alarm on every connected client, making the user think
	// everything is fine when the drive is actually faulted.
	//
	// Fix: only send "No Alarms" if the current alarm state is still "No Alarms"
	// (i.e. no fault was detected during InitMaster). If a fault alarm is active,
	// preserve it so the user sees it when they open the web page.
	if motor.GetCurrentAlarm() == "No Alarms" {
		channels.SendAlarm("No Alarms")
	} else {
		logger.Info("[EXECUTOR] Skipping 'No Alarms' — active alarm:", motor.GetCurrentAlarm())
	}

	execContext.Reset()
	return nil
}

func loadYmlConfig() (dt.YamlConfig, error) {
	if hasYmlLoaded == false {
		var err error
		yamlConfig, err = parsers.ParseExececutionConfigYML()
		if err != nil {
			return yamlConfig, err
		}
		hasYmlLoaded = true
	}
	return yamlConfig, nil
}

func validateLicenseBeforeRun() error {
	licErr := licensechecker.CheckLicense(false)
	if licErr != nil {
		channels.SendAlarm(licErr.Error())
		return licErr
	}
	return nil
}

func setDriveSettings() {
	allSettings := settings.GetAllSettings()
	execdriveSettings := make(map[string]dt.DriveSetting)
	for k, v := range allSettings {
		setting := dt.DriveSetting{POTLimit: v.POT, NOTLimit: v.NOT, ConfiguredWorkOffset: v.GetWorkOffset()}
		execdriveSettings[k] = setting
	}
	execContext.DriveSettings = execdriveSettings
}

func CompileProgram(fileName string) error {
	commands, err := createCommands(fileName)
	if err != nil {
		return err
	}
	return canExecuteGivenCommands(commands)
}

// ---------------------------------------------------------------------------
// Persistence helpers
// ---------------------------------------------------------------------------

func clearLastLine() {
	_ = writeFileAtomic(lastLineFile, []byte("0"))
	_ = writeFileAtomic(lastProgramFile, []byte(""))
}

// ClearExecutionResumeState clears persisted automatic resume state.
// Use this when a program file is edited/replaced; the old saved line number
// no longer describes the new file contents.
func ClearExecutionResumeState() {
	clearLastLine()
	clearUserLine()
}

func saveLastLine(lineNumber int) {
	if err := writeFileAtomic(lastLineFile, []byte(strconv.Itoa(lineNumber))); err != nil {
		logger.Error("Failed to save execution state to last_line.txt:", err)
	}
}

func saveLastProgram(fileName string) {
	_ = writeFileAtomic(lastProgramFile, []byte(filepath.Base(fileName)))
}

func readLastProgram() string {
	content, err := os.ReadFile(lastProgramFile)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(content))
}

func readLastLine() (int, error) {
	content, err := os.ReadFile(lastLineFile)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	lineStr := strings.TrimSpace(string(content))
	if lineStr == "" {
		return 0, nil
	}
	return strconv.Atoi(lineStr)
}

// readUserLine reads the one-shot user-selected line from userline.json.
// Format: {"user_line": "5"}  (1-based, as sent by the UI).
// Returns 0 if absent, empty, or "0".
func readUserLine() int {
	type userLineJSON struct {
		UserLine string `json:"user_line"`
	}
	data, err := os.ReadFile(userLineFile)
	if err != nil {
		return 0
	}
	var ul userLineJSON
	if err := json.Unmarshal(data, &ul); err != nil {
		logger.Error("readUserLine: failed to parse userline.json:", err)
		return 0
	}
	raw := strings.TrimSpace(ul.UserLine)
	if raw == "" || raw == "0" {
		return 0
	}
	line, err := strconv.Atoi(raw)
	if err != nil {
		logger.Error("readUserLine: non-numeric user_line value:", raw)
		return 0
	}
	return line
}

// clearUserLine resets userline.json so the one-shot selection is not
// replayed on the next Run press.
func clearUserLine() {
	_ = writeFileAtomic(userLineFile, []byte(`{"user_line":"0"}`))
}

// ---------------------------------------------------------------------------
// resolveStartLine
// ---------------------------------------------------------------------------

// resolveStartLine decides the execution start index (0-based).
//
// Priority:
//  1. Explicit user-selected line from UI (one-shot, 1-based from UI)
//  2. Last stopped line — ONLY for the same program file
//  3. Line 0 (fresh start / after reset)
//
// RS232 mode always returns 0.
func resolveStartLine(currentFile string) int {
	currentName := filepath.Base(currentFile)

	// RS232: always fresh start. The FILES programme is rebuilt on every
	// RS232 command — resuming mid-programme causes setup lines to replay.
	if IsRS232Enabled() {
		clearLastLine()
		logger.Info("[RS232] Always starting from line 0")
		return 0
	}

	// Priority 1: explicit UI line selection (one-shot).
	// Read and clear userline.json here — UpdateLastLineFromJSON must NOT
	// clear it, otherwise resolveStartLine always finds an empty file and
	// falls through to line 0 (the user-line bug).
	userLine := readUserLine()
	if userLine > 0 {
		startIdx := userLine - 1
		if startIdx < 0 {
			startIdx = 0
		}
		logger.Info("Starting from user-selected line:", userLine, "-> internal index:", startIdx)
		clearUserLine()
		return startIdx
	}

	// Priority 2: resume — only valid for the same program.
	lastProgram := readLastProgram()
	if lastProgram != currentName {
		if lastProgram != "" {
			logger.Info("Program changed from", lastProgram, "to", currentName,
				"— starting from line 0")
		}
		clearLastLine()
		return 0
	}

	lastLine, err := readLastLine()
	if err == nil && lastLine > 0 {
		logger.Info("Resuming program", currentName, "from internal index:", lastLine)
		return lastLine
	}

	return 0
}

// ---------------------------------------------------------------------------
// replaySetupCommands
// ---------------------------------------------------------------------------

// replaySetupCommands silently re-executes all non-motion setup commands
// from lines 0..startLine-1 before resuming at startLine.
//
// When a program is stopped mid-way and the operator jogs or zero-refs
// before pressing Execute again, the drive's runtime state (feedrate,
// ABS/INC mode, shortest path) is reset to defaults. Resuming at line N
// without replaying lines 0..N-1 means the motor runs at the wrong speed
// and possibly in the wrong coordinate mode.
func replaySetupCommands(commands []dt.Command, startLine int) {
	if startLine <= 0 || len(commands) == 0 {
		return
	}
	logger.Info("[RESUME] Replaying setup commands [0,", startLine-1,
		"] to restore drive context before line", startLine)

	for i := 0; i < startLine && i < len(commands); i++ {
		cmd := commands[i]
		commandToExec := yamlConfig.Execution.GetCommand(cmd.Cmd)

		// Skip motion commands — must not physically move the table.
		if commandToExec.ConsiderInBlockExecution == 1 {
			logger.Debug("[RESUME] Skipping motion command at line", i, ":", cmd.Cmd)
			continue
		}
		// Skip programme flow markers.
		if cmd.Cmd == "M99" || cmd.Cmd == "M30" {
			logger.Debug("[RESUME] Skipping programme-flow command at line", i, ":", cmd.Cmd)
			continue
		}

		funcToExec := funcMap[commandToExec.Func]
		if funcToExec == nil {
			continue
		}
		if reflect.ValueOf(funcToExec).Kind() == reflect.Ptr &&
			reflect.ValueOf(funcToExec).IsNil() {
			continue
		}

		cmd.ConsiderInBlockExecution = commandToExec.ConsiderInBlockExecution
		logger.Debug("[RESUME] Replaying setup line", i, ":", cmd.Cmd)
		funcToExec.Handle(cmd, &execContext)

		if execContext.Err != nil {
			logger.Error("[RESUME] Setup command at line", i, "failed:", execContext.Err,
				"— aborting replay")
			execContext.Err = nil
			return
		}
		// Allow async state changes (mode, feedrate) to propagate via channels.
		time.Sleep(5 * time.Millisecond)
	}
	logger.Info("[RESUME] Drive context restored, resuming at line", startLine)
}

// ---------------------------------------------------------------------------
// RunCodeFile
// ---------------------------------------------------------------------------

func RunCodeFile(fileName string) error {
	file := filepath.Base(fileName)
	logger.Info("executing program file", file)

	if !runMu.TryLock() {
		logger.Warn("Duplicate Run command rejected: execution is already running or stopping.")
		return errors.New("a program is already executing. Please stop it first")
	}
	defer runMu.Unlock()

	execContext.StopExecution = false

	// Only clear the alarm banner if there is no active drive fault.
	//
	// CARRIED FORWARD FROM HAL'S PREVIOUS RESUME DESIGN — testenv's original
	// version of this function did not have this check.
	//
	// SAME ROOT CAUSE AS Initialize():
	//   If the drive has a fault (e.g. Error 80, POT/NOT exceeded), the UI
	//   alarm banner is showing that fault. Unconditionally sending "No Alarms"
	//   here wipes the warning the moment the user presses Run — hiding the
	//   fault and letting the program attempt to start against a faulted drive.
	//
	//   Fix: if a fault is active, block the run and keep the alarm visible.
	//   The operator must reset the fault before running a program.
	if motor.GetCurrentAlarm() != "No Alarms" {
		errMsg := fmt.Errorf("cannot run program: drive is faulted — %s", motor.GetCurrentAlarm())
		logger.Warn("[EXECUTOR]", errMsg)
		channels.SendAlarm(motor.GetCurrentAlarm()) // keep fault visible on UI
		return errMsg
	}

	channels.SendAlarm("No Alarms")
	if licErr := validateLicenseBeforeRun(); licErr != nil {
		return licErr
	}

	commands, err := createCommands(fileName)
	if err != nil {
		return err
	}

	setDriveSettings()
	drvSettings := settings.GetDriverSettings("A")

	// Resolve start line BEFORE Reset()/canExecuteGivenCommands() wipe disk state.
	startLine := resolveStartLine(fileName)

	execContext.Reset()
	execContext.PrepareExecutingFile(fileName)
	execContext.ExecutingFilePath = fileName
	execContext.Commands = commands
	execContext.ECSEnabled = drvSettings.ECS
	execContext.StopExecution = false
	panicAfter = 0

	channels.NotifyMotorDriver(channels.START_EXECUTION, "", "", 0)

	// Trial (dry-run) validation.
	// canExecuteGivenCommands calls Reset() internally; restore all runtime
	// fields immediately after.
	errVerify := canExecuteGivenCommands(commands)
	if errVerify != nil {
		channels.SendAlarm(errVerify.Error())
		return errVerify
	}

	// Restore fields wiped by canExecuteGivenCommands/Reset().
	execContext.ExecutingFilePath = fileName
	execContext.Commands = commands
	execContext.ECSEnabled = drvSettings.ECS
	execContext.StopExecution = false
	execContext.NextLineWhenStopped = startLine
	execContext.NextCmdLineToExec = startLine

	// Record which programme is running so resume logic can detect a switch.
	saveLastProgram(fileName)

	// If resuming mid-programme, replay setup commands so feedrate/mode/
	// shortest-path are identical to what they would have been from line 0.
	if startLine > 0 {
		replaySetupCommands(commands, startLine)
	}

	logger.Trace("executing commands from", execContext.NextLineWhenStopped)
	execErr := executeCommands(commands, execContext.NextLineWhenStopped)

	if !execContext.StopExecution {
		channels.NotifyMotorDriver(channels.PROGRAM_EXEC_COMPLETED, "", "A", 0)
		ResetExecutingProgram()
	}
	channels.NotifyUIProgramCompleted()
	return execErr
}

func ResetExecutingProgram() {
	execContext.StopExecution = true
	execContext.Reset()
	clearLastLine()
	clearUserLine()
	logger.Info("PROGRAM RESET IS INITIATED.")
}

func canExecuteGivenCommands(commands []dt.Command) error {
	execContext.Reset()
	execContext.ActivateTrialMode()
	logger.Info("running in trial mode to verify the commands")
	execVerifyErr := executeCommands(commands, 0)
	execContext.DeActivateTrialMode()
	execContext.Reset()
	if execVerifyErr != nil {
		return execVerifyErr
	}
	logger.Info("trial run completed, commands ok.")
	return nil
}

// ---------------------------------------------------------------------------
// executeCommands — recursive execution engine
// ---------------------------------------------------------------------------

func executeCommands(commands []dt.Command, nextCommandIndex int) error {
	if execContext.StopExecution {
		logger.Info("Execution stopped. Unwinding command calls...")
		return nil
	}

	if nextCommandIndex < 0 || nextCommandIndex >= len(commands) {
		logger.Trace("command execution completed", nextCommandIndex)
		return nil
	}

	if !execContext.TrialModeActive {
		saveLastLine(nextCommandIndex)
	}

	// Capture BEFORE the command runs so we can detect explicit jump targets
	// set by M99/loop handlers vs a stale unchanged value.
	// This is the double-execution fix: if NextCmdLineToExec was not updated
	// by a handler we advance sequentially, never re-running the same line.
	previousNextIdx := execContext.NextCmdLineToExec

	waitForNextBlock, err := executeCommand(commands[nextCommandIndex], &execContext, nextCommandIndex)
	if err != nil {
		return err
	}

	// RS232: reload file after every command.
	if IsRS232Enabled() && execContext.ExecutingFilePath != "" {
		updatedCommands, reloadErr := createCommands(execContext.ExecutingFilePath)
		if reloadErr == nil {
			commands = updatedCommands
			execContext.Commands = updatedCommands
			logger.Debug("Program file reloaded, total commands:", len(commands))
		} else {
			logger.Warn("Could not reload updated program file:", reloadErr)
		}
	}

	// blockWaitCompleted tracks whether the motion command's ECS/position
	// wait finished SUCCESSFULLY before stop was requested.
	//
	// true  → command fully executed → if stop arrives now, resume at NEXT line
	// false → command was interrupted mid-wait → resume at SAME line (re-run it)
	blockWaitCompleted := false

	if waitForNextBlock && !execContext.TrialModeActive {
		if IsRS232Enabled() {
			nextIdx := nextCommandIndex + 1
			execContext.NextLineWhenStopped = nextIdx
			saveLastLine(nextIdx)
			logger.Info("[RS232] Blocking — waiting for external program file update...")
			waitForProgramFileUpdate(execContext.ExecutingFilePath)

			if execContext.StopExecution {
				if !execContext.TrialModeActive {
					saveLastLine(execContext.NextLineWhenStopped)
				}
				return nil
			}

			blockWaitCompleted = true

			updatedCommands, reloadErr := createCommands(execContext.ExecutingFilePath)
			if reloadErr == nil {
				commands = updatedCommands
				execContext.Commands = updatedCommands
				execContext.NextCmdLineToExec = nextCommandIndex + 1
				logger.Debug("Program file reloaded after RS232 update")
			}
		} else {
			logger.Debug("[EXECUTOR] Standard block-wait (non-RS232)")
			motor.RefreshCurrentPosition()

			// Set resume to THIS command before waiting so that a stop
			// arriving during the wait saves the correct (current) line.
			execContext.NextLineWhenStopped = nextCommandIndex
			execContext.WaitExecuteNextCommand()

			// If StopExecution is still false, the wait completed normally.
			if !execContext.StopExecution {
				blockWaitCompleted = true
				logger.Debug("[EXECUTOR] Block-wait completed normally for line", nextCommandIndex)
			} else {
				logger.Debug("[EXECUTOR] Block-wait interrupted by stop for line", nextCommandIndex)
			}
		}
	}

	if execContext.StopExecution {
		if execContext.HasResetted {
			// Reset pressed — always restart from line 0.
			execContext.NextLineWhenStopped = 0
			logger.Debug("Reset — next start will be line 0")

		} else if blockWaitCompleted {
			// Motion command FINISHED before stop was pressed.
			// Resume at NEXT line so the completed move is not repeated.
			execContext.NextLineWhenStopped = nextCommandIndex + 1
			logger.Debug("Stop after completed line", nextCommandIndex,
				"— resuming at next line", nextCommandIndex+1)

		} else {
			// Stop arrived DURING execution (motion interrupted or ECS wait).
			// Resume at SAME line so the move is re-executed fully.
			execContext.NextLineWhenStopped = nextCommandIndex
			logger.Debug("Stop during line", nextCommandIndex,
				"— will re-execute same line on resume")
		}

		if !execContext.TrialModeActive {
			saveLastLine(execContext.NextLineWhenStopped)
		}
		return nil
	}

	// Determine next index.
	// If a command (M99, loop end) explicitly set NextCmdLineToExec,
	// honour it as a jump target. Otherwise advance sequentially.
	nextIdx := execContext.NextCmdLineToExec
	if nextIdx == previousNextIdx || nextIdx == nextCommandIndex {
		nextIdx = nextCommandIndex + 1
		execContext.NextCmdLineToExec = nextIdx
	}

	return executeCommands(commands, nextIdx)
}

func executeCommand(cmd dt.Command, execContext *dt.ExecutionContext, currentCmdIndex int) (bool, error) {
	commandToExec := yamlConfig.Execution.GetCommand(cmd.Cmd)
	cmd.Description = commandToExec.Description
	execContext.CurrentExecCommandLine = currentCmdIndex
	funcToExec := funcMap[commandToExec.Func]
	err := isAValidHandler(funcToExec, cmd.Cmd)
	if err != nil {
		return false, err
	}
	if funcToExec != nil {
		if !execContext.TrialModeActive {
			logger.Info(fmt.Sprintf(
				"executing line:%-3d  cmd:%-14s  mode:%-4s  workOffset:%s",
				cmd.CodeLineNumber, cmd.Cmd, execContext.RunMode, execContext.CurrentWorkOffSet,
			))
			channels.SendLineNumber(cmd.CodeLineNumber)
		}
		cmd.ConsiderInBlockExecution = commandToExec.ConsiderInBlockExecution
		results := funcToExec.Handle(cmd, execContext)
		if execContext.Err != nil {
			logger.Error("Error executing command "+cmd.Cmd, execContext.Err)
			return false, execContext.Err
		}
		execLineErr := executeInLineParamFunction(funcToExec, cmd, execContext, results)
		if execLineErr != nil {
			return false, execLineErr
		}
	}
	return commandToExec.ConsiderInBlockExecution == 1, nil
}

func isAValidHandler(funcToExec h.Handler, funcName string) error {
	if funcName == "invalidCommand" {
		return errors.New("Unable to find command processor for " + funcName)
	}
	if funcToExec == nil || (reflect.ValueOf(funcToExec).Kind() == reflect.Ptr &&
		reflect.ValueOf(funcToExec).IsNil()) {
		return errors.New("Unable to find command processor for " + funcName)
	}
	return nil
}

var panicAfter int

func executeInLineParamFunction(funcToExec h.Handler, cmd dt.Command, execContext *dt.ExecutionContext, returnedResults []dt.ExecutionResult) error {
	if len(returnedResults) <= 0 {
		return nil
	}
	for _, result := range returnedResults {
		if result.ShouldExecute {
			paramToExec := funcMap[result.Cmd.Func]
			if paramToExec != nil {
				childResults := paramToExec.Handle(result.Cmd, execContext)
				if execContext.Err != nil {
					logger.Error("Error executing command "+cmd.Cmd, execContext.Err)
					return execContext.Err
				}
				if result.Cmd.ConsiderInBlockExecution == 1 {
					execContext.WaitExecuteNextCommand()
				}
				return executeInLineParamFunction(paramToExec, result.Cmd, execContext, childResults)
			}
		}
	}
	return nil
}

func ResumeExecution() error {
	if execContext.NextLineWhenStopped == 0 {
		return nil
	}
	execContext.StopExecution = false
	logger.Info("resuming execution from line ", execContext.NextLineWhenStopped)
	return executeCommands(execContext.Commands, execContext.NextLineWhenStopped)
}

// ---------------------------------------------------------------------------
// UpdateLastLineFromJSON
// ---------------------------------------------------------------------------

// UpdateLastLineFromJSON is called by the UI socket handler (save_line_number)
// immediately after the UI writes userline.json.
//
// It updates in-memory state and writes last_line.txt as a backup.
//
// IMPORTANT: It does NOT call clearUserLine() here.
// clearUserLine() is called by resolveStartLine() when it reads userline.json
// on the next Execute press. Clearing here would mean resolveStartLine()
// always finds an empty file and falls through to line 0.
func UpdateLastLineFromJSON() {
	userLine := readUserLine()
	if userLine <= 0 {
		logger.Info("UpdateLastLineFromJSON: no valid user-selected line")
		return
	}
	startIdx := userLine - 1
	if startIdx < 0 {
		startIdx = 0
	}
	execContext.NextLineWhenStopped = startIdx
	saveLastLine(startIdx)
	// NOTE: Do NOT clearUserLine() here.
	// resolveStartLine() reads userline.json on Execute and clears it there.
	logger.Info("User-selected start line applied, internal index:", startIdx)
}

// ---------------------------------------------------------------------------
// writeFileAtomic
// ---------------------------------------------------------------------------

func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-lastline-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}

// ---------------------------------------------------------------------------
// waitForProgramFileUpdate
// ---------------------------------------------------------------------------

func waitForProgramFileUpdate(filePath string) {
	const timeout = 60 * time.Second
	info, err := os.Stat(filePath)
	if err != nil {
		logger.Warn("Could not stat file for updates:", err)
		return
	}
	lastMod := info.ModTime()
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		if execContext.StopExecution {
			logger.Info("[RS232] Blocking wait aborted due to StopExecution")
			return
		}
		time.Sleep(200 * time.Millisecond)
		info, err = os.Stat(filePath)
		if err != nil {
			logger.Warn("Could not stat file for updates:", err)
			return
		}
		if info.ModTime().After(lastMod) {
			logger.Info("Detected program file update:", filePath)
			return
		}
	}
	logger.Warn("[RS232] waitForProgramFileUpdate: 60s timeout — stopping execution")
	execContext.StopExecution = true
}
