//go:build e2e

package executors

// End-to-end test: full parse → trial-validate → execute pipeline using
// mock handlers. No .so plugin files, no hardware, no real EtherCAT required.

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"EtherCAT/channels"
	h "EtherCAT/commands"
	dt "EtherCAT/datatypes"
)

type e2eRecordingHandler struct {
	name   string
	calls  *[]string
	mode   string
	motion bool
}

func (h e2eRecordingHandler) CommandName() string { return h.name }

func (h e2eRecordingHandler) Handle(cmd dt.Command, ctx *dt.ExecutionContext) []dt.ExecutionResult {
	if h.calls != nil {
		if h.motion {
			*h.calls = append(*h.calls, cmd.Cmd+":"+ctx.RunMode+":"+ctx.ExtractNumeric(cmd.Cmd))
		} else {
			*h.calls = append(*h.calls, cmd.Cmd)
		}
	}
	if h.mode != "" {
		ctx.RunMode = h.mode
	}
	ctx.MoveNextLine()
	return nil
}

func setupFakeE2E(t *testing.T, cfg dt.YamlConfig, handlers map[string]h.Handler) {
	t.Helper()

	oldFuncMap := funcMap
	oldYamlConfig := yamlConfig
	oldHasYmlLoaded := hasYmlLoaded
	oldExecContext := execContext
	oldRS232 := IsRS232Enabled()
	oldBroadcast := channels.BroadCastUIChannel
	oldLastLineFile := lastLineFile
	oldLastProgramFile := lastProgramFile
	oldUserLineFile := userLineFile

	tmp := t.TempDir()
	lastLineFile = filepath.Join(tmp, "last_line.txt")
	lastProgramFile = filepath.Join(tmp, "last_program.txt")
	userLineFile = filepath.Join(tmp, "userline.json")

	funcMap = handlers
	yamlConfig = cfg
	hasYmlLoaded = true
	execContext = dt.ExecutionContext{
		CommandMaps:   cfg.Execution,
		ExecutionMode: "continuous",
		DriveSettings: map[string]dt.DriveSetting{
			"A": {POTLimit: 100, NOTLimit: -100, ConfiguredWorkOffset: map[string]float64{"G53": 0}},
		},
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
		lastLineFile = oldLastLineFile
		lastProgramFile = oldLastProgramFile
		userLineFile = oldUserLineFile
	})
}

func TestFakeProgramE2E_ParseValidateExecuteWithoutHardware(t *testing.T) {
	var calls []string
	cfg := dt.YamlConfig{Execution: dt.Execution{Command: []dt.Command{
		{Cmd: "G90", Func: "absoluteMode", ConsiderInBlockExecution: 0},
		{Cmd: "G91", Func: "relativeMode", ConsiderInBlockExecution: 0},
		{Cmd: "A**", Func: "moveRotary", ConsiderInBlockExecution: 0},
		{Cmd: "M30", Func: "endProgram", ConsiderInBlockExecution: 0},
		{Cmd: "INVALID", Func: "invalidCommand", ConsiderInBlockExecution: 0},
	}}}
	setupFakeE2E(t, cfg, map[string]h.Handler{
		"absoluteMode": e2eRecordingHandler{name: "absoluteMode", calls: &calls, mode: "ABS"},
		"relativeMode": e2eRecordingHandler{name: "relativeMode", calls: &calls, mode: "REL"},
		"moveRotary":   e2eRecordingHandler{name: "moveRotary", calls: &calls, motion: true},
		"endProgram":   e2eRecordingHandler{name: "endProgram", calls: &calls},
	})

	programPath := filepath.Join(t.TempDir(), "safe_fake_program.nc")
	program := "# setup comment ignored\nG90;\nA90;\nG91;\nA-15;\nM30;\n"
	if err := os.WriteFile(programPath, []byte(program), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	commands, err := ParserCommand(programPath)
	if err != nil {
		t.Fatalf("ParserCommand returned error: %v", err)
	}
	if len(commands) != 5 {
		t.Fatalf("parsed command count = %d, want 5", len(commands))
	}

	if err := canExecuteGivenCommands(commands); err != nil {
		t.Fatalf("trial validation failed: %v", err)
	}

	calls = nil
	execContext.DeActivateTrialMode()
	execContext.Reset()
	if err := executeCommands(commands, 0); err != nil {
		t.Fatalf("executeCommands returned error: %v", err)
	}

	want := []string{"G90", "A90:ABS:90", "G91", "A-15:REL:-15", "M30"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("fake e2e calls = %#v, want %#v", calls, want)
	}
}
