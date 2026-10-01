package motordriver

import (
	"fmt"
	"os"

	"EtherCAT/helper"

	"gopkg.in/yaml.v2"
)

// MotionConfig holds drive-hardware motion parameters loaded from
// the drive YAML motion: section.
//
// These are drive-type facts — they never change for a given drive model.
// They live in the drive YAML (e.g. a6minas.yml) and are injected into
// Device at startup by auto-discovery, so device-configuration.yml never
// needs to contain them.
type MotionConfig struct {
	// RPMConst converts JogFeed (from settings.json) to a raw velocity
	// count for the drive. Formula: targetVel = RPMConst × JogFeed.
	// A6 Minas: 120000.006  (20000 counts/rev × 6 rev/s per RPM unit)
	// Delta ASDA2E: 10.0
	RPMConst int `yaml:"rpm_const"`

	// DriveXRatio is the number of encoder counts per output shaft revolution
	// (after the gearbox). Used to convert degrees → pulse count.
	// A6 Minas: 20000  (20-bit encoder, 1:1 to output)
	// Delta ASDA2E: 10000
	DriveXRatio int `yaml:"drive_x_ratio"`
}

// motionConfigFile mirrors only the motion: block of a drive YAML.
// Kept unexported — only ParseMotionConfig uses it.
type motionConfigFile struct {
	Motion MotionConfig `yaml:"motion"`
}

// ParseMotionConfig reads the motion: section from a drive YAML file and
// returns the populated MotionConfig.
//
// Called during auto-discovery (before ecrt_master_activate) so the values
// are available for the entire InitMaster sequence.
//
// If the motion: section is absent the returned MotionConfig will have zero
// values. The caller logs a warning and falls back to safe defaults.
func ParseMotionConfig(filePath string) (MotionConfig, error) {
	fullPath := helper.AppendWDPath(filePath)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return MotionConfig{}, fmt.Errorf("ParseMotionConfig: cannot read %s: %w", fullPath, err)
	}

	var cf motionConfigFile
	if err := yaml.Unmarshal(data, &cf); err != nil {
		return MotionConfig{}, fmt.Errorf("ParseMotionConfig: cannot parse %s: %w", fullPath, err)
	}

	if cf.Motion.RPMConst == 0 || cf.Motion.DriveXRatio == 0 {
		return cf.Motion, fmt.Errorf(
			"ParseMotionConfig: %s is missing motion: section or has zero values (rpm_const=%d drive_x_ratio=%d)",
			filePath, cf.Motion.RPMConst, cf.Motion.DriveXRatio)
	}

	return cf.Motion, nil
}

// AutoDiscoveryProfile holds everything the system needs to know about
// a detected drive — identity, config file paths, and motion parameters.
// All fields are injected into Device during InitMaster auto-discovery.
type AutoDiscoveryProfile struct {
	DriveType         string
	AddressConfigName string
	AddressConfigFile string
	Motion            MotionConfig // populated by ParseMotionConfig at discovery time
}

// IdentifyDrive matches physical bus hardware (vendor ID + product code)
// to a software drive profile.
//
// Called once per axis during InitMaster after ScanBus().
// Returns a fully populated AutoDiscoveryProfile including motion params
// loaded from the drive YAML — so the caller only needs to call this one
// function to get everything needed for that drive.
//
// To add a new drive:
//  1. Add its vendor/product code block here.
//  2. Create configs/newdrive.yml with ethercat:, pdo:, and motion: sections.
//  3. Add the Go driver file and register in motor_driver_factory.go.
//     No other files need changing.
func IdentifyDrive(vendorID, productCode uint32) (AutoDiscoveryProfile, error) {
	var profile AutoDiscoveryProfile

	switch {
	// ── 1. Panasonic MINAS A6 E-series ──────────────────────────────────
	case vendorID == 0x0000066F && productCode == 0x60380008:
		profile = AutoDiscoveryProfile{
			DriveType:         "a6_minas",
			AddressConfigName: "a6minas",
			AddressConfigFile: "/configs/a6minas.yml",
		}

	// ── 1b. Panasonic MINAS A6 BF-series (F-series, 17-bit, 1:90) ───────
	// Product code 0x613c0008 distinguishes the BF/F model from the E-series
	// (0x60380008). Both use the same CiA-402 profile and Go driver (A6Minas),
	// but the BF model has different motion parameters (rpm_const, drive_x_ratio)
	// due to its 17-bit encoder and 1:90 gearbox ratio.
	case vendorID == 0x0000066F && (productCode == 0x613c0008||
		productCode == 0x613c0017 || productCode == 0x60380009):
		profile = AutoDiscoveryProfile{
			DriveType:         "a6_minas",
			AddressConfigName: "a6minas_bf",
			AddressConfigFile: "/configs/a6minas_bf.yml",
		}
	// ── 2. Delta ASDA-A2-E ────────────────────────────────────────────────
	case vendorID == 0x000001DD && productCode == 0x10305070:
		profile = AutoDiscoveryProfile{
			DriveType:         "delta_asda2e",
			AddressConfigName: "delta_asda2e",
			AddressConfigFile: "/configs/delta_asda2e.yml",
		}

	// ── 3. Nidec / Control Techniques M700 ───────────────────────────────
	// Product code varies by operating mode — all map to the same profile.
	case vendorID == 0x000000F9 && (productCode == 0x01000102 ||
		productCode == 0x01010102 ||
		productCode == 0x01020102 ||
		productCode == 0x01030102 ||
		productCode == 0x01040102):
		profile = AutoDiscoveryProfile{
			DriveType:         "Nidec",
			AddressConfigName: "M700",
			AddressConfigFile: "/configs/M700.yml",
		}

	// ── 4. Veichi SD700 ──────────────────────────────────────────────────
	case vendorID == 0x00850104 && productCode == 0x01030507:
		profile = AutoDiscoveryProfile{
			DriveType:         "veichi_sd700",
			AddressConfigName: "veichi_sd700",
			AddressConfigFile: "/configs/veichi_sd700.yml",
		}

	// ── 5. Future drives: add new case blocks here ────────────────────────

	default:
		return AutoDiscoveryProfile{}, fmt.Errorf(
			"IdentifyDrive: unrecognized hardware Vendor=0x%08X Product=0x%08X — "+
				"add this drive to auto_discovery.go and create its config YAML",
			vendorID, productCode)
	}

	// Load motion parameters from the drive YAML.
	// This is done here so the caller (InitMaster) gets everything in one call.
	mc, err := ParseMotionConfig(profile.AddressConfigFile)
	if err != nil {
		// Non-fatal: log and return defaults so the system can still start.
		// The caller will see RPMConst=0 / DriveXRatio=0 and can decide
		// whether to abort or use fallback values.
		return profile, fmt.Errorf(
			"IdentifyDrive: drive %s identified but motion config failed: %w — "+
				"add a motion: section to %s",
			profile.DriveType, err, profile.AddressConfigFile)
	}
	profile.Motion = mc
	return profile, nil
}
