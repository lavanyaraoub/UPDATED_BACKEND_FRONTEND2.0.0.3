//go:build unit

package motordriver

// Unit tests for auto_discovery.go.
//
// This file has NO cgo dependency of its own (no `import "C"`), but it lives
// in package motordriver alongside files that do, so the whole package still
// needs the IgH EtherCAT toolchain to build — same as every other test file
// here. Nothing below touches real hardware; IdentifyDrive is pure vendor/
// product-code matching and ParseMotionConfig only touches the filesystem.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"EtherCAT/helper"
)

// ─── IdentifyDrive — known drives ─────────────────────────────────────────────

func TestIdentifyDrive_A6Minas_MatchesVendorAndProduct(t *testing.T) {
	profile, err := IdentifyDrive(0x0000066F, 0x60380008)
	// ParseMotionConfig will fail (no /configs/a6minas.yml on this machine),
	// but the identity fields must still be populated before that happens.
	if profile.DriveType != "a6_minas" {
		t.Errorf("DriveType = %q, want a6_minas", profile.DriveType)
	}
	if profile.AddressConfigName != "a6minas" {
		t.Errorf("AddressConfigName = %q, want a6minas", profile.AddressConfigName)
	}
	if profile.AddressConfigFile != "/configs/a6minas.yml" {
		t.Errorf("AddressConfigFile = %q, want /configs/a6minas.yml", profile.AddressConfigFile)
	}
	// Motion config load will realistically fail in a test sandbox — that's
	// a separate, already-documented behavior (see IdentifyDrive_MotionConfigFails).
	_ = err
}

func TestIdentifyDrive_DeltaASDA2E_MatchesVendorAndProduct(t *testing.T) {
	profile, _ := IdentifyDrive(0x000001DD, 0x10305070)
	if profile.DriveType != "delta_asda2e" {
		t.Errorf("DriveType = %q, want delta_asda2e", profile.DriveType)
	}
	if profile.AddressConfigName != "delta_asda2e" {
		t.Errorf("AddressConfigName = %q, want delta_asda2e", profile.AddressConfigName)
	}
	if profile.AddressConfigFile != "/configs/delta_asda2e.yml" {
		t.Errorf("AddressConfigFile = %q, want /configs/delta_asda2e.yml", profile.AddressConfigFile)
	}
}

func TestIdentifyDrive_NidecM700_AllFiveProductCodesMatch(t *testing.T) {
	codes := []uint32{0x01000102, 0x01010102, 0x01020102, 0x01030102, 0x01040102}
	for _, code := range codes {
		profile, _ := IdentifyDrive(0x000000F9, code)
		if profile.DriveType != "Nidec" {
			t.Errorf("productCode=0x%08X: DriveType = %q, want Nidec", code, profile.DriveType)
		}
		if profile.AddressConfigName != "M700" {
			t.Errorf("productCode=0x%08X: AddressConfigName = %q, want M700", code, profile.AddressConfigName)
		}
		if profile.AddressConfigFile != "/configs/M700.yml" {
			t.Errorf("productCode=0x%08X: AddressConfigFile = %q, want /configs/M700.yml", code, profile.AddressConfigFile)
		}
	}
}

// ─── IdentifyDrive — unknown / wrong mappings ─────────────────────────────────

func TestIdentifyDrive_UnknownVendorID_ReturnsError(t *testing.T) {
	_, err := IdentifyDrive(0xDEADBEEF, 0x60380008)
	if err == nil {
		t.Fatal("unknown vendor ID: expected error, got nil")
	}
}

func TestIdentifyDrive_KnownVendorWrongProductCode_ReturnsError(t *testing.T) {
	// Correct A6 vendor ID, but a product code that belongs to no known drive.
	_, err := IdentifyDrive(0x0000066F, 0x00000000)
	if err == nil {
		t.Fatal("known vendor + wrong product: expected error, got nil")
	}
}

func TestIdentifyDrive_NidecVendorWithUnlistedProductCode_ReturnsError(t *testing.T) {
	// Nidec vendor ID is correct, but 0x01050102 is not one of the five
	// enumerated operating-mode product codes.
	_, err := IdentifyDrive(0x000000F9, 0x01050102)
	if err == nil {
		t.Fatal("Nidec vendor + unlisted product code: expected error, got nil")
	}
}

func TestIdentifyDrive_ZeroVendorAndProduct_ReturnsError(t *testing.T) {
	_, err := IdentifyDrive(0, 0)
	if err == nil {
		t.Fatal("zero vendor/product: expected error, got nil")
	}
}

func TestIdentifyDrive_ErrorMessageIncludesHexCodes(t *testing.T) {
	_, err := IdentifyDrive(0x12345678, 0x9ABCDEF0)
	if err == nil {
		t.Fatal("expected error for unrecognized hardware")
	}
	msg := err.Error()
	if !strings.Contains(msg, "12345678") || !strings.Contains(msg, "9ABCDEF0") {
		t.Errorf("error message %q should include both hex codes", msg)
	}
}

// ─── IdentifyDrive — motion config failure is non-fatal to identity ──────────

func TestIdentifyDrive_MotionConfigMissing_StillReturnsIdentityWithError(t *testing.T) {
	// On a machine without /configs/a6minas.yml, IdentifyDrive should still
	// report the correct DriveType/AddressConfigName — only the motion
	// section load fails. The caller decides whether that's fatal.
	profile, err := IdentifyDrive(0x0000066F, 0x60380008)
	if profile.DriveType != "a6_minas" {
		t.Errorf("DriveType should be populated even when motion config fails, got %q", profile.DriveType)
	}
	if err == nil {
		// If a real /configs/a6minas.yml with a valid motion: section exists
		// on this machine, that's fine too — just confirm the motion values
		// look sane rather than failing the test.
		if profile.Motion.RPMConst == 0 || profile.Motion.DriveXRatio == 0 {
			t.Errorf("motion config succeeded but has zero values: %+v", profile.Motion)
		}
	}
}

// ─── ParseMotionConfig ────────────────────────────────────────────────────────
//
// ParseMotionConfig calls helper.AppendWDPath(filePath), which is a PLAIN
// STRING CONCATENATION (execPath + filePath), not filepath.Join. Passing an
// arbitrary absolute t.TempDir() path here does not work: the function
// silently looks for execPath+thatAbsolutePath, which never exists.
//
// writeExecRelativeYAML writes content to a uniquely-named file inside the
// test binary's own exec directory (the same directory AppendWDPath("")
// resolves to) and returns the "/name" suffix to pass into ParseMotionConfig
// so that execPath + suffix reconstructs the real file path.

func writeExecRelativeYAML(t *testing.T, content string) string {
	t.Helper()
	execDir := helper.AppendWDPath("")
	f, err := os.CreateTemp(execDir, "motionconfig-*.yml")
	if err != nil {
		t.Fatalf("failed to create test file under exec dir %q: %v", execDir, err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	name := filepath.Base(f.Name())
	t.Cleanup(func() { os.Remove(f.Name()) })
	return "/" + name
}

func TestParseMotionConfig_ValidFile_ReturnsPopulatedConfig(t *testing.T) {
	suffix := writeExecRelativeYAML(t, "motion:\n  rpm_const: 120000\n  drive_x_ratio: 20000\n")

	mc, err := ParseMotionConfig(suffix)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mc.RPMConst != 120000 {
		t.Errorf("RPMConst = %d, want 120000", mc.RPMConst)
	}
	if mc.DriveXRatio != 20000 {
		t.Errorf("DriveXRatio = %d, want 20000", mc.DriveXRatio)
	}
}

func TestParseMotionConfig_MissingFile_ReturnsError(t *testing.T) {
	_, err := ParseMotionConfig("/nonexistent/path/nope.yml")
	if err == nil {
		t.Fatal("missing file: expected error, got nil")
	}
}

func TestParseMotionConfig_MissingMotionSection_ReturnsErrorWithZeroValues(t *testing.T) {
	suffix := writeExecRelativeYAML(t, "ethercat:\n  operations: []\n")

	mc, err := ParseMotionConfig(suffix)
	if err == nil {
		t.Fatal("missing motion section: expected error, got nil")
	}
	if mc.RPMConst != 0 || mc.DriveXRatio != 0 {
		t.Errorf("expected zero-value MotionConfig, got %+v", mc)
	}
}

func TestParseMotionConfig_ZeroRPMConst_ReturnsError(t *testing.T) {
	suffix := writeExecRelativeYAML(t, "motion:\n  rpm_const: 0\n  drive_x_ratio: 20000\n")

	_, err := ParseMotionConfig(suffix)
	if err == nil {
		t.Fatal("zero rpm_const: expected error, got nil")
	}
}

func TestParseMotionConfig_ZeroDriveXRatio_ReturnsError(t *testing.T) {
	suffix := writeExecRelativeYAML(t, "motion:\n  rpm_const: 10\n  drive_x_ratio: 0\n")

	_, err := ParseMotionConfig(suffix)
	if err == nil {
		t.Fatal("zero drive_x_ratio: expected error, got nil")
	}
}

func TestParseMotionConfig_MalformedYAML_ReturnsError(t *testing.T) {
	suffix := writeExecRelativeYAML(t, "motion: [this is not: a valid: map")

	_, err := ParseMotionConfig(suffix)
	if err == nil {
		t.Fatal("malformed YAML: expected error, got nil")
	}
}

func TestParseMotionConfig_DeltaLikeValues(t *testing.T) {
	suffix := writeExecRelativeYAML(t, "motion:\n  rpm_const: 10\n  drive_x_ratio: 10000\n")

	mc, err := ParseMotionConfig(suffix)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mc.RPMConst != 10 || mc.DriveXRatio != 10000 {
		t.Errorf("got %+v, want RPMConst=10 DriveXRatio=10000", mc)
	}
}
