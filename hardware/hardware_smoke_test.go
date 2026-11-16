//go:build hardware

package hardware_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const defaultCommandTimeout = 5 * time.Second

func TestMain(m *testing.M) {
	if os.Getenv("ALLOW_HARDWARE_TESTS") != "1" {
		fmt.Fprintln(os.Stderr, "hardware tests refused: set ALLOW_HARDWARE_TESTS=1 to run hardware tests")
		os.Exit(1)
	}
	// ALLOW_MOTION_TESTS=1 is handled per-test via requireMotionTestsOptIn /
	// requireHardwareSmokeOptIn. TestMain no longer blocks at the package level
	// so that motion tests and read-only smoke tests can coexist in the same
	// package and be filtered with -run.
	os.Exit(m.Run())
}

func TestHardwareSmoke_ReadOnlySafetyContract(t *testing.T) {
	if os.Getenv("ALLOW_HARDWARE_TESTS") != "1" {
		t.Fatalf("ALLOW_HARDWARE_TESTS must be 1")
	}
	if os.Getenv("ALLOW_MOTION_TESTS") == "1" {
		t.Fatalf("read-only hardware smoke tests must not run with ALLOW_MOTION_TESTS=1")
	}

	t.Log("read-only smoke suite: no PDO writes, no motor enable, no motion commands")
}

func TestHardwareSmoke_ConfigFilesReadable(t *testing.T) {
	configDir := strings.TrimSpace(os.Getenv("CONFIG_DIR"))
	if configDir == "" {
		configDir = "configs"
	}

	required := []string{
		"device-configuration.yml",
		"execution.yml",
		"a6minas.yml",
	}

	if raw := os.Getenv("HARDWARE_REQUIRED_CONFIGS"); strings.TrimSpace(raw) != "" {
		required = nil
		for _, part := range strings.Split(raw, ",") {
			name := strings.TrimSpace(part)
			if name != "" {
				required = append(required, name)
			}
		}
	}

	for _, name := range required {
		path := filepath.Join(configDir, name)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("required config file %q is not readable: %v", path, err)
		}
		t.Logf("config file %q is readable", path)
	}
}

func TestHardwareSmoke_EtherCATCLIExists(t *testing.T) {
	cli := ethercatCLI()
	path, err := exec.LookPath(cli)
	if err != nil {
		t.Fatalf("EtherCAT CLI %q not found in PATH; set ETHERCAT_CLI=/path/to/ethercat if needed", cli)
	}
	t.Logf("using EtherCAT CLI: %s", path)
}

func TestHardwareSmoke_NetworkInterfaceExists(t *testing.T) {
	ifaceName := strings.TrimSpace(os.Getenv("ETHERCAT_INTERFACE"))
	if ifaceName == "" {
		t.Skip("ETHERCAT_INTERFACE is not set; set it to eth0/enp*/etc. to validate the hardware interface")
	}

	iface, err := net.InterfaceByName(ifaceName)
	if err != nil {
		t.Fatalf("EtherCAT network interface %q not found: %v", ifaceName, err)
	}
	if iface.Flags&net.FlagUp == 0 {
		t.Fatalf("EtherCAT network interface %q exists but is not UP; flags=%s", ifaceName, iface.Flags.String())
	}

	t.Logf("EtherCAT interface %q is present and UP; flags=%s", iface.Name, iface.Flags.String())
}

func TestHardwareSmoke_EtherCATMasterReadOnly(t *testing.T) {
	out, err := runEtherCATReadOnly(t, "master")
	if err != nil {
		if os.Getenv("ALLOW_NO_ETHERCAT_BUS") == "1" {
			t.Skipf("ethercat master failed and ALLOW_NO_ETHERCAT_BUS=1: %v\n%s", err, out)
		}
		t.Fatalf("ethercat master failed: %v\n%s", err, out)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatalf("ethercat master returned empty output")
	}
	t.Logf("ethercat master output:\n%s", out)
}

func TestHardwareSmoke_EtherCATSlavesReadOnly(t *testing.T) {
	out, err := runEtherCATReadOnly(t, "slaves")
	if err != nil {
		if os.Getenv("ALLOW_NO_ETHERCAT_BUS") == "1" {
			t.Skipf("ethercat slaves failed and ALLOW_NO_ETHERCAT_BUS=1: %v\n%s", err, out)
		}
		t.Fatalf("ethercat slaves failed: %v\n%s", err, out)
	}

	actual := countNonEmptyLines(out)
	expectedText := strings.TrimSpace(os.Getenv("EXPECTED_ETHERCAT_SLAVES"))
	if expectedText == "" {
		t.Logf("EXPECTED_ETHERCAT_SLAVES not set; detected %d slave line(s)", actual)
		return
	}

	expected, err := strconv.Atoi(expectedText)
	if err != nil {
		t.Fatalf("EXPECTED_ETHERCAT_SLAVES=%q is not an integer", expectedText)
	}
	if actual != expected {
		t.Fatalf("detected %d EtherCAT slave line(s), want %d\n%s", actual, expected, out)
	}
	t.Logf("detected expected EtherCAT slave count: %d", actual)
}

func configFilesFromEnv() []string {
	value := strings.TrimSpace(os.Getenv("HARDWARE_CONFIG_FILES"))
	if value == "" {
		return []string{
			"configs/device-configuration.yml",
			"configs/execution.yml",
			"configs/a6minas.yml",
		}
	}
	return strings.Split(value, ",")
}

func ethercatCLI() string {
	cli := strings.TrimSpace(os.Getenv("ETHERCAT_CLI"))
	if cli == "" {
		return "ethercat"
	}
	return cli
}

func runEtherCATReadOnly(t *testing.T, args ...string) (string, error) {
	t.Helper()

	cli := ethercatCLI()
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, cli, args...)
	output, err := cmd.CombinedOutput()
	out := string(output)
	if ctx.Err() == context.DeadlineExceeded {
		return out, fmt.Errorf("%s %s timed out after %s", cli, strings.Join(args, " "), commandTimeout())
	}
	return out, err
}

func commandTimeout() time.Duration {
	value := strings.TrimSpace(os.Getenv("HARDWARE_COMMAND_TIMEOUT"))
	if value == "" {
		return defaultCommandTimeout
	}

	d, err := time.ParseDuration(value)
	if err == nil && d > 0 {
		return d
	}
	return defaultCommandTimeout
}

func countNonEmptyLines(text string) int {
	count := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}
