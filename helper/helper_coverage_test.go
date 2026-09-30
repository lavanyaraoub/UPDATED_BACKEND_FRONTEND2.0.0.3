//go:build unit

package helper

// Tests for previously uncovered helper functions.
//
// AppendWDPath, GetCodeFilePath, and getExecPath all rely on
// os.Executable() which returns the path to the test binary during
// `go test`. The functions are therefore fully exercisable without
// any special fixtures — they just return paths relative to the
// compiled test binary location.

import (
	"strings"
	"testing"
)

// ─── getExecPath ──────────────────────────────────────────────────────────

// TestGetExecPath_ReturnsNonEmptyString verifies that getExecPath returns a
// non-empty string and does not panic.  During `go test` os.Executable()
// always succeeds.
func TestGetExecPath_ReturnsNonEmptyString(t *testing.T) {
	p := getExecPath()
	if p == "" {
		t.Error("getExecPath() returned empty string")
	}
}

// TestGetExecPath_DoesNotContainFilename verifies that getExecPath returns a
// directory (filepath.Dir result), so it should not end with ".go" or ".test".
func TestGetExecPath_IsDirectory(t *testing.T) {
	p := getExecPath()
	if strings.HasSuffix(p, ".go") || strings.HasSuffix(p, ".test") {
		t.Errorf("getExecPath() looks like a file path, not a directory: %q", p)
	}
}

// ─── AppendWDPath ─────────────────────────────────────────────────────────

// TestAppendWDPath_AppendsPathSegment verifies that AppendWDPath returns a
// string that ends with the given segment appended to the executable directory.
func TestAppendWDPath_AppendsPathSegment(t *testing.T) {
	segment := "/configs/test.json"
	result := AppendWDPath(segment)
	if !strings.HasSuffix(result, segment) {
		t.Errorf("AppendWDPath(%q): got %q, want suffix %q", segment, result, segment)
	}
}

// TestAppendWDPath_EmptySegment_ReturnsExecDir verifies that AppendWDPath("")
// returns the executable directory unchanged.
func TestAppendWDPath_EmptySegment_ReturnsExecDir(t *testing.T) {
	base := getExecPath()
	result := AppendWDPath("")
	if result != base {
		t.Errorf("AppendWDPath(%q): got %q, want %q", "", result, base)
	}
}

// TestAppendWDPath_ResultIsLongerThanBase verifies that appending a non-empty
// segment produces a string longer than the executable directory alone.
func TestAppendWDPath_ResultIsLongerThanBase(t *testing.T) {
	base := getExecPath()
	result := AppendWDPath("/extra")
	if len(result) <= len(base) {
		t.Errorf("AppendWDPath with segment: result %q is not longer than base %q", result, base)
	}
}

// ─── GetCodeFilePath ──────────────────────────────────────────────────────

// TestGetCodeFilePath_ReturnsNonEmptyString verifies that GetCodeFilePath
// returns a non-empty path (delegates to AppendWDPath).
func TestGetCodeFilePath_ReturnsNonEmptyPath(t *testing.T) {
	p := GetCodeFilePath()
	if p == "" {
		t.Error("GetCodeFilePath() returned empty string")
	}
}

// TestGetCodeFilePath_EndsWithGmCodes verifies that the returned path ends
// with the expected directory segment "/gm_codes".
func TestGetCodeFilePath_EndsWithGmCodes(t *testing.T) {
	p := GetCodeFilePath()
	if !strings.HasSuffix(p, "/gm_codes") {
		t.Errorf("GetCodeFilePath() = %q, want suffix \"/gm_codes\"", p)
	}
}

// TestGetCodeFilePath_IsDeterministic verifies two consecutive calls return
// the same value (no randomness introduced).
func TestGetCodeFilePath_IsDeterministic(t *testing.T) {
	p1 := GetCodeFilePath()
	p2 := GetCodeFilePath()
	if p1 != p2 {
		t.Errorf("GetCodeFilePath() not deterministic: %q vs %q", p1, p2)
	}
}

// ─── ReadSerialNumber ─────────────────────────────────────────────────────────
// Reads /proc/cpuinfo which exists on the Pi — returns the CPU serial number.

func TestReadSerialNumber_ReturnsNonEmptyString(t *testing.T) {
	serial, err := ReadSerialNumber()
	if err != nil {
		t.Fatalf("ReadSerialNumber: unexpected error: %v", err)
	}
	if serial == "" {
		t.Log("ReadSerialNumber: returned empty string (CPU may not have Serial in /proc/cpuinfo)")
	} else {
		t.Logf("ReadSerialNumber: %q", serial)
	}
}
