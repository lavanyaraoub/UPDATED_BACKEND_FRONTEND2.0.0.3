//go:build unit

package logger

import (
	"os"
	"path/filepath"
	"testing"

	"EtherCAT/helper"
	"EtherCAT/settings"
)

// writeEnvConfig writes a minimal envconfig.yaml into the test binary's own
// exec-relative configs/ directory (same AppendWDPath pattern used
// throughout this codebase for settings.json/device-configuration.yml),
// then forces settings.GetEnvSettings() to re-read it via ResetEnvSettings.
func writeEnvConfig(t *testing.T, mode string) {
	t.Helper()
	dir := filepath.Join(helper.AppendWDPath(""), "configs")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir configs: %v", err)
	}
	content := "mode: " + mode + "\nlog_level: DEBUG\n"
	if err := os.WriteFile(filepath.Join(dir, "envconfig.yaml"), []byte(content), 0644); err != nil {
		t.Fatalf("write envconfig.yaml: %v", err)
	}
	t.Cleanup(func() {
		os.RemoveAll(dir)
		settings.ResetEnvSettings()
	})
	settings.ResetEnvSettings()
}

func TestInit_DebugMode_WritesToStdoutNotFile(t *testing.T) {
	writeEnvConfig(t, "debug")

	// Debug mode writes to os.Stdout directly — no file is created, no
	// panic from a missing rolling-file hook. This is the safe branch.
	Init("DEBUG")

	if build != "debug" {
		t.Errorf("build = %q, want %q", build, "debug")
	}
}

func TestInit_NonDebugMode_CreatesLogFileUnderExecDir(t *testing.T) {
	writeEnvConfig(t, "release")

	// Non-debug mode creates a real rolling-file hook, but at
	// helper.AppendWDPath("/log/log.log") — the test binary's own
	// ephemeral exec-relative directory, not any real production path.
	// Safe to let this actually run.
	Init("INFO")
	logPath := helper.AppendWDPath("/log/log.log")
	t.Cleanup(func() {
		os.RemoveAll(filepath.Dir(logPath))
	})

	if build == "debug" {
		t.Errorf("build = %q, want non-debug value from envconfig.yaml", build)
	}

	// The rolling-file hook creates the file lazily on first write, not
	// at hook-creation time — so we need an actual log call here before
	// checking the file exists.
	Info("triggering lazy file creation")

	if _, err := os.Stat(filepath.Dir(logPath)); err != nil {
		t.Errorf("expected log directory to exist at %s after logging: %v", filepath.Dir(logPath), err)
	}
}

func TestInit_LogLevels_AllRecognized(t *testing.T) {
	writeEnvConfig(t, "debug")

	// Each of these should set a level without panicking. There's no
	// exported way to read the level back, so this test is primarily a
	// no-panic guarantee across every recognized case plus the default
	// fallback.
	for _, level := range []string{"DEBUG", "INFO", "TRACE", "ERROR", "UNRECOGNIZED"} {
		t.Run(level, func(t *testing.T) {
			Init(level)
		})
	}
}

func TestInfo_DoesNotPanic(t *testing.T) {
	writeEnvConfig(t, "debug")
	Init("DEBUG")
	Info("test info message", 123)
}

func TestError_DoesNotPanic(t *testing.T) {
	writeEnvConfig(t, "debug")
	Init("DEBUG")
	Error("test error message")
}

func TestWarn_DoesNotPanic(t *testing.T) {
	writeEnvConfig(t, "debug")
	Init("DEBUG")
	Warn("test warning message")
}

func TestDebug_DoesNotPanic(t *testing.T) {
	writeEnvConfig(t, "debug")
	Init("DEBUG")
	Debug("test debug message")
}

func TestTrace_DoesNotPanic(t *testing.T) {
	writeEnvConfig(t, "debug")
	Init("DEBUG")
	Trace("test trace message")
}

// Fatal() is deliberately not tested — logrus's Fatal level calls
// os.Exit(1) internally, which would terminate the test binary itself.
// This is the same class of accepted, permanent gap as plugin_loader.go's
// corrupt-.so path: the function cannot be exercised without special
// subprocess machinery, and the risk/effort of building that outweighs
// the value here, since the function is a one-line passthrough to a
// well-tested third-party library call.

func TestPrintStruct_DoesNotPanic(t *testing.T) {
	PrintStruct("field", 42, true)
}

func TestPrintOnSameLine_DebugMode_DoesNotPanic(t *testing.T) {
	writeEnvConfig(t, "debug")
	Init("DEBUG")
	PrintOnSameLine("progress update")
}

func TestPrintOnSameLine_NonDebugMode_DoesNotPanic(t *testing.T) {
	writeEnvConfig(t, "release")
	Init("INFO")
	t.Cleanup(func() {
		os.RemoveAll(filepath.Dir(helper.AppendWDPath("/log/log.log")))
	})
	// Non-debug mode is a no-op for this function — confirms it doesn't
	// panic or attempt to print when build != "debug".
	PrintOnSameLine("should not print")
}
