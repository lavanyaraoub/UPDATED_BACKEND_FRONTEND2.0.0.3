package motordriver

import (
	"fmt"
	"os"

	"EtherCAT/helper"

	"gopkg.in/yaml.v2"
)

// MotionConfig contains motion scaling loaded from the selected drive YAML.
// These values must match the installed motor, gearing and machine setup.
type MotionConfig struct {
	RPMConst    int `yaml:"rpm_const"`
	DriveXRatio int `yaml:"drive_x_ratio"`
}

// motionConfigFile reads only the motion section of a drive YAML.
type motionConfigFile struct {
	Motion MotionConfig `yaml:"motion"`
}

// ParseMotionConfig loads the motion parameters from a drive YAML.
// Missing or zero values return an error.
func ParseMotionConfig(filePath string) (MotionConfig, error) {
	fullPath := helper.AppendWDPath(filePath)

	data, err := os.ReadFile(fullPath)
	if err != nil {
		return MotionConfig{}, fmt.Errorf(
			"ParseMotionConfig: cannot read %s: %w",
			fullPath, err,
		)
	}

	var cf motionConfigFile
	if err := yaml.Unmarshal(data, &cf); err != nil {
		return MotionConfig{}, fmt.Errorf(
			"ParseMotionConfig: cannot parse %s: %w",
			fullPath, err,
		)
	}

	if cf.Motion.RPMConst == 0 || cf.Motion.DriveXRatio == 0 {
		return cf.Motion, fmt.Errorf(
			"ParseMotionConfig: %s is missing motion: section "+
				"or has zero values (rpm_const=%d drive_x_ratio=%d)",
			filePath,
			cf.Motion.RPMConst,
			cf.Motion.DriveXRatio,
		)
	}

	return cf.Motion, nil
}

// AutoDiscoveryProfile describes the selected software drive profile.
type AutoDiscoveryProfile struct {
	DriveType         string
	AddressConfigName string
	AddressConfigFile string
	Motion            MotionConfig
}

// IdentifyDrive selects one configured profile per vendor.
//
// Product code is retained in the function signature for compatibility
// with existing callers and for diagnostic messages. It is not used
// to select or reject a profile.
//
// InitMaster must continue copying the actual scanned vendor ID and
// product code into the device configuration.
//
// Vendor-only selection does not establish that every product from
// that vendor is compatible with the selected profile.
func IdentifyDrive(
	vendorID, productCode uint32,
) (AutoDiscoveryProfile, error) {
	var profile AutoDiscoveryProfile

	switch vendorID {
	// Panasonic: fixed A6 profile.
	// For a BF installation, change AddressConfigName and
	// AddressConfigFile below to a6minas_bf.
	// case 0x0000066F:
	// 	profile = AutoDiscoveryProfile{
	// 		DriveType:         "a6_minas",
	// 		AddressConfigName: "a6minas",
	// 		AddressConfigFile: "/configs/a6minas.yml",
	// 	}
	
	// Panasonic — vendor-only matching, using BF configuration.
	case 0x0000066F:
		profile = AutoDiscoveryProfile{
			DriveType:         "a6_minas",
			AddressConfigName: "a6minas_bf",
			AddressConfigFile: "/configs/a6minas_bf.yml",
		}

	// Delta: fixed ASDA-A2-E profile.
	case 0x000001DD:
		profile = AutoDiscoveryProfile{
			DriveType:         "delta_asda2e",
			AddressConfigName: "delta_asda2e",
			AddressConfigFile: "/configs/delta_asda2e.yml",
		}

	// Nidec / Control Techniques: fixed M700 profile.
	case 0x000000F9:
		profile = AutoDiscoveryProfile{
			DriveType:         "Nidec",
			AddressConfigName: "M700",
			AddressConfigFile: "/configs/M700.yml",
		}

	// Veichi: fixed SD710 profile.
	case 0x00850104:
		profile = AutoDiscoveryProfile{
			DriveType:         "sd710",
			AddressConfigName: "sd710",
			AddressConfigFile: "/configs/sd710.yml",
		}

	default:
		return AutoDiscoveryProfile{}, fmt.Errorf(
			"IdentifyDrive: unsupported Vendor=0x%08X "+
				"Product=0x%08X; register a compatible vendor profile "+
				"in auto_discovery.go",
			vendorID, productCode,
		)
	}

	mc, err := ParseMotionConfig(profile.AddressConfigFile)
	if err != nil {
		return profile, fmt.Errorf(
			"IdentifyDrive: profile %s selected for "+
				"Vendor=0x%08X Product=0x%08X, "+
				"but motion config failed: %w",
			profile.AddressConfigName,
			vendorID,
			productCode,
			err,
		)
	}

	profile.Motion = mc
	return profile, nil
}