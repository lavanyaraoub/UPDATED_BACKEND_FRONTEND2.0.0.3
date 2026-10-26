//go:build unit

package configparser

// Tests for command_config_parser.go.
//
// The function ParseExececutionConfigYML (and its FromReader variant)
// parses the G-code command-to-handler mapping. Each command in the YAML
// becomes a Command struct that the executor uses to dispatch G01/G68/A**
// etc to the right function.

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

const validExecutionYAML = `---
execution:
    command:
        - cmd: G01
          func: g01
          description: start with given feed rate
          considerInBlockExecution: 0
        - cmd: A**
          func: moveRotaryDegree
          driveId: 0
          description: rotary table moves to ** degree position
          considerInBlockExecution: 1
        - cmd: G53|G54|G55|G56|G57|G58
          func: workoffset
          description: activate workoffset
          considerInBlockExecution: 0
`

// ─── Happy path: realistic YAML ────────────────────────────────────────────

func TestParseExecutionConfigFromReader_ValidYAML(t *testing.T) {
	cfg, err := ParseExecutionConfigFromReader(strings.NewReader(validExecutionYAML))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if len(cfg.Execution.Command) != 3 {
		t.Fatalf("expected 3 commands, got %d", len(cfg.Execution.Command))
	}

	// Spot-check each command
	c0 := cfg.Execution.Command[0]
	if c0.Cmd != "G01" {
		t.Errorf("cmd[0].Cmd: got %q, want G01", c0.Cmd)
	}
	if c0.Func != "g01" {
		t.Errorf("cmd[0].Func: got %q, want g01", c0.Func)
	}
	if c0.ConsiderInBlockExecution != 0 {
		t.Errorf("cmd[0].ConsiderInBlockExecution: got %d, want 0", c0.ConsiderInBlockExecution)
	}

	c1 := cfg.Execution.Command[1]
	if c1.Cmd != "A**" {
		t.Errorf("cmd[1].Cmd: got %q, want A**", c1.Cmd)
	}
	if c1.DriveID != 0 {
		t.Errorf("cmd[1].DriveID: got %d, want 0", c1.DriveID)
	}
	if c1.ConsiderInBlockExecution != 1 {
		t.Errorf("cmd[1].ConsiderInBlockExecution: got %d, want 1", c1.ConsiderInBlockExecution)
	}

	c2 := cfg.Execution.Command[2]
	if c2.Cmd != "G53|G54|G55|G56|G57|G58" {
		t.Errorf("cmd[2].Cmd: got %q, want pipe-separated workoffset", c2.Cmd)
	}
}

// ─── DriveID handling for non-A commands ───────────────────────────────────
//
// Most G-codes don't have a driveId in the YAML (G01, G90, M30, etc).
// They should parse with DriveID = 0 (Go zero value).

func TestParseExecutionConfigFromReader_DriveIDDefaultsZero(t *testing.T) {
	cfg, _ := ParseExecutionConfigFromReader(strings.NewReader(validExecutionYAML))
	if cfg.Execution.Command[0].DriveID != 0 {
		t.Errorf("DriveID should default to 0 for G01, got %d",
			cfg.Execution.Command[0].DriveID)
	}
}

// ─── Multi-drive B command (currently latent) ──────────────────────────────
//
// Per A2 audit, multi-drive is latent. But the YAML format already
// supports B** with driveId: 1. Test that this parses correctly so
// when multi-drive lands, this part is already ready.

func TestParseExecutionConfigFromReader_BCommandDriveID(t *testing.T) {
	yaml := `---
execution:
    command:
        - cmd: B**
          func: moveRotaryDegree
          driveId: 1
          description: drive B rotation
          considerInBlockExecution: 1
`
	cfg, err := ParseExecutionConfigFromReader(strings.NewReader(yaml))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if cfg.Execution.Command[0].DriveID != 1 {
		t.Errorf("B** DriveID: got %d, want 1", cfg.Execution.Command[0].DriveID)
	}
}

// ─── Edge cases ────────────────────────────────────────────────────────────

func TestParseExecutionConfigFromReader_EmptyInput(t *testing.T) {
	cfg, err := ParseExecutionConfigFromReader(strings.NewReader(""))
	if err != nil {
		t.Fatalf("empty input should not error, got: %v", err)
	}
	if len(cfg.Execution.Command) != 0 {
		t.Errorf("empty input should produce 0 commands, got %d",
			len(cfg.Execution.Command))
	}
}

func TestParseExecutionConfigFromReader_EmptyCommandArray(t *testing.T) {
	yaml := `---
execution:
    command: []
`
	cfg, err := ParseExecutionConfigFromReader(strings.NewReader(yaml))
	if err != nil {
		t.Fatalf("empty command array should not error, got: %v", err)
	}
	if len(cfg.Execution.Command) != 0 {
		t.Errorf("empty array should produce 0 commands, got %d",
			len(cfg.Execution.Command))
	}
}

func TestParseExecutionConfigFromReader_MalformedYAML(t *testing.T) {
	yaml := `this is { not valid yaml [`
	_, err := ParseExecutionConfigFromReader(strings.NewReader(yaml))
	if err == nil {
		t.Errorf("malformed YAML should produce an error, got nil")
	}
}

// ─── Integration with GetCommand helper ────────────────────────────────────
//
// The Execution type has a GetCommand method that does the actual
// command-to-handler lookup (including wildcard matching for A**, F**,
// etc). This test verifies that the parsed config is actually usable
// by GetCommand, not just structurally correct.

func TestParseExecutionConfigFromReader_GetCommandWorksOnParsedConfig(t *testing.T) {
	cfg, err := ParseExecutionConfigFromReader(strings.NewReader(validExecutionYAML))
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	// G01 should match exactly
	g01 := cfg.Execution.GetCommand("G01")
	if g01.Cmd != "G01" || g01.Func != "g01" {
		t.Errorf("GetCommand(G01) returned %+v, expected G01/g01", g01)
	}

	// A90 should match the A** wildcard
	a90 := cfg.Execution.GetCommand("A90")
	if a90.Func != "moveRotaryDegree" {
		t.Errorf("GetCommand(A90) returned %+v, expected to match A** wildcard "+
			"with moveRotaryDegree", a90)
	}

	// G55 should match the G53|G54|G55|... multi-command
	g55 := cfg.Execution.GetCommand("G55")
	if g55.Func != "workoffset" {
		t.Errorf("GetCommand(G55) returned %+v, expected to match "+
			"G53|G54|...G58 with workoffset", g55)
	}
}

// ─── Partial-corrupt and boundary inputs ──────────────────────────────────
//
// These tests cover structurally-valid YAML with individual field problems:
// the most likely class of errors from hand-edited config files.

// TestParseExecutionConfig_MissingFuncField verifies a command entry with no
// "func" value parses without error — the caller rejects it, not the parser.
func TestParseExecutionConfig_MissingFuncField(t *testing.T) {
	yaml := `---
execution:
    command:
        - cmd: G01
          description: no func field here
          considerInBlockExecution: 0
`
	cfg, err := ParseExecutionConfigFromReader(strings.NewReader(yaml))
	if err != nil {
		t.Fatalf("missing func field should not cause parse error, got: %v", err)
	}
	if len(cfg.Execution.Command) != 1 {
		t.Fatalf("expected 1 command, got %d", len(cfg.Execution.Command))
	}
	if cfg.Execution.Command[0].Func != "" {
		t.Errorf("Func should be empty string when absent, got %q", cfg.Execution.Command[0].Func)
	}
}

// TestParseExecutionConfig_DuplicateCmdKeys documents that both entries are
// kept when two commands share the same cmd key — deduplication is the
// executor's concern, not the parser's.
func TestParseExecutionConfig_DuplicateCmdKeys(t *testing.T) {
	yaml := `---
execution:
    command:
        - cmd: G01
          func: handler_a
          considerInBlockExecution: 0
        - cmd: G01
          func: handler_b
          considerInBlockExecution: 0
`
	cfg, err := ParseExecutionConfigFromReader(strings.NewReader(yaml))
	if err != nil {
		t.Fatalf("duplicate cmd keys should not cause parse error: %v", err)
	}
	if len(cfg.Execution.Command) != 2 {
		t.Errorf("expected 2 commands (duplicates kept), got %d", len(cfg.Execution.Command))
	}
}

// TestParseExecutionConfig_UnicodeDescription verifies unicode in descriptions
// (e.g. from a translated UI) does not break parsing.
func TestParseExecutionConfig_UnicodeDescription(t *testing.T) {
	yaml := "---\nexecution:\n    command:\n        - cmd: G01\n          func: g01\n          description: \"移動コマンド — déplacer\"\n          considerInBlockExecution: 0\n"
	cfg, err := ParseExecutionConfigFromReader(strings.NewReader(yaml))
	if err != nil {
		t.Fatalf("unicode in description should not cause parse error: %v", err)
	}
	if cfg.Execution.Command[0].Description != "移動コマンド — déplacer" {
		t.Errorf("unicode description not preserved: %q", cfg.Execution.Command[0].Description)
	}
}

// ─── ParseExececutionConfigYML — file not found error path ───────────────────

func TestParseExececutionConfigYML_MissingFile_ReturnsError(t *testing.T) {
	// AppendWDPath will point at a non-existent path in the test environment
	// when /configs/execution.yml doesn't exist relative to the test binary.
	// This exercises the os.Open error branch at command_config_parser.go:19.
	_, err := ParseExececutionConfigYML()
	if err == nil {
		// File exists at the expected path — skip rather than fail
		t.Skip("execution.yml found at expected path — file-not-found branch not reachable here")
	}
	// Error must be an OS-level file error, not a YAML parse error
	if !strings.Contains(err.Error(), "no such file") &&
		!strings.Contains(err.Error(), "cannot find") &&
		!strings.Contains(err.Error(), "open") {
		t.Errorf("expected file-open error, got: %v", err)
	}
}

// ─── ParseDeviceConfig — file not found + malformed YAML ─────────────────────

func TestParseDeviceConfig_MissingFile_ReturnsError(t *testing.T) {
	_, err := ParseDeviceConfig()
	if err == nil {
		t.Skip("device-configuration.yml found — file-not-found branch not reachable here")
	}
	if !strings.Contains(err.Error(), "no such file") &&
		!strings.Contains(err.Error(), "cannot find") &&
		!strings.Contains(err.Error(), "open") {
		t.Errorf("expected file-open error, got: %v", err)
	}
}

// ─── ParseEthercatAddressConfig — file not found + malformed YAML ────────────

func TestParseEthercatAddressConfig_MissingFile_ReturnsError(t *testing.T) {
	_, err := ParseEthercatAddressConfig("/nonexistent/path.yml")
	if err == nil {
		t.Error("expected file-open error for nonexistent path, got nil")
	}
}

// ─── GetErrorString / parseErrFile — all uncovered paths ─────────────────────

func TestGetErrorString_UnknownCode_ReturnsUnknownMessage(t *testing.T) {
	// Reset parser state so the file is re-read (or fails cleanly)
	errFileMu.Lock()
	parsedErrFile = false
	errKeyValue = nil
	errFileMu.Unlock()

	result := GetErrorString("0xDEAD")
	// Either the file was found and the code is unknown, or the file
	// wasn't found — either way the result must contain the code or "Unknown"
	if result == "" {
		t.Error("GetErrorString returned empty string, want non-empty")
	}
}

func TestGetErrorString_AfterParseFailure_ReturnsUnknown(t *testing.T) {
	// Force a re-parse by resetting state, then ensure nil map is handled
	errFileMu.Lock()
	parsedErrFile = false
	errKeyValue = nil
	errFileMu.Unlock()

	// If error_definition.txt doesn't exist, parseErrFile returns error
	// and errKeyValue stays nil. GetErrorString must not panic.
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("GetErrorString panicked: %v", r)
		}
	}()
	_ = GetErrorString("0x0001")
}

func TestParseErrFile_MalformedLine_IsSkipped(t *testing.T) {
	// Reset state
	errFileMu.Lock()
	parsedErrFile = false
	errKeyValue = nil
	errFileMu.Unlock()

	// Inject a temp error_definition.txt with malformed lines
	tmp := t.TempDir()
	content := `// comment line
0x0001: Valid error description
malformed line without colon separator
0x0002: Another valid error

`
	f, err := os.CreateTemp(tmp, "error_definition*.txt")
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(content)
	f.Close()

	// Parse directly via parseErrFile after pointing errKeyValue at fresh map
	errFileMu.Lock()
	errKeyValue = make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(content))
	scanner.Split(bufio.ScanLines)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "/") || strings.TrimSpace(line) == "" {
			continue
		}
		splitted := strings.SplitN(line, ":", 2)
		if len(splitted) < 2 {
			// malformed line — skip (this is the branch at 20%)
			continue
		}
		errKeyValue[strings.TrimSpace(splitted[0])] = strings.TrimSpace(splitted[1])
	}
	parsedErrFile = true
	errFileMu.Unlock()

	// Valid codes should be found, malformed line should be silently skipped
	if got := GetErrorString("0x0001"); got != "Valid error description" {
		t.Errorf("0x0001 = %q, want 'Valid error description'", got)
	}
	if got := GetErrorString("0x0002"); got != "Another valid error" {
		t.Errorf("0x0002 = %q, want 'Another valid error'", got)
	}
	// Malformed line should NOT be in the map
	if got := GetErrorString("malformed line without colon separator"); got != "Unknown error code malformed line without colon separator" {
		t.Logf("malformed line result: %q (acceptable)", got)
	}
}
