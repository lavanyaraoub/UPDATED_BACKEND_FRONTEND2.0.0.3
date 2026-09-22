//go:build hardware

package hardware_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// requireHardwareSmokeOptIn skips or fails the test based on the
// ALLOW_HARDWARE_TESTS and ALLOW_MOTION_TESTS environment variables.
func requireHardwareSmokeOptIn(t *testing.T) {
	t.Helper()
	if strings.TrimSpace(os.Getenv("ALLOW_HARDWARE_TESTS")) != "1" {
		t.Skip("set ALLOW_HARDWARE_TESTS=1 to run read-only hardware checks")
	}
	if strings.TrimSpace(os.Getenv("ALLOW_MOTION_TESTS")) == "1" {
		t.Skip("read-only smoke test skipped when ALLOW_MOTION_TESTS=1 — use -run TestHardwareMotion for motion tests")
	}
}

// TestHardwareSlaveIdentity_EEPROM reads vendor ID, product code, and revision
// from the slave's EEPROM via "ethercat slaves -v".
//
// This works in Idle phase (master not activated) because the IgH kernel module
// caches EEPROM data at slave detection time — no SDO mailbox required.
func TestHardwareSlaveIdentity_EEPROM(t *testing.T) {
	requireHardwareSmokeOptIn(t)

	out, err := runEtherCATReadOnly(t, "slaves", "-v")
	if err != nil {
		if os.Getenv("ALLOW_NO_ETHERCAT_BUS") == "1" {
			t.Skipf("ethercat slaves -v failed and ALLOW_NO_ETHERCAT_BUS=1: %v\n%s", err, out)
		}
		t.Fatalf("ethercat slaves -v failed: %v\n%s", err, out)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatal("ethercat slaves -v returned empty output")
	}
	t.Logf("ethercat slaves -v output:\n%s", out)

	vendorID := parseEEPROMField(out, `Vendor\s+Id:\s+(0x[0-9a-fA-F]+)`)
	productCode := parseEEPROMField(out, `Product\s+code:\s+(0x[0-9a-fA-F]+)`)
	revisionNumber := parseEEPROMField(out, `Revision\s+number:\s+(0x[0-9a-fA-F]+)`)

	t.Logf("Vendor ID:        %s", orUnknown(vendorID))
	t.Logf("Product code:     %s", orUnknown(productCode))
	t.Logf("Revision number:  %s", orUnknown(revisionNumber))

	// Optional assertion: set env vars to enforce expected values.
	checkEEPROMExpectation(t, "EXPECTED_ETHERCAT_VENDOR_ID", "vendor ID", vendorID)
	checkEEPROMExpectation(t, "EXPECTED_ETHERCAT_PRODUCT_CODE", "product code", productCode)
	checkEEPROMExpectation(t, "EXPECTED_ETHERCAT_REVISION_NUMBER", "revision number", revisionNumber)
}

// TestHardwareSlaveIdentity_SDO reads the CiA-402 identity object (0x1018)
// via SDO upload. This requires the master to be active (PREOP or higher).
// The test skips automatically when the master is in Idle phase.
func TestHardwareSlaveIdentity_SDO(t *testing.T) {
	requireHardwareSmokeOptIn(t)

	if masterIsIdle(t) {
		t.Skip("EtherCAT master is in Idle phase (application not running); SDO identity check skipped. " +
			"Start the application to test SDO access.")
	}

	type sdoIdentityObject struct {
		Name     string
		Index    string
		SubIndex string
		EnvName  string
	}

	objects := []sdoIdentityObject{
		{Name: "vendor_id", Index: "0x1018", SubIndex: "1", EnvName: "EXPECTED_ETHERCAT_VENDOR_ID"},
		{Name: "product_code", Index: "0x1018", SubIndex: "2", EnvName: "EXPECTED_ETHERCAT_PRODUCT_CODE"},
		{Name: "revision_number", Index: "0x1018", SubIndex: "3", EnvName: "EXPECTED_ETHERCAT_REVISION_NUMBER"},
	}

	position := sdoEnv("ETHERCAT_SLAVE_POSITION", "0")
	cli := sdoEnv("ETHERCAT_CLI", "ethercat")
	timeout := sdoTimeout(t)

	cliPath, err := exec.LookPath(cli)
	if err != nil {
		t.Fatalf("EtherCAT CLI %q not found in PATH: %v", cli, err)
	}

	for _, obj := range objects {
		obj := obj
		t.Run(obj.Name, func(t *testing.T) {
			value, raw := readIdentitySDOUint32(t, cliPath, timeout, position, obj.Index, obj.SubIndex)
			t.Logf("slave position %s %s (%s:%s) = 0x%08x (%d); raw=%q",
				position, obj.Name, obj.Index, obj.SubIndex, value, value, raw)

			if expectedRaw := strings.TrimSpace(os.Getenv(obj.EnvName)); expectedRaw != "" {
				expected, err := parseExpectedUint32(expectedRaw)
				if err != nil {
					t.Fatalf("invalid %s=%q: %v", obj.EnvName, expectedRaw, err)
				}
				if value != expected {
					t.Fatalf("%s = 0x%08x (%d), want 0x%08x (%d) from %s",
						obj.Name, value, value, expected, expected, obj.EnvName)
				}
			}
		})
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// masterIsIdle returns true when "ethercat master" reports Phase: Idle.
func masterIsIdle(t *testing.T) bool {
	t.Helper()
	out, err := runEtherCATReadOnly(t, "master")
	if err != nil {
		return false // can't tell — let the test proceed and fail naturally
	}
	return strings.Contains(out, "Phase: Idle")
}

func parseEEPROMField(output, pattern string) string {
	re := regexp.MustCompile(pattern)
	m := re.FindStringSubmatch(output)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

func checkEEPROMExpectation(t *testing.T, envName, fieldName, observed string) {
	t.Helper()
	expectedRaw := strings.TrimSpace(os.Getenv(envName))
	if expectedRaw == "" {
		return
	}
	if observed == "" {
		t.Errorf("could not parse %s from ethercat slaves -v output; expected %s=%s", fieldName, envName, expectedRaw)
		return
	}
	expected, err := strconv.ParseUint(strings.TrimPrefix(expectedRaw, "0x"), 16, 64)
	if err != nil {
		// try decimal
		expected, err = strconv.ParseUint(expectedRaw, 10, 64)
		if err != nil {
			t.Fatalf("invalid %s=%q: %v", envName, expectedRaw, err)
		}
	}
	observedVal, err := strconv.ParseUint(strings.TrimPrefix(observed, "0x"), 16, 64)
	if err != nil {
		t.Errorf("could not parse observed %s=%q as hex", fieldName, observed)
		return
	}
	if observedVal != expected {
		t.Errorf("%s (EEPROM) = %s, want 0x%x from %s", fieldName, observed, expected, envName)
	}
}

func orUnknown(s string) string {
	if s == "" {
		return "(not found in output)"
	}
	return s
}

func readIdentitySDOUint32(t *testing.T, cliPath string, timeout time.Duration, position, index, subIndex string) (uint32, string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	args := []string{"upload", "-p", position, "-t", "uint32", index, subIndex}
	cmd := exec.CommandContext(ctx, cliPath, args...)
	out, err := cmd.CombinedOutput()
	raw := strings.TrimSpace(string(out))
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("ethercat %s timed out after %s; output=%q", strings.Join(args, " "), timeout, raw)
	}
	if err != nil {
		if strings.TrimSpace(os.Getenv("ALLOW_NO_ETHERCAT_BUS")) == "1" {
			t.Skipf("ethercat %s failed but ALLOW_NO_ETHERCAT_BUS=1; output=%q error=%v", strings.Join(args, " "), raw, err)
		}
		t.Fatalf("ethercat %s failed: %v; output=%q", strings.Join(args, " "), err, raw)
	}

	value, err := parseLastUint32(raw)
	if err != nil {
		t.Fatalf("could not parse uint32 from ethercat %s output %q: %v", strings.Join(args, " "), raw, err)
	}
	return value, raw
}

func sdoEnv(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func sdoTimeout(t *testing.T) time.Duration {
	t.Helper()
	raw := sdoEnv("HARDWARE_COMMAND_TIMEOUT", sdoEnv("ETHERCAT_COMMAND_TIMEOUT", "5s"))
	d, err := time.ParseDuration(raw)
	if err != nil {
		t.Fatalf("invalid hardware command timeout %q: %v", raw, err)
	}
	return d
}

func parseExpectedUint32(raw string) (uint32, error) {
	value, err := strconv.ParseUint(strings.TrimSpace(raw), 0, 32)
	return uint32(value), err
}

func parseLastUint32(raw string) (uint32, error) {
	matches := regexp.MustCompile(`(?i)0x[0-9a-f]+|\b[0-9]+\b`).FindAllString(raw, -1)
	if len(matches) == 0 {
		return 0, fmt.Errorf("no numeric tokens found")
	}
	for i := len(matches) - 1; i >= 0; i-- {
		value, err := strconv.ParseUint(matches[i], 0, 32)
		if err == nil {
			return uint32(value), nil
		}
	}
	return 0, fmt.Errorf("numeric tokens were not valid uint32 values: %v", matches)
}
