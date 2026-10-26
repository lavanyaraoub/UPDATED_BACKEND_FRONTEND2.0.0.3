package motordriver

/*
#include <ecrt.h>
#include "ethercatinterface.h"
*/
import "C"

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"
)

// RunBusScan is the implementation of the --scan-bus CLI command.
//
// It requests the EtherCAT master, scans the bus, and prints two things:
//
//  1. A human-readable table of every slave found:
//     Pos  VendorID    ProductCode  Name
//     0    0x000001DD  0x10305070   Delta ASDA-A2-E
//
//  2. A ready-to-paste device-configuration.yml block for each slave,
//     with mechanical parameters marked as # FILL IN so the operator
//     knows exactly what needs manual input.
//
// Usage:
//
//	./EtherCAT --scan-bus
//
// The application exits immediately after printing — it does not start
// the full motor control stack. Safe to run while the drives are powered
// but no motion program is running.
func RunBusScan() {
	fmt.Println("================================================")
	fmt.Println(" EtherCAT Bus Scanner")
	fmt.Println(" Scanning for drives — please wait...")
	fmt.Println("================================================")
	fmt.Println()

	// ── Request master ────────────────────────────────────────────────────
	// We request master index 0 directly (same as InitMaster does).
	// This is safe before activation and does not affect any running session.
	master := C.ecrt_request_master(0)
	if master == nil {
		fmt.Fprintln(os.Stderr, "ERROR: Could not request EtherCAT master.")
		fmt.Fprintln(os.Stderr, "  Check: is the IgH kernel module loaded? (lsmod | grep ec_master)")
		fmt.Fprintln(os.Stderr, "  Check: is another process holding the master?")
		os.Exit(1)
	}
	defer C.ecrt_release_master(master)

	// Give the master a moment to enumerate slaves after acquisition.
	time.Sleep(200 * time.Millisecond)

	// ── Scan the bus ──────────────────────────────────────────────────────
	slaves, err := ScanBus(master)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR: Bus scan failed:", err)
		os.Exit(1)
	}

	if len(slaves) == 0 {
		fmt.Println("No EtherCAT slaves found.")
		fmt.Println()
		fmt.Println("Check:")
		fmt.Println("  - EtherCAT cable is plugged in")
		fmt.Println("  - All drives are powered on")
		fmt.Println("  - Drives are in EtherCAT mode (not RS485/CANopen)")
		return
	}

	// ── Print summary table ───────────────────────────────────────────────
	fmt.Printf("Found %d slave(s) on the bus:\n\n", len(slaves))

	w := tabwriter.NewWriter(os.Stdout, 1, 1, 3, ' ', 0)
	fmt.Fprintln(w, "Pos\tAlias\tVendorID\tProductCode\tName")
	fmt.Fprintln(w, "---\t-----\t--------\t-----------\t----")
	for _, s := range slaves {
		name := s.Name
		if name == "" {
			name = "(no ESI name)"
		}
		fmt.Fprintf(w, "%d\t%d\t0x%08X\t0x%08X\t%s\n",
			s.Position, s.Alias, s.VendorID, s.ProductCode, name)
	}
	w.Flush()

	// ── Print device-configuration.yml blocks ─────────────────────────────
	fmt.Println()
	fmt.Println("================================================")
	fmt.Println(" Generated device-configuration.yml entries")
	fmt.Println(" Copy the blocks below into your config file.")
	fmt.Println(" Fields marked '# FILL IN' need manual values.")
	fmt.Println("================================================")
	fmt.Println()

	for _, s := range slaves {
		block := generateDeviceBlock(s)
		fmt.Println(block)
		fmt.Println()
	}

	// ── Print YAML template reminder ──────────────────────────────────────
	fmt.Println("================================================")
	fmt.Println(" Next steps:")
	fmt.Println()
	fmt.Println(" 1. Copy the block(s) above into:")
	fmt.Println("    /configs/device-configuration.yml")
	fmt.Println()
	fmt.Println(" 2. Fill in the # FILL IN fields:")
	fmt.Println("    name        — axis label (A, B, C ...)")
	fmt.Println("    rpm-const   — RPMConst × JogFeed = encoder counts/sec at full speed")
	fmt.Println("    drive-x-ratio — encoder counts per motor revolution")
	fmt.Println()
	fmt.Println(" 3. Create the drive YAML config file:")
	fmt.Println("    /configs/<drive-type>.yml")
	fmt.Println("    (PDO layout from drive ESI + SDO ops from drive manual)")
	fmt.Println()
	fmt.Println(" 4. Create the driver Go file:")
	fmt.Println("    motordriver/<drive-type>_motor_driver.go")
	fmt.Println("    (implement IMotorDriver interface)")
	fmt.Println("================================================")
}

// generateDeviceBlock produces a ready-to-paste device-configuration.yml
// entry for one discovered slave. Fields that require human knowledge are
// marked with # FILL IN comments.
func generateDeviceBlock(s SlaveOnBus) string {
	// Derive a safe drive-type string from the ESI name.
	// e.g. "Delta ASDA-A2-E CoE Drive" → "delta_asda_a2_e"
	driveType := deriveDriveType(s)
	configName := driveType
	configFile := fmt.Sprintf("/configs/%s.yml", driveType)

	var b strings.Builder
	b.WriteString(fmt.Sprintf("    # Slave at ring position %d — %s\n", s.Position, s.Name))
	b.WriteString("    - device:\n")
	b.WriteString("      name: # FILL IN — axis label, e.g. A or B\n")
	b.WriteString(fmt.Sprintf("      vendor-id: 0x%08X\n", s.VendorID))
	b.WriteString(fmt.Sprintf("      product-code: 0x%08X\n", s.ProductCode))
	b.WriteString("      rpm-const: # FILL IN — RPMConst for this drive/gearing\n")
	b.WriteString("      drive-x-ratio: # FILL IN — encoder counts per motor revolution\n")
	b.WriteString(fmt.Sprintf("      alias: %d\n", s.Alias))
	b.WriteString(fmt.Sprintf("      id: %d\n", s.Position))
	b.WriteString(fmt.Sprintf("      address-config-name: %s\n", configName))
	b.WriteString(fmt.Sprintf("      address-config-file: \"%s\"\n", configFile))
	b.WriteString(fmt.Sprintf("      drive-type: %s\n", driveType))
	b.WriteString("      pot-not-threshold: 1.5\n")
	b.WriteString("      stop-when-hardware-potnot: true\n")
	b.WriteString("      io-poll-interval: 10000\n")
	return b.String()
}

// deriveDriveType converts a drive's ESI name into a safe Go/YAML identifier.
// Falls back to vendor_product if the name is empty or unrecognisable.
//
// Examples:
//
//	"Delta Electronics ASDA-A2-E CoE Drive" → "delta_asda_a2_e"
//	"Panasonic MINAS A6"                    → "panasonic_minas_a6"
//	""                                      → "vendor_000001dd_prod_10305070"
func deriveDriveType(s SlaveOnBus) string {
	if s.Name == "" {
		return fmt.Sprintf("vendor_%08x_prod_%08x",
			s.VendorID, s.ProductCode)
	}

	// Known vendor ID → canonical prefix mapping
	// This avoids generic names like "ethercat_servo" for known brands.
	knownPrefixes := map[uint32]string{
		0x000001DD: "delta",
		0x0000066F: "a6_minas",
		0x00000002: "beckhoff",
		0x00000539: "yaskawa",
		0x00000046: "omron",
		0x0000006B: "schneider",
	}

	name := s.Name

	// Use known prefix if vendor is recognised
	if prefix, ok := knownPrefixes[s.VendorID]; ok {
		// Strip the vendor name from the beginning of the ESI name
		// to avoid "delta_delta_asda_a2_e"
		for _, strip := range []string{
			"Delta Electronics", "Delta", "Panasonic",
			"Beckhoff Automation GmbH", "Beckhoff",
			"Yaskawa", "Omron", "Schneider",
		} {
			if strings.HasPrefix(strings.ToLower(name), strings.ToLower(strip)) {
				name = strings.TrimSpace(name[len(strip):])
				break
			}
		}
		name = prefix + "_" + name
	}

	// Sanitise: lower case, replace non-alphanumeric with underscore
	var safe strings.Builder
	prev := '_'
	for _, ch := range strings.ToLower(name) {
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') {
			safe.WriteRune(ch)
			prev = ch
		} else if prev != '_' {
			safe.WriteRune('_')
			prev = '_'
		}
	}

	result := strings.Trim(safe.String(), "_")
	if result == "" {
		return fmt.Sprintf("vendor_%08x_prod_%08x", s.VendorID, s.ProductCode)
	}
	return result
}