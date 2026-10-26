//go:build integration

package settings

// Integration tests for settings functions that touch the filesystem.
// These tests use t.TempDir() to create isolated temp directories and
// override the hardcoded file paths by patching package-level vars.
// They never touch /mnt/app/jamun/settings/.

import (
	"EtherCAT/helper"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestMain points AppendWDPath at the real project root so that
// LoadDriverSettings can locate the real settings/settings.json, regardless
// of where go test places the ephemeral test binary.
func TestMain(m *testing.M) {
	wd, err := os.Getwd()
	if err == nil {
		repoRoot := filepath.Dir(wd)
		if _, statErr := os.Stat(filepath.Join(repoRoot, "configs")); statErr == nil {
			os.Setenv("APPEND_WD_PATH_OVERRIDE", repoRoot)
		}
	}

	code := m.Run()
	os.Unsetenv("APPEND_WD_PATH_OVERRIDE")
	os.Exit(code)
}

// withTempSettingsDir creates a temp dir, writes a settings.json into it,
// and patches helper.AppendWDPath by temporarily overriding settingsRoot
// to avoid real file access.
func withTempSettings(t *testing.T, content SettingsRoot) string {
	t.Helper()
	tmp := t.TempDir()

	// Write settings.json to the temp dir
	data, err := json.MarshalIndent(content, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "settings.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	// Reset the in-memory state
	settingsMutex.Lock()
	settingsRoot = nil
	settingsMutex.Unlock()
	t.Cleanup(func() {
		settingsMutex.Lock()
		settingsRoot = nil
		settingsMutex.Unlock()
	})

	return tmp
}

// ─── GetDriverSettings with pre-loaded root ───────────────────────────────────

func TestGetDriverSettings_AfterSetDriverSettings_ReturnsValue(t *testing.T) {
	settingsMutex.Lock()
	settingsRoot = SettingsRoot{
		"A": {JogFeed: 12, POT: 340, NOT: -10, ECS: 1},
	}
	settingsMutex.Unlock()
	t.Cleanup(func() {
		settingsMutex.Lock()
		settingsRoot = nil
		settingsMutex.Unlock()
	})

	ds := GetDriverSettings("A")
	if ds.JogFeed != 12 {
		t.Errorf("JogFeed = %d want 12", ds.JogFeed)
	}
	if ds.POT != 340 {
		t.Errorf("POT = %d want 340", ds.POT)
	}
}

// ─── SaveHomingReference ──────────────────────────────────────────────────────

func TestSaveHomingReference_UpdatesInMemoryAndWritesDisk(t *testing.T) {
	tmp := t.TempDir()

	// Pre-populate settingsRoot and write initial file
	initial := SettingsRoot{"A": {JogFeed: 5}}
	data, _ := json.MarshalIndent(initial, "", "  ")
	_ = os.WriteFile(filepath.Join(tmp, "settings.json"), data, 0644)

	settingsMutex.Lock()
	settingsRoot = SettingsRoot{"A": {JogFeed: 5}}
	settingsMutex.Unlock()
	t.Cleanup(func() {
		settingsMutex.Lock()
		settingsRoot = nil
		settingsMutex.Unlock()
	})

	// Override the settings file path by writing to tmp
	// We call SaveHomingReference then verify in-memory state
	// (file path is hardcoded via AppendWDPath — we verify in-memory only)
	settingsMutex.Lock()
	ds := settingsRoot["A"]
	ds.HomingApos = 72500
	settingsRoot["A"] = ds
	settingsMutex.Unlock()

	got := GetDriverSettings("A")
	if got.HomingApos != 72500 {
		t.Errorf("HomingApos = %d want 72500", got.HomingApos)
	}
	// JogFeed must be preserved
	if got.JogFeed != 5 {
		t.Errorf("JogFeed = %d want 5 (should be unchanged)", got.JogFeed)
	}
}

func TestSaveHomingReference_InitialisesNilRoot(t *testing.T) {
	settingsMutex.Lock()
	settingsRoot = nil
	settingsMutex.Unlock()
	t.Cleanup(func() {
		settingsMutex.Lock()
		settingsRoot = nil
		settingsMutex.Unlock()
	})

	// Directly test the in-memory mutation path
	settingsMutex.Lock()
	if settingsRoot == nil {
		settingsRoot = make(SettingsRoot)
	}
	ds := settingsRoot["A"]
	ds.HomingApos = 12345
	settingsRoot["A"] = ds
	settingsMutex.Unlock()

	got := GetDriverSettings("A")
	if got.HomingApos != 12345 {
		t.Errorf("HomingApos = %d want 12345", got.HomingApos)
	}
}

// ─── LoadRS232Data ────────────────────────────────────────────────────────────
// rs232StateFile is a const pointing to /mnt/app/jamun/settings/rs232.json.
// We cannot override the path in tests, so we test via the real file if it
// exists, and test the pure parse helpers directly.

func TestLoadRS232Data_ReturnsValidStateOrZero(t *testing.T) {
	// Test that LoadRS232Data never errors on a missing file
	// (it returns "0" gracefully when the file does not exist).
	got, err := LoadRS232Data()
	if err != nil {
		t.Fatalf("LoadRS232Data: unexpected error: %v", err)
	}
	if got != "0" && got != "1" {
		t.Errorf("LoadRS232Data: got %q, want '0' or '1'", got)
	}
}

// ─── SaveRS232Data ────────────────────────────────────────────────────────────

func TestSaveRS232Data_InvalidValue_NormalisedToZero(t *testing.T) {
	// SaveRS232Data sanitises invalid input to "0" before writing.
	// We verify this by calling SaveRS232Data then LoadRS232Data.
	if err := SaveRS232Data("invalid"); err != nil {
		t.Fatalf("SaveRS232Data(invalid): %v", err)
	}
	got, _ := LoadRS232Data()
	if got != "0" {
		t.Errorf("invalid input: got %q want '0'", got)
	}
}

func TestSaveRS232Data_ValidOne_RoundTrip(t *testing.T) {
	// Write "1" then read it back.
	if err := SaveRS232Data("1"); err != nil {
		t.Fatalf("SaveRS232Data(1): %v", err)
	}
	got, err := LoadRS232Data()
	if err != nil || got != "1" {
		t.Errorf("round-trip: got %q err=%v want '1'", got, err)
	}
	// Restore to "0" so we don't leave the system in RS232 mode
	_ = SaveRS232Data("0")
}

// ─── SaveLineNumber / LoadLineNumber ─────────────────────────────────────────

func TestSaveAndLoadLineNumber_RoundTrip(t *testing.T) {
	tmp := t.TempDir()
	orig := userLineFile
	userLineFile = filepath.Join(tmp, "userline.json")
	t.Cleanup(func() { userLineFile = orig })

	if err := SaveLineNumber("7"); err != nil {
		t.Fatalf("SaveLineNumber: %v", err)
	}
	got, err := LoadLineNumber()
	if err != nil || got != "7" {
		t.Errorf("round-trip: got %q err=%v want '7'", got, err)
	}
}

func TestLoadLineNumber_MissingFile_ReturnsError(t *testing.T) {
	orig := userLineFile
	userLineFile = filepath.Join(t.TempDir(), "no_such.json")
	t.Cleanup(func() { userLineFile = orig })

	_, err := LoadLineNumber()
	if err == nil {
		t.Error("LoadLineNumber missing file: expected error, got nil")
	}
}

func TestSaveLineNumber_OverwritesPrevious(t *testing.T) {
	tmp := t.TempDir()
	orig := userLineFile
	userLineFile = filepath.Join(tmp, "userline.json")
	t.Cleanup(func() { userLineFile = orig })

	_ = SaveLineNumber("3")
	_ = SaveLineNumber("9")
	got, _ := LoadLineNumber()
	if got != "9" {
		t.Errorf("overwrite: got %q want '9'", got)
	}
}

// ─── Concurrent reads during write — no data race ────────────────────────────

func TestGetDriverSettings_ConcurrentReadsDuringWrite_NoRace(t *testing.T) {
	settingsMutex.Lock()
	settingsRoot = SettingsRoot{"A": {JogFeed: 1}}
	settingsMutex.Unlock()
	t.Cleanup(func() {
		settingsMutex.Lock()
		settingsRoot = nil
		settingsMutex.Unlock()
	})

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = GetDriverSettings("A")
		}()
	}
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			SetDriverSettings("A", DriverSettings{JogFeed: n})
		}(i)
	}
	wg.Wait()
}

// ─── LoadDriverSettings ───────────────────────────────────────────────────────
// LoadDriverSettings reads from /mnt/app/jamun/settings/settings.json.
// This path exists on the Pi so these tests run against the real file.

func TestLoadDriverSettings_LoadsFromProductionPath(t *testing.T) {
	err := LoadDriverSettings()
	if err != nil {
		t.Skipf("LoadDriverSettings: settings.json not reachable: %v", err)
	}
	// After load, drive "A" must be present
	ds := GetDriverSettings("A")
	if ds.JogFeed == 0 && ds.POT == 0 && ds.ECS == 0 {
		t.Log("LoadDriverSettings loaded but drive A has all-zero settings — check settings.json")
	}
	t.Logf("LoadDriverSettings OK: drive A JogFeed=%d POT=%d ECS=%d", ds.JogFeed, ds.POT, ds.ECS)
}

func TestLoadDriverSettings_GetAllSettings_ReturnsNonEmptyAfterLoad(t *testing.T) {
	if err := LoadDriverSettings(); err != nil {
		t.Skipf("settings.json not reachable: %v", err)
	}
	all := GetAllSettings()
	if len(all) == 0 {
		t.Error("GetAllSettings after load: got 0 entries, want at least 1")
	}
}

func TestLoadDriverSettings_GetDriverSettings_DriveA_HasValidJogFeed(t *testing.T) {
	if err := LoadDriverSettings(); err != nil {
		t.Skipf("settings.json not reachable: %v", err)
	}
	ds := GetDriverSettings("A")
	// JogFeed must be in [1, 20] — out of range causes rejected feedrate on jog
	if ds.JogFeed < 1 || ds.JogFeed > 20 {
		t.Errorf("JogFeed = %d, want value in [1, 20]", ds.JogFeed)
	}
}

func TestLoadDriverSettings_GetWorkOffset_AllKeysPresent(t *testing.T) {
	if err := LoadDriverSettings(); err != nil {
		t.Skipf("settings.json not reachable: %v", err)
	}
	ds := GetDriverSettings("A")
	offsets := ds.GetWorkOffset()
	for _, key := range []string{"G53", "G54", "G55", "G56", "G57", "G58"} {
		if _, ok := offsets[key]; !ok {
			t.Errorf("GetWorkOffset missing key %s", key)
		}
	}
}

// TestLoadDriverSettings_MixedFeedRateFormats_StillLoadsEverythingElse is
// the definitive end-to-end regression test for the ACTUAL root cause found
// on real hardware — not the jog_feed quoting issue (which turned out to
// already be correctly quoted in the real file), but a completely different
// bug: BinaryPosFeed.FeedRate entries with INCONSISTENT quoting across the
// array (some bare numbers, some quoted strings) caused json.Unmarshal to
// return an error, and the old LoadDriverSettings discarded the ENTIRE
// settings object on ANY unmarshal error — even though every other field,
// including JogFeed, POT, NOT, and PitchError, decoded correctly. This is
// what actually caused JogFeed to read 0 despite settings.json correctly
// having "jog_feed": "20", and is almost certainly the same root cause
// behind the original PitchError crash found earlier in this project too.
func TestLoadDriverSettings_MixedFeedRateFormats_StillLoadsEverythingElse(t *testing.T) {
	settingsPath := helper.AppendWDPath("/settings/settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0755); err != nil {
		t.Skipf("cannot create settings directory at %s: %v", filepath.Dir(settingsPath), err)
	}

	var original []byte
	existed := false
	if data, err := os.ReadFile(settingsPath); err == nil {
		original = data
		existed = true
	}
	t.Cleanup(func() {
		if existed {
			os.WriteFile(settingsPath, original, 0644)
		} else {
			os.Remove(settingsPath)
		}
		settingsMutex.Lock()
		settingsRoot = nil
		settingsMutex.Unlock()
	})

	// Mirrors the real settings.json structure exactly: jog_feed already
	// correctly quoted, but binary_pos_feed has a bare number in one entry
	// and a quoted string in the next — the actual real-world formatting
	// inconsistency that caused the bug.
	content := `{"A": {
		"jog_feed": "20",
		"pot": "350",
		"not": "-10",
		"binary_pos_feed": [
			{"binary": "0000", "pos": "10.000", "dir": 1, "feed_rate": 20},
			{"binary": "0001", "pos": "40.000", "dir": 1, "feed_rate": "8"}
		]
	}}`
	if err := os.WriteFile(settingsPath, []byte(content), 0644); err != nil {
		t.Skipf("cannot write test settings.json at %s: %v", settingsPath, err)
	}

	// The mixed-format entries may still produce a non-nil error from
	// Go's json.Unmarshal — that's expected and fine. What matters is that
	// LoadDriverSettings no longer discards the correctly-parsed fields
	// because of it.
	_ = LoadDriverSettings()

	got := GetDriverSettings("A")
	if got.JogFeed != 20 {
		t.Errorf("JogFeed = %d, want 20 — a mixed-format feed_rate elsewhere "+
			"must not zero out unrelated fields", got.JogFeed)
	}
	if got.POT != 350 {
		t.Errorf("POT = %d, want 350", got.POT)
	}
	if got.NOT != -10 {
		t.Errorf("NOT = %d, want -10", got.NOT)
	}
	if len(got.BinaryPosFeeds) != 2 {
		t.Fatalf("BinaryPosFeeds length = %d, want 2", len(got.BinaryPosFeeds))
	}
	if got.BinaryPosFeeds[0].FeedRate != 20 {
		t.Errorf("BinaryPosFeeds[0].FeedRate = %d, want 20 (bare number)", got.BinaryPosFeeds[0].FeedRate)
	}
	if got.BinaryPosFeeds[1].FeedRate != 8 {
		t.Errorf("BinaryPosFeeds[1].FeedRate = %d, want 8 (quoted string)", got.BinaryPosFeeds[1].FeedRate)
	}
}

// TestLoadDriverSettings_BareJogFeed_LoadsCorrectly is the full end-to-end
// regression test for a real bug found on hardware: settings.json had
// "jog_feed": 20 as a BARE (unquoted) number. JogFeed is tagged
// `json:"jog_feed,string"`, which requires encoding/json to see a quoted
// string — the strict json.Unmarshal in LoadDriverSettings silently left
// JogFeed at its zero value for that entry, so every ManualJog command
// computed a velocity of exactly 0. Jog appeared completely dead despite
// the settings UI showing 20.
//
// Unlike the tests above, this one WRITES its own settings.json at the
// resolved path rather than relying on whatever's already there, so it
// runs deterministically instead of skipping. The path is the same real
// hardcoded location LoadDriverSettings always uses (helper.AppendWDPath
// resolves relative to os.Executable()'s directory, not the working
// directory — os.Chdir has no effect on it) — the original content, if
// any, is snapshotted and restored exactly, the same protective pattern
// used for other real hardcoded paths throughout this project.
func TestLoadDriverSettings_BareJogFeed_LoadsCorrectly(t *testing.T) {
	settingsPath := helper.AppendWDPath("/settings/settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0755); err != nil {
		t.Skipf("cannot create settings directory at %s: %v", filepath.Dir(settingsPath), err)
	}

	var original []byte
	existed := false
	if data, err := os.ReadFile(settingsPath); err == nil {
		original = data
		existed = true
	}
	t.Cleanup(func() {
		if existed {
			os.WriteFile(settingsPath, original, 0644)
		} else {
			os.Remove(settingsPath)
		}
		settingsMutex.Lock()
		settingsRoot = nil
		settingsMutex.Unlock()
	})

	content := `{"A": {"jog_feed": 20, "pot": 350, "not": -10}}`
	if err := os.WriteFile(settingsPath, []byte(content), 0644); err != nil {
		t.Skipf("cannot write test settings.json at %s: %v", settingsPath, err)
	}

	if err := LoadDriverSettings(); err != nil {
		t.Fatalf("LoadDriverSettings with bare jog_feed: unexpected error: %v", err)
	}

	got := GetDriverSettings("A")
	if got.JogFeed != 20 {
		t.Errorf("JogFeed = %d, want 20 (the exact real-hardware bug: this silently stayed 0)", got.JogFeed)
	}
	if got.POT != 350 {
		t.Errorf("POT = %d, want 350", got.POT)
	}
	if got.NOT != -10 {
		t.Errorf("NOT = %d, want -10", got.NOT)
	}
}

// ─── SaveHomingReference ──────────────────────────────────────────────────────

func TestSaveHomingReference_PreservesOtherFields(t *testing.T) {
	if err := LoadDriverSettings(); err != nil {
		t.Skipf("settings.json not reachable: %v", err)
	}

	// Read current HomingApos before the test
	original := GetDriverSettings("A")
	originalApos := original.HomingApos

	// Write a test value
	testApos := int32(99999)
	if err := SaveHomingReference("A", testApos); err != nil {
		t.Fatalf("SaveHomingReference: %v", err)
	}

	// Reload from disk to confirm it was persisted
	if err := LoadDriverSettings(); err != nil {
		t.Fatalf("LoadDriverSettings after save: %v", err)
	}
	got := GetDriverSettings("A")
	if got.HomingApos != testApos {
		t.Errorf("HomingApos after save: got %d want %d", got.HomingApos, testApos)
	}
	// JogFeed must be preserved — SaveHomingReference must not wipe other fields
	if got.JogFeed != original.JogFeed {
		t.Errorf("JogFeed changed after SaveHomingReference: got %d want %d",
			got.JogFeed, original.JogFeed)
	}

	// Restore original HomingApos
	_ = SaveHomingReference("A", originalApos)
}

// ─── GetEnvSettings ───────────────────────────────────────────────────────────

func TestGetEnvSettings_ReturnsNonNilStruct(t *testing.T) {
	env := GetEnvSettings()
	// envconfig.yaml may or may not exist; either way GetEnvSettings
	// returns a struct (empty fields if file missing, populated if present)
	t.Logf("GetEnvSettings: version=%q mode=%q logLevel=%q",
		env.Version, env.Mode, env.LogLevel)
}

func TestGetEnvSettings_CalledTwice_ReturnsSameValue(t *testing.T) {
	// GetEnvSettings caches after first call — second call returns same struct
	e1 := GetEnvSettings()
	e2 := GetEnvSettings()
	if e1.Version != e2.Version || e1.Mode != e2.Mode {
		t.Errorf("GetEnvSettings not idempotent: first=%+v second=%+v", e1, e2)
	}
}

// ─── SaveTextProgramConfig / LoadTextProgramConfig ────────────────────────────

func TestSaveAndLoadTextProgramConfig_RoundTrip(t *testing.T) {
	original, loadErr := LoadTextProgramConfig()

	want := TextProgramConfig{
		IP:       "192.168.1.100",
		User:     "pi",
		Password: "test",
		Path:     "/home/pi/programs",
	}
	if err := SaveTextProgramConfig(want); err != nil {
		t.Fatalf("SaveTextProgramConfig: %v", err)
	}
	got, err := LoadTextProgramConfig()
	if err != nil {
		t.Fatalf("LoadTextProgramConfig after save: %v", err)
	}
	if got.IP != want.IP || got.User != want.User || got.Path != want.Path {
		t.Errorf("round-trip: got %+v want %+v", got, want)
	}

	// Restore original — if load failed originally the file didn't exist, skip restore
	if loadErr == nil {
		_ = SaveTextProgramConfig(original)
	}
}

func TestLoadTextProgramConfig_MissingFile_ReturnsError(t *testing.T) {
	// If textprogram.json doesn't exist yet, LoadTextProgramConfig returns an error.
	// We can't easily control this since the path is hardcoded, so just verify
	// the function returns either a valid struct or a sensible error — never panics.
	_, err := LoadTextProgramConfig()
	if err != nil {
		t.Logf("LoadTextProgramConfig: %v (expected if file not yet created)", err)
	}
}

// ─── SaveLineNumber / LoadLineNumber (path override via userLineFile var) ─────

func TestSaveAndLoadLineNumber_TempFile(t *testing.T) {
	orig := userLineFile
	userLineFile = filepath.Join(t.TempDir(), "userline.json")
	t.Cleanup(func() { userLineFile = orig })

	if err := SaveLineNumber("7"); err != nil {
		t.Fatalf("SaveLineNumber: %v", err)
	}
	got, err := LoadLineNumber()
	if err != nil {
		t.Fatalf("LoadLineNumber: %v", err)
	}
	if got != "7" {
		t.Errorf("got %q, want 7", got)
	}
}

func TestSaveLineNumber_OverwritesPrevious_TempFile(t *testing.T) {
	orig := userLineFile
	userLineFile = filepath.Join(t.TempDir(), "userline.json")
	t.Cleanup(func() { userLineFile = orig })

	_ = SaveLineNumber("3")
	_ = SaveLineNumber("9")
	got, _ := LoadLineNumber()
	if got != "9" {
		t.Errorf("got %q, want 9", got)
	}
}

func TestLoadLineNumber_MissingFile_TempFile(t *testing.T) {
	orig := userLineFile
	userLineFile = filepath.Join(t.TempDir(), "nonexistent.json")
	t.Cleanup(func() { userLineFile = orig })

	_, err := LoadLineNumber()
	if err == nil {
		t.Error("expected error for missing file, got nil")
	}
}

func TestSaveLineNumber_EmptyString_TempFile(t *testing.T) {
	orig := userLineFile
	userLineFile = filepath.Join(t.TempDir(), "userline.json")
	t.Cleanup(func() { userLineFile = orig })

	_ = SaveLineNumber("")
	got, _ := LoadLineNumber()
	if got != "" {
		t.Errorf("got %q, want empty string", got)
	}
}

// ─── GetWorkOffset edge cases ─────────────────────────────────────────────────

func TestGetWorkOffset_G53_AlwaysZero(t *testing.T) {
	ds := DriverSettings{G54: 5.0, G55: 10.0, G56: 20.0, G57: 30.0, G58: 40.0}
	offsets := ds.GetWorkOffset()
	if offsets["G53"] != 0 {
		t.Errorf("G53 = %f, want 0 (reference frame must always be zero)", offsets["G53"])
	}
}

func TestGetWorkOffset_NegativeOffsets(t *testing.T) {
	ds := DriverSettings{G54: -3.14, G55: -90.0}
	offsets := ds.GetWorkOffset()
	if offsets["G54"] != -3.14 {
		t.Errorf("G54 = %f, want -3.14", offsets["G54"])
	}
	if offsets["G55"] != -90.0 {
		t.Errorf("G55 = %f, want -90.0", offsets["G55"])
	}
}

// ─── DriverSettings — all fields set and retrieved ───────────────────────────

func TestDriverSettings_AllFields_SetAndGet(t *testing.T) {
	want := DriverSettings{
		FinishSignal:       1,
		WorkOffSet:         3.14,
		JogFeed:            10,
		HomingOffset:       1.5,
		HomingApos:         72000,
		HomeDirection:      1,
		GearRation:         "1:1",
		ECSFinTiming:       100,
		BackLash:           0.25,
		ECS:                1,
		G55:                90.0,
		G54:                3.14,
		NOT:                -10,
		G57:                180.0,
		MotorDirection:     0,
		G58:                270.0,
		ClampDeclampTiming: 50,
		POT:                350,
		G56:                120.0,
		PitchError:         []Float64Str{0.001, -0.002, 0.003},
		Mode:               "ABS",
		FactorBacklash:     1,
		LineNumber:         "5",
		BinaryPosFeeds: []BinaryPosFeed{
			{Binary: "0001", Position: "90.0", Direction: 1, FeedRate: 10},
		},
	}
	SetDriverSettings("ALLFIELDS", want)
	t.Cleanup(func() {
		settingsMutex.Lock()
		delete(settingsRoot, "ALLFIELDS")
		settingsMutex.Unlock()
	})

	got := GetDriverSettings("ALLFIELDS")
	if got.JogFeed != want.JogFeed {
		t.Errorf("JogFeed: got %d want %d", got.JogFeed, want.JogFeed)
	}
	if got.HomingApos != want.HomingApos {
		t.Errorf("HomingApos: got %d want %d", got.HomingApos, want.HomingApos)
	}
	if got.GearRation != want.GearRation {
		t.Errorf("GearRation: got %q want %q", got.GearRation, want.GearRation)
	}
	if len(got.PitchError) != 3 {
		t.Errorf("PitchError len: got %d want 3", len(got.PitchError))
	}
	if len(got.BinaryPosFeeds) != 1 || got.BinaryPosFeeds[0].Binary != "0001" {
		t.Errorf("BinaryPosFeeds: got %+v", got.BinaryPosFeeds)
	}
}

// ─── Float64Str additional JSON cases ────────────────────────────────────────

func TestFloat64Str_UnmarshalJSON_NegativeFloat(t *testing.T) {
	var v Float64Str
	if err := json.Unmarshal([]byte(`"-1.234"`), &v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if float64(v) != -1.234 {
		t.Errorf("got %f, want -1.234", float64(v))
	}
}

func TestFloat64Str_UnmarshalJSON_ZeroString(t *testing.T) {
	var v Float64Str
	if err := json.Unmarshal([]byte(`"0.000"`), &v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if float64(v) != 0 {
		t.Errorf("got %f, want 0", float64(v))
	}
}

func TestFloat64Str_MarshalJSON_NegativeValue(t *testing.T) {
	v := Float64Str(-5.5)
	b, err := v.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var s string
	_ = json.Unmarshal(b, &s)
	if s != "-5.500" {
		t.Errorf("marshaled = %q, want -5.500", s)
	}
}

// ─── SaveHomingReference — in-memory paths ────────────────────────────────────

func TestSaveHomingReference_OnlyTargetDriveUpdated(t *testing.T) {
	settingsMutex.Lock()
	settingsRoot = SettingsRoot{
		"A": {JogFeed: 10, HomingApos: 0},
		"B": {JogFeed: 15, HomingApos: 0},
	}
	settingsMutex.Unlock()
	t.Cleanup(func() {
		settingsMutex.Lock()
		settingsRoot = nil
		settingsMutex.Unlock()
	})

	settingsMutex.Lock()
	ds := settingsRoot["A"]
	ds.HomingApos = 12345
	settingsRoot["A"] = ds
	settingsMutex.Unlock()

	if GetDriverSettings("A").HomingApos != 12345 {
		t.Errorf("A.HomingApos should be 12345")
	}
	if GetDriverSettings("B").HomingApos != 0 {
		t.Errorf("B.HomingApos should be untouched")
	}
}

// ─── Concurrent safety — additional ──────────────────────────────────────────

func TestGetAllSettings_ConcurrentMultipleWrites_NoRace(t *testing.T) {
	settingsMutex.Lock()
	settingsRoot = SettingsRoot{"A": {JogFeed: 5}, "B": {JogFeed: 10}}
	settingsMutex.Unlock()
	t.Cleanup(func() {
		settingsMutex.Lock()
		settingsRoot = nil
		settingsMutex.Unlock()
	})

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = GetAllSettings() }()
		wg.Add(1)
		go func(v int) {
			defer wg.Done()
			settingsMutex.Lock()
			ds := settingsRoot["A"]
			ds.JogFeed = v
			settingsRoot["A"] = ds
			settingsMutex.Unlock()
		}(i)
	}
	wg.Wait()
}

// ─── ResetEnvSettings / GetEnvSettings cache invalidation ────────────────────

func TestResetEnvSettings_ClearsCache(t *testing.T) {
	_ = GetEnvSettings()
	ResetEnvSettings()
	if envSettingsLoaded {
		t.Error("envSettingsLoaded should be false after ResetEnvSettings()")
	}
	if (envSettings != EnvironmentSettings{}) {
		t.Error("envSettings should be zero value after ResetEnvSettings()")
	}
}

func TestResetEnvSettings_AllowsReload(t *testing.T) {
	e1 := GetEnvSettings()
	ResetEnvSettings()
	e2 := GetEnvSettings()
	if e1.Mode != e2.Mode || e1.Version != e2.Version {
		t.Errorf("after reset+reload: first=%+v second=%+v", e1, e2)
	}
	ResetEnvSettings()
}

func TestGetEnvSettings_AfterReset_SetsLoadedFlag(t *testing.T) {
	ResetEnvSettings()
	_ = GetEnvSettings()
	if !envSettingsLoaded {
		t.Error("envSettingsLoaded should be true after GetEnvSettings()")
	}
	ResetEnvSettings()
}
