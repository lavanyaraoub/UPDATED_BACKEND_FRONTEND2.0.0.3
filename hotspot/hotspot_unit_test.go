//go:build unit

package hotspot

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"EtherCAT/helper"
)

// fakeExecCommand replaces execCommand for the duration of a test with one
// that re-invokes this same test binary in a special subprocess mode
// (TestHelperProcess below) instead of ever calling a real script. This is
// the standard Go pattern for testing exec.Command-based code safely.
func fakeExecCommand(exitCode int, stdout string) func(name string, arg ...string) *exec.Cmd {
	return func(name string, arg ...string) *exec.Cmd {
		cs := []string{"-test.run=TestHelperProcess", "--"}
		cs = append(cs, name)
		cs = append(cs, arg...)
		cmd := exec.Command(os.Args[0], cs...)
		cmd.Env = []string{
			"GO_WANT_HELPER_PROCESS=1",
			fmt.Sprintf("HELPER_EXIT_CODE=%d", exitCode),
			fmt.Sprintf("HELPER_STDOUT=%s", stdout),
		}
		return cmd
	}
}

// TestHelperProcess is not a real test — it's the subprocess entry point
// fakeExecCommand spawns instead of a real script. Guarded so it's a no-op
// under normal `go test` runs.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	fmt.Fprint(os.Stdout, os.Getenv("HELPER_STDOUT"))
	var code int
	fmt.Sscanf(os.Getenv("HELPER_EXIT_CODE"), "%d", &code)
	os.Exit(code)
}

func setupFakeScript(t *testing.T, scriptRelPath string) {
	t.Helper()
	full := helper.AppendWDPath(scriptRelPath)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte("#!/bin/sh\necho ok\n"), 0644); err != nil {
		t.Fatalf("write fake script: %v", err)
	}
	t.Cleanup(func() {
		os.RemoveAll(filepath.Dir(full))
	})
}

func TestNewHotspot_ReturnsZeroValue(t *testing.T) {
	h := NewHotspot()
	if h.hotspotEnabled {
		t.Error("expected hotspotEnabled to default to false")
	}
}

func TestHotspot_Create_Success(t *testing.T) {
	orig := execCommand
	execCommand = fakeExecCommand(0, "hotspot created")
	defer func() { execCommand = orig }()

	h := NewHotspot()
	h.Create()
	// Create() discards the return value, so this is primarily a
	// no-panic guarantee on the success path — matches production usage.
}

func TestHotspot_Kill_Success(t *testing.T) {
	orig := execCommand
	execCommand = fakeExecCommand(0, "hotspot killed")
	defer func() { execCommand = orig }()

	h := NewHotspot()
	h.Kill()
}

func TestChangeHotspotState_Success_ReturnsStdout(t *testing.T) {
	orig := execCommand
	execCommand = fakeExecCommand(0, "hotspot created")
	defer func() { execCommand = orig }()

	h := NewHotspot()
	out, err := h.changeHotspotState("CREATE")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "hotspot created" {
		t.Errorf("stdout = %q, want %q", out, "hotspot created")
	}
}

func TestChangeHotspotState_ScriptFails_ReturnsWrappedError(t *testing.T) {
	orig := execCommand
	execCommand = fakeExecCommand(1, "")
	defer func() { execCommand = orig }()

	h := NewHotspot()
	_, err := h.changeHotspotState("KILL")
	if err == nil {
		t.Fatal("expected an error when the script exits non-zero")
	}
}

func TestConfigureWifi_Success(t *testing.T) {
	setupFakeScript(t, "/scripts/wifi.sh")

	orig := execCommand
	execCommand = fakeExecCommand(0, "wifi configured")
	defer func() { execCommand = orig }()

	if err := ConfigureWifi("myssid", "mypassword", "US"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConfigureWifi_ScriptFails_ReturnsError(t *testing.T) {
	setupFakeScript(t, "/scripts/wifi.sh")

	orig := execCommand
	execCommand = fakeExecCommand(1, "")
	defer func() { execCommand = orig }()

	if err := ConfigureWifi("myssid", "mypassword", "US"); err == nil {
		t.Fatal("expected an error when wifi.sh exits non-zero")
	}
}

func TestConfigureWifi_MissingScript_ReturnsChmodError(t *testing.T) {
	// Deliberately do NOT create the script — os.Chmod on a nonexistent
	// path should fail before execCommand is ever reached.
	orig := execCommand
	called := false
	execCommand = func(name string, arg ...string) *exec.Cmd {
		called = true
		return fakeExecCommand(0, "")(name, arg...)
	}
	defer func() { execCommand = orig }()

	err := ConfigureWifi("myssid", "mypassword", "US")
	if err == nil {
		t.Fatal("expected an error when wifi.sh does not exist")
	}
	if called {
		t.Error("execCommand should not be called when os.Chmod fails first")
	}
}
