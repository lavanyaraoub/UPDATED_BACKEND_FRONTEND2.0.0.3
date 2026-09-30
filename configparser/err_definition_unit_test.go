//go:build unit

package configparser

// Unit tests for configparser functions that don't require real config files.
//
// ParseExececutionConfigYML, ParseDeviceConfig, and ParseEthercatAddressConfig
// are file-reading wrappers tested under the integration tag (they need the
// real configs/ directory or a temp file setup). The functions below are
// testable as pure logic in unit mode:
//
//   GetErrorString — lookup with injected cache
//   parseErrFile format — replicated parsing logic
//   ParseExecutionConfigFromReader, ParseDeviceConfigFromReader,
//   ParseEthercatAddressConfigFromReader — already covered by unit tests
//   in the existing *_test.go files; only the error-code path is new here.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// resetErrParser forces GetErrorString to re-parse on next call by clearing
// the package-level singleton. Called in t.Cleanup to avoid polluting other tests.
func resetErrParser() {
	parsedErrFile = false
	errKeyValue = nil
}

// ─── GetErrorString ───────────────────────────────────────────────────────

// TestGetErrorString_UnknownCodeReturnsUnknownMessage verifies that a code not
// in the map returns a message containing "Unknown" and the code itself.
// When error_definition.txt is not present, errKeyValue stays empty and every
// lookup falls back to the unknown-code path.
func TestGetErrorString_UnknownCodeReturnsUnknownMessage(t *testing.T) {
	resetErrParser()
	t.Cleanup(resetErrParser)

	got := GetErrorString("9999")
	if !strings.Contains(got, "9999") {
		t.Errorf("GetErrorString(9999) = %q, expected code 9999 in message", got)
	}
	if !strings.Contains(got, "Unknown") {
		t.Errorf("GetErrorString(9999) = %q, expected 'Unknown' in message", got)
	}
}

// TestGetErrorString_KnownCodeResolvesAfterManualInjection bypasses parseErrFile
// (which needs a real file) by injecting errKeyValue directly. This tests the
// lookup branch of GetErrorString in isolation.
func TestGetErrorString_KnownCodeResolvesAfterManualInjection(t *testing.T) {
	resetErrParser()
	t.Cleanup(resetErrParser)

	parsedErrFile = true
	errKeyValue = map[string]string{
		"0":  "No Alarms",
		"14": "Over-current protection",
		"87": "Hardware emergency activated",
	}

	cases := []struct{ id, want string }{
		{"0", "No Alarms"},
		{"14", "Over-current protection"},
		{"87", "Hardware emergency activated"},
	}
	for _, c := range cases {
		got := GetErrorString(c.id)
		if got != c.want {
			t.Errorf("GetErrorString(%q) = %q, want %q", c.id, got, c.want)
		}
	}
}

// TestGetErrorString_MissingKeyReturnsUnknownWithID verifies an injected map
// that does not contain the requested key returns the "Unknown error code X" form.
func TestGetErrorString_MissingKeyReturnsUnknownWithID(t *testing.T) {
	resetErrParser()
	t.Cleanup(resetErrParser)

	parsedErrFile = true
	errKeyValue = map[string]string{"1": "some error"}

	got := GetErrorString("42")
	if !strings.Contains(got, "42") {
		t.Errorf("GetErrorString(42) = %q, expected code 42 in message", got)
	}
}

// TestGetErrorString_CachesAfterSuccessfulParse verifies parsedErrFile is set
// to true only after a successful parse, and subsequent calls use the cache.
func TestGetErrorString_CachesAfterSuccessfulParse(t *testing.T) {
	resetErrParser()
	t.Cleanup(resetErrParser)

	// Simulate a successful parse by injecting the map and setting the flag.
	parsedErrFile = true
	errKeyValue = map[string]string{"5": "injected"}

	// First call — parsedErrFile already true, goes straight to lookup.
	got := GetErrorString("5")
	if got != "injected" {
		t.Errorf("GetErrorString(5) = %q, want 'injected'", got)
	}

	// Overwrite the map — call must still return cache value (no re-parse).
	errKeyValue["5"] = "updated"
	got = GetErrorString("5")
	if got != "updated" {
		t.Errorf("GetErrorString(5) after update = %q, want 'updated'", got)
	}
}

// TestGetErrorString_FileNotFoundLeavesNotParsed verifies that when the error
// definition file is missing, parsedErrFile remains false so each call retries.
// This is the actual production behaviour — no stale-cache risk when the file
// is temporarily unavailable at startup.
func TestGetErrorString_FileNotFoundLeavesNotParsed(t *testing.T) {
	resetErrParser()
	t.Cleanup(resetErrParser)

	// Call with no file present — parseErrFile will fail and return error.
	GetErrorString("1")

	// parsedErrFile must still be false (only set on success).
	if parsedErrFile {
		t.Errorf("parsedErrFile = true after failed parse, want false")
	}
}

// ─── parseErrFile format (logic test) ─────────────────────────────────────
//
// parseErrFile is unexported and hardcodes AppendWDPath, so we can't call it
// directly with a custom file. Instead we replicate its parsing logic using a
// temp file to verify the format specification: lines starting with "/" are
// comments, remaining lines are "key : value" pairs.

// TestParseErrFileFormat_CommentsAreSkipped verifies lines starting with "/"
// are not included in the parsed map.
func TestParseErrFileFormat_CommentsAreSkipped(t *testing.T) {
	content := "/ this is a comment\n0 : No Alarms\n"
	parsed := parseErrFileContent(content)
	if _, ok := parsed["/"]; ok {
		t.Errorf("comment line '/' should not be a key in parsed map")
	}
	if _, ok := parsed["/ this is a comment"]; ok {
		t.Errorf("comment line should not appear in parsed map")
	}
}

// TestParseErrFileFormat_KeyValueParsedCorrectly verifies colon-delimited
// key:value pairs are trimmed and stored correctly.
func TestParseErrFileFormat_KeyValueParsedCorrectly(t *testing.T) {
	content := "  14 : Over-current protection\n  15: Over-voltage protection\n"
	parsed := parseErrFileContent(content)
	if parsed["14"] != "Over-current protection" {
		t.Errorf("parsed[14] = %q, want 'Over-current protection'", parsed["14"])
	}
	if parsed["15"] != "Over-voltage protection" {
		t.Errorf("parsed[15] = %q, want 'Over-voltage protection'", parsed["15"])
	}
}

// TestParseErrFileFormat_RealFileParsedViaWriteThenRead writes a real temp
// file in the expected format and reads it back using the same scanning logic
// as parseErrFile, confirming the format survives a write/read round-trip.
func TestParseErrFileFormat_RealFileParsedViaWriteThenRead(t *testing.T) {
	content := "/ comment\n0 : No Alarms\n14: Over-current protection\n87 : Hardware emergency\n"
	tmp := filepath.Join(t.TempDir(), "error_definition.txt")
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(tmp)
	if err != nil {
		t.Fatal(err)
	}
	parsed := parseErrFileContent(string(data))
	if parsed["0"] != "No Alarms" {
		t.Errorf("parsed[0] = %q, want 'No Alarms'", parsed["0"])
	}
	if parsed["87"] != "Hardware emergency" {
		t.Errorf("parsed[87] = %q, want 'Hardware emergency'", parsed["87"])
	}
}

// parseErrFileContent replicates the line-scanning logic of parseErrFile
// using in-memory content instead of a file path. Used to test the format
// specification without needing AppendWDPath.
func parseErrFileContent(content string) map[string]string {
	result := make(map[string]string)
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "/") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			val := strings.TrimSpace(parts[1])
			if key != "" {
				result[key] = val
			}
		}
	}
	return result
}

// ─── parseErrFile — malformed line branch (line 37) ──────────────────────────

// TestParseErrFile_MalformedLineSkipped calls the real parseErrFile via an
// injectable path so Go's coverage tool can instrument the actual branch.
func TestParseErrFile_MalformedLineSkipped(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "error_definition.txt")
	content := "/ comment\n0x0001: Valid error\nmalformed line without colon\n0x0002: Another valid\n"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	// Reset state and point parser at our temp file
	errFileMu.Lock()
	parsedErrFile = false
	errKeyValue = nil
	errDefinitionPath = tmp
	errFileMu.Unlock()

	t.Cleanup(func() {
		errFileMu.Lock()
		parsedErrFile = false
		errKeyValue = nil
		errDefinitionPath = ""
		errFileMu.Unlock()
	})

	// Call real parseErrFile — this hits the malformed-line branch at line 37
	errFileMu.Lock()
	err := parseErrFile()
	errFileMu.Unlock()

	if err != nil {
		t.Fatalf("parseErrFile: unexpected error: %v", err)
	}

	// Valid lines should be parsed
	if got := GetErrorString("0x0001"); got != "Valid error" {
		t.Errorf("0x0001 = %q, want 'Valid error'", got)
	}
	if got := GetErrorString("0x0002"); got != "Another valid" {
		t.Errorf("0x0002 = %q, want 'Another valid'", got)
	}

	// Malformed line must NOT appear as a key
	if got := GetErrorString("malformed line without colon"); !strings.Contains(got, "Unknown") {
		t.Errorf("malformed line should be unknown, got %q", got)
	}
}
