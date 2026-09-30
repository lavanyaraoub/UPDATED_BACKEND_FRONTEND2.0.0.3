package licensechecker

// Tests for licensechecker's pure functions: HasError, isLicenseFileExist,
// inTimeSpan. Deliberately NOT testing CheckLicense/createLicenseFile/
// validateLicenseFile/fetchLicense/checkStillLicenseValid — those make
// real network calls to a live license server (confirmed the hard way in
// clientcommunication earlier in this project: one such call took ~40s and
// wrote a real license file to disk). Nothing here touches the network or
// EtherCAT/motordriver, so no build tag or cgo toolchain is needed.

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// ─── HasError ───────────────────────────────────────────────────────────────

func TestHasError_StatusOK_ReturnsNil(t *testing.T) {
	resp := &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(bytes.NewReader(nil)),
	}
	if err := HasError(resp); err != nil {
		t.Errorf("HasError(200) = %v, want nil", err)
	}
}

func TestHasError_NonOKStatus_ReturnsErrorWithMessage(t *testing.T) {
	body := `{"message":"license expired"}`
	resp := &http.Response{
		StatusCode: 403,
		Body:       io.NopCloser(bytes.NewReader([]byte(body))),
	}
	err := HasError(resp)
	if err == nil {
		t.Fatal("HasError(403): expected error, got nil")
	}
	if err.Error() != "license expired" {
		t.Errorf("HasError error message = %q, want %q", err.Error(), "license expired")
	}
}

func TestHasError_NonOKStatus_MalformedBody_ReturnsEmptyMessageError(t *testing.T) {
	resp := &http.Response{
		StatusCode: 500,
		Body:       io.NopCloser(bytes.NewReader([]byte("not json"))),
	}
	err := HasError(resp)
	if err == nil {
		t.Fatal("HasError(500) with malformed body: expected error, got nil")
	}
	// json.Unmarshal fails silently (error ignored in source), ExceptionMessage
	// stays zero-valued, so the returned error message is empty — pinning that
	// actual (if slightly awkward) current behavior.
	if err.Error() != "" {
		t.Errorf("HasError with malformed body: message = %q, want empty", err.Error())
	}
}

func TestHasError_StatusCreated_TreatedAsError(t *testing.T) {
	// Only 200 is treated as success — even other 2xx codes count as an error
	// under the current (exact-match) implementation.
	resp := &http.Response{
		StatusCode: 201,
		Body:       io.NopCloser(bytes.NewReader([]byte(`{"message":"unexpected"}`))),
	}
	err := HasError(resp)
	if err == nil {
		t.Error("HasError(201): expected error under exact-200-match behavior, got nil")
	}
}

// ─── isLicenseFileExist ─────────────────────────────────────────────────────

func TestIsLicenseFileExist_FileExists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "license.lic")
	if err := os.WriteFile(path, []byte("dummy"), 0644); err != nil {
		t.Fatalf("test setup: %v", err)
	}

	if !isLicenseFileExist(path) {
		t.Error("isLicenseFileExist: expected true for an existing file")
	}
}

func TestIsLicenseFileExist_FileDoesNotExist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does_not_exist.lic")

	if isLicenseFileExist(path) {
		t.Error("isLicenseFileExist: expected false for a missing file")
	}
}

func TestIsLicenseFileExist_EmptyPath(t *testing.T) {
	if isLicenseFileExist("") {
		t.Error("isLicenseFileExist(''): expected false")
	}
}

// NOTE: inTimeSpan is NOT tested here — despite appearing in a `grep -n
// "^func "` scan of this file, it's dead reference code sitting inside a
// /** ... **/ block comment (a leftover Go Playground snippet at the end of
// license_checker.go), never actually compiled into the real package. The
// real expiry check in checkStillLicenseValid does the comparison inline
// (expiry.Before(time.Now())) instead of calling any such function. There
// is nothing here to test.
