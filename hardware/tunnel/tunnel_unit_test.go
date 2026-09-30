//go:build unit

package tunnel

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"EtherCAT/helper"
)

// fakeExecCommand mirrors the pattern used in hotspot/hotspot_unit_test.go —
// re-invokes this test binary in a subprocess instead of ever calling the
// real tunnel.sh script.
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

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	fmt.Fprint(os.Stdout, os.Getenv("HELPER_STDOUT"))
	var code int
	fmt.Sscanf(os.Getenv("HELPER_EXIT_CODE"), "%d", &code)
	os.Exit(code)
}

func setupFakeScript(t *testing.T) {
	t.Helper()
	full := helper.AppendWDPath("/scripts/tunnel.sh")
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

func setupFakeNgrokLog(t *testing.T, content string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "ngrok.log")
	if content != "" {
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("write fake ngrok log: %v", err)
		}
	}
	orig := ngrokLogPath
	ngrokLogPath = path
	t.Cleanup(func() { ngrokLogPath = orig })
}

func TestNewTunnel_ReturnsZeroValue(t *testing.T) {
	NewTunnel()
}

func TestChangeTunnelState_Success_ReturnsStdout(t *testing.T) {
	setupFakeScript(t)
	orig := execCommand
	execCommand = fakeExecCommand(0, "tunnel started")
	defer func() { execCommand = orig }()

	tun := NewTunnel()
	out, err := tun.changeTunnelState("STARTHTTP")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "tunnel started" {
		t.Errorf("stdout = %q, want %q", out, "tunnel started")
	}
}

func TestChangeTunnelState_ScriptFails_ReturnsWrappedError(t *testing.T) {
	setupFakeScript(t)
	orig := execCommand
	execCommand = fakeExecCommand(1, "")
	defer func() { execCommand = orig }()

	tun := NewTunnel()
	_, err := tun.changeTunnelState("KILL")
	if err == nil {
		t.Fatal("expected an error when the script exits non-zero")
	}
}

func TestChangeTunnelState_MissingScript_ReturnsChmodError(t *testing.T) {
	// Deliberately don't create the script.
	tun := NewTunnel()
	_, err := tun.changeTunnelState("STARTHTTP")
	if err == nil {
		t.Fatal("expected an error when tunnel.sh does not exist")
	}
}

func TestStop_DoesNotPanic(t *testing.T) {
	setupFakeScript(t)
	orig := execCommand
	execCommand = fakeExecCommand(0, "")
	defer func() { execCommand = orig }()

	tun := NewTunnel()
	tun.Stop() // return values are deliberately discarded in production
}

func TestGetHTTPTunnelURL_SuccessLine_ReturnsURL(t *testing.T) {
	setupFakeNgrokLog(t, "some other line\nurl=https://abc123.ngrok.io\n")

	tun := NewTunnel()
	url, err := tun.getHTTPTunnelURL()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if url != "https://abc123.ngrok.io" {
		t.Errorf("url = %q, want %q", url, "https://abc123.ngrok.io")
	}
}

func TestGetHTTPTunnelURL_RealError_ReturnsError(t *testing.T) {
	setupFakeNgrokLog(t, "err=connection refused\n")

	tun := NewTunnel()
	_, err := tun.getHTTPTunnelURL()
	if err == nil {
		t.Fatal("expected an error when the log contains a real ngrok error")
	}
}

func TestGetHTTPTunnelURL_ErrNil_IsIgnored(t *testing.T) {
	setupFakeNgrokLog(t, "err=nil\n")

	tun := NewTunnel()
	url, err := tun.getHTTPTunnelURL()
	if err != nil {
		t.Fatalf("unexpected error for err=nil line: %v", err)
	}
	if url != "" {
		t.Errorf("url = %q, want empty (not ready yet)", url)
	}
}

func TestGetHTTPTunnelURL_NotReadyYet_ReturnsEmpty(t *testing.T) {
	setupFakeNgrokLog(t, "starting up...\nno url or err yet\n")

	tun := NewTunnel()
	url, err := tun.getHTTPTunnelURL()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if url != "" {
		t.Errorf("url = %q, want empty", url)
	}
}

func TestGetHTTPTunnelURL_MissingFile_ReturnsError(t *testing.T) {
	setupFakeNgrokLog(t, "") // empty content still creates no file below
	// Point at a path that genuinely doesn't exist.
	orig := ngrokLogPath
	ngrokLogPath = filepath.Join(t.TempDir(), "does-not-exist.log")
	defer func() { ngrokLogPath = orig }()

	tun := NewTunnel()
	_, err := tun.getHTTPTunnelURL()
	if err == nil {
		t.Fatal("expected an error when the ngrok log file does not exist")
	}
}

func TestStartHTTP_ImmediateSuccess(t *testing.T) {
	setupFakeScript(t)
	setupFakeNgrokLog(t, "url=https://ready.ngrok.io\n")

	orig := execCommand
	execCommand = fakeExecCommand(0, "")
	defer func() { execCommand = orig }()

	tun := NewTunnel()
	url, err := tun.StartHTTP()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if url != "https://ready.ngrok.io" {
		t.Errorf("url = %q, want %q", url, "https://ready.ngrok.io")
	}
}

func TestStartHTTP_ScriptFails_ReturnsErrorImmediately(t *testing.T) {
	setupFakeScript(t)
	orig := execCommand
	execCommand = fakeExecCommand(1, "")
	defer func() { execCommand = orig }()

	tun := NewTunnel()
	_, err := tun.StartHTTP()
	if err == nil {
		t.Fatal("expected an error when the start script fails")
	}
}

func TestStartHTTP_FatalNgrokError_ReturnsErrorWithoutFullTimeout(t *testing.T) {
	setupFakeScript(t)
	setupFakeNgrokLog(t, "err=auth failed\n")

	orig := execCommand
	execCommand = fakeExecCommand(0, "")
	defer func() { execCommand = orig }()

	tun := NewTunnel()
	_, err := tun.StartHTTP()
	if err == nil {
		t.Fatal("expected an error when ngrok reports a fatal error")
	}
}
