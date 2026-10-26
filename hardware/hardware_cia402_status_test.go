//go:build hardware

package hardware_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestHardwareCIA402_ReadOnlyStatusObjects reads CiA-402 status objects via SDO.
//
// Requires the EtherCAT master to be active (application running).
// Skips automatically when master is in Idle phase.
func TestHardwareCIA402_ReadOnlyStatusObjects(t *testing.T) {
	if os.Getenv("ALLOW_HARDWARE_TESTS") != "1" {
		t.Skip("set ALLOW_HARDWARE_TESTS=1 to run hardware tests")
	}
	if os.Getenv("ALLOW_MOTION_TESTS") == "1" {
		t.Skip("read-only CIA402 test skipped when ALLOW_MOTION_TESTS=1 — use -run TestHardwareMotion for motion tests")
	}

	if masterIsIdle(t) {
		t.Skip("EtherCAT master is in Idle phase (application not running); " +
			"CiA-402 SDO reads require an active master. " +
			"Start the application then re-run with ALLOW_HARDWARE_TESTS=1.")
	}

	slavePosition := cia402Env("ETHERCAT_SLAVE_POSITION", "0")

	type cia402SDOObject struct {
		name      string
		index     string
		subindex  string
		dataType  string
		expectEnv string
		optional  bool
	}

	objects := []cia402SDOObject{
		{
			name:      "statusword",
			index:     "0x6041",
			subindex:  "0",
			dataType:  "uint16",
			expectEnv: "EXPECTED_CIA402_STATUSWORD",
		},
		{
			name:      "error_code",
			index:     "0x603f",
			subindex:  "0",
			dataType:  "uint16",
			expectEnv: "EXPECTED_CIA402_ERROR_CODE",
		},
		{
			name:      "mode_of_operation_display",
			index:     "0x6061",
			subindex:  "0",
			dataType:  "int8",
			expectEnv: "EXPECTED_CIA402_MODE_DISPLAY",
			optional:  true,
		},
	}

	for _, obj := range objects {
		obj := obj
		t.Run(obj.name, func(t *testing.T) {
			value, raw, err := cia402UploadSDO(slavePosition, obj.index, obj.subindex, obj.dataType)
			if err != nil {
				if obj.optional {
					t.Skipf("optional CiA-402 object %s (%s:%s) not readable: %v",
						obj.name, obj.index, obj.subindex, err)
				}
				t.Fatalf("read-only CiA-402 SDO upload failed for %s (%s:%s): %v",
					obj.name, obj.index, obj.subindex, err)
			}

			t.Logf("slave position %s %s (%s:%s) = %s (%d); raw=%q",
				slavePosition, obj.name, obj.index, obj.subindex,
				cia402FormatHex(value, obj.dataType), value, raw)

			if expectedRaw := strings.TrimSpace(os.Getenv(obj.expectEnv)); expectedRaw != "" {
				expected, err := cia402ParseInteger(expectedRaw)
				if err != nil {
					t.Fatalf("%s=%q is not a valid integer/hex value: %v", obj.expectEnv, expectedRaw, err)
				}
				if value != expected {
					t.Fatalf("%s = %s (%d), want %s (%d)",
						obj.name,
						cia402FormatHex(value, obj.dataType), value,
						cia402FormatHex(expected, obj.dataType), expected)
				}
			}
		})
	}
}

// TestHardwareCIA402_ErrorCodeIsZeroWhenExpected asserts the drive error code
// is zero. Opt-in via REQUIRE_CIA402_NO_ERROR=1.
// Skips when master is in Idle phase.
func TestHardwareCIA402_ErrorCodeIsZeroWhenExpected(t *testing.T) {
	if os.Getenv("ALLOW_HARDWARE_TESTS") != "1" {
		t.Skip("set ALLOW_HARDWARE_TESTS=1 to run hardware tests")
	}
	if strings.TrimSpace(os.Getenv("REQUIRE_CIA402_NO_ERROR")) != "1" {
		t.Skip("set REQUIRE_CIA402_NO_ERROR=1 to require 0x603F error code to be zero")
	}

	if masterIsIdle(t) {
		t.Skip("EtherCAT master is in Idle phase; error code check requires active master")
	}

	slavePosition := cia402Env("ETHERCAT_SLAVE_POSITION", "0")
	value, raw, err := cia402UploadSDO(slavePosition, "0x603f", "0", "uint16")
	if err != nil {
		t.Fatalf("read-only CiA-402 error-code upload failed: %v", err)
	}
	if value != 0 {
		t.Fatalf("CiA-402 error code = %s (%d), raw=%q; want 0x0000",
			cia402FormatHex(value, "uint16"), value, raw)
	}
	t.Logf("CiA-402 error code is zero; raw=%q", raw)
}

// TestHardwareMaster_PhaseAndSlaveState reads master phase and per-slave
// AL state without any SDO — works in Idle and Active alike.
func TestHardwareMaster_PhaseAndSlaveState(t *testing.T) {
	if os.Getenv("ALLOW_HARDWARE_TESTS") != "1" {
		t.Skip("set ALLOW_HARDWARE_TESTS=1 to run hardware tests")
	}

	masterOut, err := runEtherCATReadOnly(t, "master")
	if err != nil {
		if os.Getenv("ALLOW_NO_ETHERCAT_BUS") == "1" {
			t.Skipf("ethercat master failed and ALLOW_NO_ETHERCAT_BUS=1: %v", err)
		}
		t.Fatalf("ethercat master: %v", err)
	}

	// Log phase — useful whether Idle or Active.
	phase := extractField(masterOut, `Phase:\s+(\S+)`)
	active := extractField(masterOut, `Active:\s+(\S+)`)
	t.Logf("Master phase: %s  active: %s", orUnknown(phase), orUnknown(active))

	// Read per-slave AL state (Application Layer state: INIT/PREOP/SAFEOP/OP).
	slavesOut, err := runEtherCATReadOnly(t, "slaves", "-v")
	if err != nil {
		t.Logf("ethercat slaves -v failed (non-fatal): %v", err)
		return
	}

	alState := extractField(slavesOut, `State:\s+(\S+)`)
	t.Logf("Slave AL state: %s", orUnknown(alState))

	// If REQUIRE_SLAVE_OP=1, assert the slave is in OP state.
	if strings.TrimSpace(os.Getenv("REQUIRE_SLAVE_OP")) == "1" {
		if alState != "OP" {
			t.Fatalf("slave AL state = %q, want OP (set REQUIRE_SLAVE_OP=1 only when application is running)", alState)
		}
		t.Logf("slave is in OP state")
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// extractField extracts the first capture group from pattern in text.
// Delegates to parseEEPROMField (defined in hardware_sdo_identity_test.go).
func extractField(text, pattern string) string {
	return parseEEPROMField(text, pattern)
}

func cia402UploadSDO(slavePosition, index, subindex, dataType string) (int64, string, error) {
	cli := cia402Env("ETHERCAT_CLI", "ethercat")
	timeout := cia402Env("HARDWARE_COMMAND_TIMEOUT", cia402Env("ETHERCAT_COMMAND_TIMEOUT", "5s"))
	duration, err := time.ParseDuration(timeout)
	if err != nil {
		return 0, "", fmt.Errorf("invalid hardware command timeout %q: %w", timeout, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()

	cmd := exec.CommandContext(ctx, cli, "upload", "-p", slavePosition, "-t", dataType, index, subindex)
	output, err := cmd.CombinedOutput()
	raw := strings.TrimSpace(string(output))
	if ctx.Err() == context.DeadlineExceeded {
		return 0, raw, fmt.Errorf("command timed out after %s", timeout)
	}
	if err != nil {
		return 0, raw, fmt.Errorf("%s upload -p %s -t %s %s %s failed: %w; output=%q",
			cli, slavePosition, dataType, index, subindex, err, raw)
	}

	value, err := cia402ParseUploadOutput(raw)
	if err != nil {
		return 0, raw, err
	}
	return value, raw, nil
}

func cia402ParseUploadOutput(raw string) (int64, error) {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return 0, fmt.Errorf("empty SDO upload output")
	}
	return cia402ParseInteger(fields[0])
}

func cia402ParseInteger(raw string) (int64, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, fmt.Errorf("empty value")
	}
	parsed, err := strconv.ParseInt(value, 0, 64)
	if err == nil {
		return parsed, nil
	}
	unsigned, unsignedErr := strconv.ParseUint(value, 0, 64)
	if unsignedErr == nil {
		return int64(unsigned), nil
	}
	return 0, err
}

func cia402FormatHex(value int64, dataType string) string {
	switch dataType {
	case "uint16":
		return fmt.Sprintf("0x%04x", uint16(value))
	case "uint32":
		return fmt.Sprintf("0x%08x", uint32(value))
	case "int8":
		return fmt.Sprintf("0x%02x", uint8(value))
	default:
		return fmt.Sprintf("0x%x", value)
	}
}

func cia402Env(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}
