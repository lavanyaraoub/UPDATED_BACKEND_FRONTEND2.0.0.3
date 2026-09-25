//go:build unit

package executors

// Unit tests for the command parsing layer in program_reader.go and the
// RS232 enable flag in command_executor.go.
//
// CURATION NOTE: this is a trimmed subset of testenv's original
// program_reader_unit_test.go. Only tests for functions that are byte-
// identical between HAL and testenv (extractCommand, createCommands,
// SetRS232Enabled/IsRS232Enabled) were kept. Tests for resume-logic
// functions (writeFileAtomic, resolveStartLine, readUserLine, etc.) that
// don't exist in HAL's command_executor.go were intentionally dropped —
// see TESTING_TODO.md at the repo root.

import (
	"os"
	"testing"
)

// ─── extractCommand ────────────────────────────────────────────────────────
//
// Contract:
//   - Returns everything before the first semicolon, trimmed as-is.
//   - If no semicolon is present, returns ("", error("Syntax error")).
//   - Content after the semicolon is treated as comment and dropped.

func TestExtractCommand_SemicolonAtEnd(t *testing.T) {
	got, err := extractCommand("G90;")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "G90" {
		t.Errorf("got %q, want %q", got, "G90")
	}
}

func TestExtractCommand_SemicolonWithInlineComment(t *testing.T) {
	got, err := extractCommand("A90; move to 90 degrees")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "A90" {
		t.Errorf("got %q, want %q", got, "A90")
	}
}

func TestExtractCommand_NoSemicolonReturnsSyntaxError(t *testing.T) {
	_, err := extractCommand("G90")
	if err == nil {
		t.Fatalf("expected Syntax error for line without semicolon, got nil")
	}
	if err.Error() != "Syntax error" {
		t.Errorf("error = %q, want %q", err.Error(), "Syntax error")
	}
}

func TestExtractCommand_EmptyBeforeSemicolon(t *testing.T) {
	// A bare semicolon is valid — produces an empty command string.
	// The caller (createCommands) will skip empty strings.
	got, err := extractCommand(";")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty string before semicolon", got)
	}
}

func TestExtractCommand_MultipleSemicolonsUsesFirst(t *testing.T) {
	// Only the FIRST semicolon is the command terminator.
	got, err := extractCommand("A90; comment; extra")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "A90" {
		t.Errorf("got %q, want A90 (only first semicolon is terminator)", got)
	}
}

func TestExtractCommand_NegativeValue(t *testing.T) {
	got, err := extractCommand("A-90;")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "A-90" {
		t.Errorf("got %q, want A-90", got)
	}
}

func TestExtractCommand_FloatValue(t *testing.T) {
	got, err := extractCommand("A90.5; move to 90.5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "A90.5" {
		t.Errorf("got %q, want A90.5", got)
	}
}

func TestExtractCommand_LeadingWhitespacePreserved(t *testing.T) {
	// extractCommand does NOT trim whitespace — the caller is responsible.
	// This test pins the current behavior: whitespace before command is kept.
	got, err := extractCommand("  G90;")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "  G90" {
		t.Errorf("got %q — whitespace handling changed (update test if intentional)", got)
	}
}

func TestExtractCommand_MixedCasePreserved(t *testing.T) {
	// extractCommand does NOT uppercase — createCommands does that later.
	// Verify the function is case-neutral.
	got, err := extractCommand("g90;")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "g90" {
		t.Errorf("got %q, extractCommand should preserve original case", got)
	}
}

// ─── Exhaustive property: no panic on any ASCII printable input ─────────────
//
// The function should never panic regardless of input. Certain inputs
// will produce Syntax error (no semicolon) — that's expected and correct.

func TestExtractCommand_NeverPanics(t *testing.T) {
	inputs := []string{
		"", "G90", "G90;", ";", ";;", "G90;comment", "A-180.5; note",
		"   ", "\t", "G90;\n", "!@#$%;", "A1000000;",
	}
	for _, in := range inputs {
		t.Run(in, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("extractCommand(%q) panicked: %v", in, r)
				}
			}()
			extractCommand(in) //nolint:errcheck — we only check no-panic here
		})
	}
}

// ─── createCommands (via temp file) ────────────────────────────────────────

func TestCreateCommands_ValidProgram(t *testing.T) {
	f := writeTempFile(t, "G90;\nA90; move to 90\nM30;\n")
	cmds, err := createCommands(f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cmds) != 3 {
		t.Fatalf("expected 3 commands, got %d: %v", len(cmds), cmds)
	}
	wantCmds := []string{"G90", "A90", "M30"}
	for i, want := range wantCmds {
		if cmds[i].Cmd != want {
			t.Errorf("cmd[%d] = %q, want %q", i, cmds[i].Cmd, want)
		}
	}
}

func TestCreateCommands_StripsCommentLines(t *testing.T) {
	f := writeTempFile(t, "# this is a comment\nG90;\n// also a comment\nM30;\n")
	cmds, err := createCommands(f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cmds) != 2 {
		t.Fatalf("expected 2 commands (comments stripped), got %d: %v", len(cmds), cmds)
	}
	if cmds[0].Cmd != "G90" || cmds[1].Cmd != "M30" {
		t.Errorf("commands = %v, want [G90 M30]", cmds)
	}
}

func TestCreateCommands_SkipsEmptyLines(t *testing.T) {
	f := writeTempFile(t, "\n\nG90;\n\nM30;\n\n")
	cmds, err := createCommands(f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cmds) != 2 {
		t.Fatalf("expected 2 commands (empty lines skipped), got %d", len(cmds))
	}
}

func TestCreateCommands_NormalisesToUpperCase(t *testing.T) {
	f := writeTempFile(t, "g90;\na90;\nm30;\n")
	cmds, err := createCommands(f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, cmd := range cmds {
		for _, ch := range cmd.Cmd {
			if ch >= 'a' && ch <= 'z' {
				t.Errorf("cmd %q contains lowercase — createCommands must uppercase", cmd.Cmd)
			}
		}
	}
}

func TestCreateCommands_PreservesSourceLineNumbers(t *testing.T) {
	f := writeTempFile(t, "# comment\nG90;\nA45;\n")
	cmds, err := createCommands(f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cmds) != 2 {
		t.Fatalf("expected 2 commands, got %d", len(cmds))
	}
	if cmds[0].CodeLineNumber != 1 {
		t.Errorf("G90 CodeLineNumber = %d, want 1", cmds[0].CodeLineNumber)
	}
	if cmds[1].CodeLineNumber != 2 {
		t.Errorf("A45 CodeLineNumber = %d, want 2", cmds[1].CodeLineNumber)
	}
}

func TestCreateCommands_SyntaxErrorStopsProcessing(t *testing.T) {
	f := writeTempFile(t, "G90;\nG91\nM30;\n")
	_, err := createCommands(f)
	if err == nil {
		t.Fatalf("expected Syntax error for line without semicolon, got nil")
	}
	if err.Error() != "Syntax error" {
		t.Errorf("error = %q, want \"Syntax error\"", err.Error())
	}
}

func TestCreateCommands_EmptyFileReturnsEmptySlice(t *testing.T) {
	f := writeTempFile(t, "")
	cmds, err := createCommands(f)
	if err != nil {
		t.Fatalf("unexpected error on empty file: %v", err)
	}
	if len(cmds) != 0 {
		t.Errorf("expected 0 commands from empty file, got %d", len(cmds))
	}
}

func TestCreateCommands_CommentsOnlyReturnsEmptySlice(t *testing.T) {
	f := writeTempFile(t, "# first comment\n# second comment\n// third\n")
	cmds, err := createCommands(f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cmds) != 0 {
		t.Errorf("expected 0 commands from comment-only file, got %d", len(cmds))
	}
}

func TestCreateCommands_MissingFileReturnsError(t *testing.T) {
	_, err := createCommands("/nonexistent/path/program.nc")
	if err == nil {
		t.Fatalf("expected error for missing file, got nil")
	}
}

// ─── RS232 flag ───────────────────────────────────────────────────────────

func TestSetRS232Enabled_TrueIsReflected(t *testing.T) {
	t.Cleanup(func() { SetRS232Enabled(false) })
	SetRS232Enabled(true)
	if !IsRS232Enabled() {
		t.Errorf("IsRS232Enabled() = false after SetRS232Enabled(true)")
	}
}

func TestSetRS232Enabled_FalseIsReflected(t *testing.T) {
	SetRS232Enabled(true)
	SetRS232Enabled(false)
	if IsRS232Enabled() {
		t.Errorf("IsRS232Enabled() = true after SetRS232Enabled(false)")
	}
}

func writeTempFile(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp("", "unit_test_*.gcode")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })
	return f.Name()
}
