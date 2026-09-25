//go:build unit

package systemupdate

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func withInstallPath(t *testing.T, dir string) {
	t.Helper()
	orig := installPath
	installPath = dir
	t.Cleanup(func() { installPath = orig })
}

func withRtcUpdateRoot(t *testing.T, dir string) {
	t.Helper()
	orig := rtcUpdateRoot
	rtcUpdateRoot = dir
	t.Cleanup(func() { rtcUpdateRoot = orig })
}

func withHTTPGet(t *testing.T, fn func(url string) (*http.Response, error)) {
	t.Helper()
	orig := httpGet
	httpGet = fn
	t.Cleanup(func() { httpGet = orig })
}

func TestExtractVersion(t *testing.T) {
	cases := []struct {
		filename string
		want     string
	}{
		{"jamun_v2.0.0.4.tar.gz", "2.0.0.4"},
		{"jamun_v1.0.0.0.tar.gz", "1.0.0.0"},
		{"jamun_v2.0.0.4", "2.0.0.4"},
		{"", ""},
	}
	for _, c := range cases {
		t.Run(c.filename, func(t *testing.T) {
			if got := extractVersion(c.filename); got != c.want {
				t.Errorf("extractVersion(%q) = %q, want %q", c.filename, got, c.want)
			}
		})
	}
}

func TestReadInstalledVersion_MissingFile_ReturnsEmpty(t *testing.T) {
	withInstallPath(t, t.TempDir())
	if got := readInstalledVersion(); got != "" {
		t.Errorf("readInstalledVersion() = %q, want empty for missing file", got)
	}
}

func TestReadInstalledVersion_ValidFile_ReturnsTrimmedContent(t *testing.T) {
	dir := t.TempDir()
	withInstallPath(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "version.txt"), []byte("2.0.0.4\n"), 0644); err != nil {
		t.Fatalf("write version.txt: %v", err)
	}
	if got := readInstalledVersion(); got != "2.0.0.4" {
		t.Errorf("readInstalledVersion() = %q, want %q", got, "2.0.0.4")
	}
}

func TestCheckDiskSpace_SufficientSpace_ReturnsNil(t *testing.T) {
	orig := syscallStatfsCall
	syscallStatfsCall = func(path string, stat *syscall.Statfs_t) error {
		stat.Bavail = 1000000
		stat.Bsize = 4096
		return nil
	}
	defer func() { syscallStatfsCall = orig }()

	if err := checkDiskSpace("/anywhere", 100*1024*1024); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCheckDiskSpace_InsufficientSpace_ReturnsError(t *testing.T) {
	orig := syscallStatfsCall
	syscallStatfsCall = func(path string, stat *syscall.Statfs_t) error {
		stat.Bavail = 10
		stat.Bsize = 1
		return nil
	}
	defer func() { syscallStatfsCall = orig }()

	if err := checkDiskSpace("/anywhere", 100*1024*1024); err == nil {
		t.Error("expected an error for insufficient disk space")
	}
}

func TestCheckDiskSpace_StatfsFails_AllowsDownload(t *testing.T) {
	orig := syscallStatfsCall
	syscallStatfsCall = func(path string, stat *syscall.Statfs_t) error {
		return &os.PathError{Op: "statfs", Path: path, Err: os.ErrNotExist}
	}
	defer func() { syscallStatfsCall = orig }()

	// Documented behavior: if we can't check, allow the download rather
	// than blocking on an unrelated failure.
	if err := checkDiskSpace("/anywhere", 100*1024*1024); err != nil {
		t.Errorf("expected nil (fail-open) when statfs itself fails, got: %v", err)
	}
}

func TestValidateStagedUpdate_AllFilesPresent_ReturnsNil(t *testing.T) {
	stage := t.TempDir()
	for _, f := range []string{"jamun", "libethercatinterface.so", "ethercatinterface.h"} {
		if err := os.WriteFile(filepath.Join(stage, f), []byte("x"), 0644); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
	for _, d := range []string{"commands", "configs", "scripts"} {
		if err := os.MkdirAll(filepath.Join(stage, d), 0755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	if err := validateStagedUpdate(stage); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateStagedUpdate_MissingRequiredFile_ReturnsError(t *testing.T) {
	stage := t.TempDir()
	// Only create dirs, no files.
	for _, d := range []string{"commands", "configs", "scripts"} {
		os.MkdirAll(filepath.Join(stage, d), 0755)
	}

	if err := validateStagedUpdate(stage); err == nil {
		t.Error("expected an error when required files are missing")
	}
}

func TestValidateStagedUpdate_MissingRequiredDir_ReturnsError(t *testing.T) {
	stage := t.TempDir()
	for _, f := range []string{"jamun", "libethercatinterface.so", "ethercatinterface.h"} {
		os.WriteFile(filepath.Join(stage, f), []byte("x"), 0644)
	}
	// No dirs created.

	if err := validateStagedUpdate(stage); err == nil {
		t.Error("expected an error when required directories are missing")
	}
}

func TestValidateStagedUpdate_FileWhereDirExpected_ReturnsError(t *testing.T) {
	stage := t.TempDir()
	for _, f := range []string{"jamun", "libethercatinterface.so", "ethercatinterface.h"} {
		os.WriteFile(filepath.Join(stage, f), []byte("x"), 0644)
	}
	// "commands" exists but as a file, not a directory.
	os.WriteFile(filepath.Join(stage, "commands"), []byte("x"), 0644)
	os.MkdirAll(filepath.Join(stage, "configs"), 0755)
	os.MkdirAll(filepath.Join(stage, "scripts"), 0755)

	if err := validateStagedUpdate(stage); err == nil {
		t.Error("expected an error when a required directory is actually a file")
	}
}

func TestGetLatestReleaseInfo_Success_ReturnsFilenameAndURL(t *testing.T) {
	body := `{"items":[{"name":"releases/other.txt"},{"name":"releases/jamun_v2.0.0.4.tar.gz"}]}`
	withHTTPGet(t, func(url string) (*http.Response, error) {
		rec := httptest.NewRecorder()
		rec.Body.WriteString(body)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(rec.Body)}, nil
	})

	filename, downloadURL, err := getLatestReleaseInfo()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filename != "jamun_v2.0.0.4.tar.gz" {
		t.Errorf("filename = %q, want %q", filename, "jamun_v2.0.0.4.tar.gz")
	}
	if downloadURL == "" {
		t.Error("expected a non-empty download URL")
	}
}

func TestGetLatestReleaseInfo_NoTarGzInBucket_ReturnsError(t *testing.T) {
	withHTTPGet(t, func(url string) (*http.Response, error) {
		rec := httptest.NewRecorder()
		rec.Body.WriteString(`{"items":[{"name":"releases/readme.txt"}]}`)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(rec.Body)}, nil
	})

	_, _, err := getLatestReleaseInfo()
	if err == nil {
		t.Error("expected an error when no .tar.gz release is found")
	}
}

func TestGetLatestReleaseInfo_HTTPError_ReturnsError(t *testing.T) {
	withHTTPGet(t, func(url string) (*http.Response, error) {
		return nil, &os.PathError{Op: "get", Err: os.ErrClosed}
	})

	_, _, err := getLatestReleaseInfo()
	if err == nil {
		t.Error("expected an error when the HTTP request itself fails")
	}
}

func TestGetLatestReleaseInfo_NonOKStatus_ReturnsError(t *testing.T) {
	withHTTPGet(t, func(url string) (*http.Response, error) {
		rec := httptest.NewRecorder()
		return &http.Response{StatusCode: http.StatusNotFound, Status: "404 Not Found", Body: io.NopCloser(rec.Body)}, nil
	})

	_, _, err := getLatestReleaseInfo()
	if err == nil {
		t.Error("expected an error for a non-200 bucket list response")
	}
}

func TestFetchRelease_AlreadyOnLatestVersion_ReturnsNilWithoutDownload(t *testing.T) {
	dir := t.TempDir()
	withInstallPath(t, dir)
	withRtcUpdateRoot(t, t.TempDir())
	os.WriteFile(filepath.Join(dir, "version.txt"), []byte("2.0.0.4"), 0644)

	withHTTPGet(t, func(url string) (*http.Response, error) {
		rec := httptest.NewRecorder()
		rec.Body.WriteString(`{"items":[{"name":"releases/jamun_v2.0.0.4.tar.gz"}]}`)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(rec.Body)}, nil
	})

	if err := fetchRelease(false); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestFetchRelease_OlderVersionAvailable_ReturnsNilWithoutDownload(t *testing.T) {
	dir := t.TempDir()
	withInstallPath(t, dir)
	withRtcUpdateRoot(t, t.TempDir())
	os.WriteFile(filepath.Join(dir, "version.txt"), []byte("3.0.0.0"), 0644)

	withHTTPGet(t, func(url string) (*http.Response, error) {
		rec := httptest.NewRecorder()
		rec.Body.WriteString(`{"items":[{"name":"releases/jamun_v1.0.0.0.tar.gz"}]}`)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(rec.Body)}, nil
	})

	if err := fetchRelease(false); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestFetchRelease_AlreadyStaged_ReturnsNilWithoutDownload(t *testing.T) {
	dir := t.TempDir()
	withInstallPath(t, dir)
	rtcRoot := t.TempDir()
	withRtcUpdateRoot(t, rtcRoot)
	os.WriteFile(filepath.Join(dir, "version.txt"), []byte("1.0.0.0"), 0644)

	// Staged folder already has the "jamun" binary present.
	stageDir := filepath.Join(rtcRoot, "jamun_v2.0.0.4")
	os.MkdirAll(stageDir, 0755)
	os.WriteFile(filepath.Join(stageDir, "jamun"), []byte("x"), 0644)

	withHTTPGet(t, func(url string) (*http.Response, error) {
		rec := httptest.NewRecorder()
		rec.Body.WriteString(`{"items":[{"name":"releases/jamun_v2.0.0.4.tar.gz"}]}`)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(rec.Body)}, nil
	})

	if err := fetchRelease(false); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestFetchRelease_DiscoveryFails_ReturnsWrappedError(t *testing.T) {
	withHTTPGet(t, func(url string) (*http.Response, error) {
		return nil, &os.PathError{Op: "get", Err: os.ErrClosed}
	})

	if err := fetchRelease(false); err == nil {
		t.Error("expected an error when release discovery fails")
	}
}

func TestFetchRelease_InsufficientDiskSpace_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	withInstallPath(t, dir)
	withRtcUpdateRoot(t, t.TempDir())
	// No version.txt — treated as unknown, download allowed to proceed
	// as far as the disk-space check.

	withHTTPGet(t, func(url string) (*http.Response, error) {
		rec := httptest.NewRecorder()
		rec.Body.WriteString(`{"items":[{"name":"releases/jamun_v1.0.0.0.tar.gz"}]}`)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(rec.Body)}, nil
	})

	origStatfs := syscallStatfsCall
	syscallStatfsCall = func(path string, stat *syscall.Statfs_t) error {
		stat.Bavail = 1
		stat.Bsize = 1
		return nil
	}
	defer func() { syscallStatfsCall = origStatfs }()

	if err := fetchRelease(false); err == nil {
		t.Error("expected an error when disk space is insufficient")
	}
}

func TestIsAnUpdateInProgress_DefaultFalse(t *testing.T) {
	if isAnUpdateInProgress() {
		t.Error("expected isAnUpdateInProgress() to be false by default")
	}
}

func TestApplyUpdate_AlwaysReturnsDisabledError(t *testing.T) {
	if err := applyUpdate("/some/stage/path"); err == nil {
		t.Error("expected applyUpdate to always return an error (disabled, handled by OTA script)")
	}
}

func TestSendBootSuccessNotification_DoesNotPanic(t *testing.T) {
	SendBootSuccessNotification()
}

func TestReadFileNameOfUpdate_MissingFile_ReturnsEmpty(t *testing.T) {
	withRtcUpdateRoot(t, t.TempDir())
	if got := readFileNameOfUpdate(); got != "" {
		t.Errorf("readFileNameOfUpdate() = %q, want empty for missing file", got)
	}
}

func TestReadFileNameOfUpdate_ValidFile_ReturnsName(t *testing.T) {
	dir := t.TempDir()
	withRtcUpdateRoot(t, dir)
	os.WriteFile(filepath.Join(dir, "current_update"), []byte("Name:jamun_v2.0.0.4.tar.gz"), 0644)

	if got := readFileNameOfUpdate(); got != "jamun_v2.0.0.4.tar.gz" {
		t.Errorf("readFileNameOfUpdate() = %q, want %q", got, "jamun_v2.0.0.4.tar.gz")
	}
}

func TestCheckforUpdates_AlreadyInProgress_IsNoOp(t *testing.T) {
	checkForUpdateInProgress = true
	defer func() { checkForUpdateInProgress = false }()

	called := false
	withHTTPGet(t, func(url string) (*http.Response, error) {
		called = true
		return nil, &os.PathError{Op: "get", Err: os.ErrClosed}
	})

	CheckforUpdates(false)

	if called {
		t.Error("expected CheckforUpdates to return immediately without making any HTTP call")
	}
}

func TestCheckforUpdates_SystemUpdateInProgress_IsNoOp(t *testing.T) {
	isUpdateInProgress = true
	defer func() { isUpdateInProgress = false }()

	called := false
	withHTTPGet(t, func(url string) (*http.Response, error) {
		called = true
		return nil, &os.PathError{Op: "get", Err: os.ErrClosed}
	})

	CheckforUpdates(false)

	if called {
		t.Error("expected CheckforUpdates to return immediately when a system update is already in progress")
	}
}
