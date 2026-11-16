//go:build unit

package settings

// Unit tests for the settings package.
//
// The settings package is used by almost every other package in the codebase
// (every motion command calls GetDriverSettings, every alarm handler reads
// ECS and POT/NOT limits). A bug here propagates silently everywhere — wrong
// POT limit means the motor drives past its mechanical stop; wrong ECS flag
// means the clamp check is skipped; wrong JogFeed silently changes jog speed.
//
// These tests use SetDriverSettings (test_helpers_unit.go) to inject
// deterministic values without touching the filesystem or relying on a
// settings.json being present on the build machine.
//
// For tests that require disk I/O (LoadDriverSettings, SaveHomingReference),
// see settings_integration_test.go.

import (
	"sync"
	"testing"
)

// resetSettings clears the package-level settingsRoot between tests so
// each test starts from a clean state.
func resetSettings(t *testing.T) {
	t.Helper()
	settingsMutex.Lock()
	settingsRoot = nil
	settingsMutex.Unlock()
	t.Cleanup(func() {
		settingsMutex.Lock()
		settingsRoot = nil
		settingsMutex.Unlock()
	})
}

// ─── GetDriverSettings ────────────────────────────────────────────────────────

func TestGetDriverSettings_UnknownDrive_ReturnsZeroValue(t *testing.T) {
	resetSettings(t)
	ds := GetDriverSettings("NONEXISTENT")
	// Zero value: all numeric fields 0, all string fields ""
	if ds.JogFeed != 0 || ds.POT != 0 || ds.ECS != 0 {
		t.Errorf("unknown drive: got non-zero settings %+v", ds)
	}
}

func TestGetDriverSettings_KnownDrive_ReturnsCorrectValues(t *testing.T) {
	resetSettings(t)
	want := DriverSettings{
		JogFeed:       10,
		POT:           350,
		NOT:           -10,
		ECS:           1,
		HomeDirection: 1,
		BackLash:      0.5,
	}
	SetDriverSettings("A", want)

	got := GetDriverSettings("A")
	if got.JogFeed != want.JogFeed {
		t.Errorf("JogFeed: got %d want %d", got.JogFeed, want.JogFeed)
	}
	if got.POT != want.POT {
		t.Errorf("POT: got %d want %d", got.POT, want.POT)
	}
	if got.NOT != want.NOT {
		t.Errorf("NOT: got %d want %d", got.NOT, want.NOT)
	}
	if got.ECS != want.ECS {
		t.Errorf("ECS: got %d want %d", got.ECS, want.ECS)
	}
	if got.BackLash != want.BackLash {
		t.Errorf("BackLash: got %f want %f", got.BackLash, want.BackLash)
	}
}

func TestGetDriverSettings_ReturnsCopy_NotReference(t *testing.T) {
	resetSettings(t)
	SetDriverSettings("A", DriverSettings{JogFeed: 5})

	got := GetDriverSettings("A")
	got.JogFeed = 999 // mutate the copy

	// Original in the map should be unchanged
	original := GetDriverSettings("A")
	if original.JogFeed != 5 {
		t.Errorf("GetDriverSettings returned reference not copy — original mutated to %d", original.JogFeed)
	}
}

// ─── SetDriverSettings ────────────────────────────────────────────────────────

func TestSetDriverSettings_InitialisesNilMap(t *testing.T) {
	resetSettings(t)
	// settingsRoot is nil — SetDriverSettings must initialise it
	SetDriverSettings("A", DriverSettings{JogFeed: 7})

	got := GetDriverSettings("A")
	if got.JogFeed != 7 {
		t.Errorf("SetDriverSettings on nil map: got JogFeed=%d want 7", got.JogFeed)
	}
}

func TestSetDriverSettings_OverwritesExistingEntry(t *testing.T) {
	resetSettings(t)
	SetDriverSettings("A", DriverSettings{JogFeed: 5})
	SetDriverSettings("A", DriverSettings{JogFeed: 15})

	got := GetDriverSettings("A")
	if got.JogFeed != 15 {
		t.Errorf("overwrite: got JogFeed=%d want 15", got.JogFeed)
	}
}

func TestSetDriverSettings_MultipleDrivers_Independent(t *testing.T) {
	resetSettings(t)
	SetDriverSettings("A", DriverSettings{JogFeed: 10, POT: 350})
	SetDriverSettings("B", DriverSettings{JogFeed: 5, POT: 180})

	a := GetDriverSettings("A")
	b := GetDriverSettings("B")

	if a.JogFeed != 10 || a.POT != 350 {
		t.Errorf("drive A: got JogFeed=%d POT=%d want 10, 350", a.JogFeed, a.POT)
	}
	if b.JogFeed != 5 || b.POT != 180 {
		t.Errorf("drive B: got JogFeed=%d POT=%d want 5, 180", b.JogFeed, b.POT)
	}
}

// ─── GetAllSettings ───────────────────────────────────────────────────────────

func TestGetAllSettings_EmptyMap_ReturnsEmptyMap(t *testing.T) {
	resetSettings(t)
	all := GetAllSettings()
	if len(all) != 0 {
		t.Errorf("empty settings: got %d entries want 0", len(all))
	}
}

func TestGetAllSettings_ReturnsCopyOfAllEntries(t *testing.T) {
	resetSettings(t)
	SetDriverSettings("A", DriverSettings{JogFeed: 10})
	SetDriverSettings("B", DriverSettings{JogFeed: 5})

	all := GetAllSettings()
	if len(all) != 2 {
		t.Errorf("GetAllSettings: got %d entries want 2", len(all))
	}
	if all["A"].JogFeed != 10 {
		t.Errorf("GetAllSettings[A].JogFeed = %d want 10", all["A"].JogFeed)
	}
	if all["B"].JogFeed != 5 {
		t.Errorf("GetAllSettings[B].JogFeed = %d want 5", all["B"].JogFeed)
	}
}

func TestGetAllSettings_MutatingReturnedMap_DoesNotAffectOriginal(t *testing.T) {
	resetSettings(t)
	SetDriverSettings("A", DriverSettings{JogFeed: 10})

	all := GetAllSettings()
	all["A"] = DriverSettings{JogFeed: 999}
	all["Z"] = DriverSettings{JogFeed: 1}

	// Original should be unchanged
	got := GetDriverSettings("A")
	if got.JogFeed != 10 {
		t.Errorf("mutating returned map affected original: JogFeed=%d want 10", got.JogFeed)
	}
	if _, ok := GetAllSettings()["Z"]; ok {
		t.Error("adding key to returned map added it to original")
	}
}

// ─── GetWorkOffset ────────────────────────────────────────────────────────────

func TestGetWorkOffset_ReturnsAllOffsets(t *testing.T) {
	ds := DriverSettings{
		G54: 90.0,
		G55: 180.0,
		G56: 270.0,
		G57: 45.0,
		G58: 135.0,
	}
	offsets := ds.GetWorkOffset()

	// G53 is always 0 (machine coordinate origin)
	if offsets["G53"] != 0 {
		t.Errorf("G53: got %f want 0", offsets["G53"])
	}
	if offsets["G54"] != 90.0 {
		t.Errorf("G54: got %f want 90.0", offsets["G54"])
	}
	if offsets["G55"] != 180.0 {
		t.Errorf("G55: got %f want 180.0", offsets["G55"])
	}
	if offsets["G56"] != 270.0 {
		t.Errorf("G56: got %f want 270.0", offsets["G56"])
	}
	if offsets["G57"] != 45.0 {
		t.Errorf("G57: got %f want 45.0", offsets["G57"])
	}
	if offsets["G58"] != 135.0 {
		t.Errorf("G58: got %f want 135.0", offsets["G58"])
	}
}

func TestGetWorkOffset_ZeroOffsets_AllZero(t *testing.T) {
	ds := DriverSettings{} // all zero
	offsets := ds.GetWorkOffset()
	for _, key := range []string{"G53", "G54", "G55", "G56", "G57", "G58"} {
		if offsets[key] != 0 {
			t.Errorf("%s: got %f want 0", key, offsets[key])
		}
	}
}

func TestGetWorkOffset_ReturnsAllExpectedKeys(t *testing.T) {
	ds := DriverSettings{}
	offsets := ds.GetWorkOffset()
	for _, key := range []string{"G53", "G54", "G55", "G56", "G57", "G58"} {
		if _, ok := offsets[key]; !ok {
			t.Errorf("GetWorkOffset missing key %s", key)
		}
	}
}

// ─── Float64Str custom JSON marshaling ───────────────────────────────────────

func TestFloat64Str_MarshalUnmarshal_RoundTrip(t *testing.T) {
	cases := []struct {
		input string
		want  float64
	}{
		{"0.0", 0.0},
		{"1.5", 1.5},
		{"-0.25", -0.25},
		{"100.000", 100.0},
	}

	for _, c := range cases {
		var f Float64Str
		// UnmarshalJSON expects a JSON-encoded string value (with quotes)
		if err := f.UnmarshalJSON([]byte(`"` + c.input + `"`)); err != nil {
			t.Fatalf("UnmarshalJSON(%q): %v", c.input, err)
		}
		if float64(f) != c.want {
			t.Errorf("Float64Str(%q) = %f want %f", c.input, float64(f), c.want)
		}
	}
}

func TestFloat64Str_UnmarshalJSON_NumericFallback(t *testing.T) {
	// When the JSON value is a number (not a quoted string), the fallback path fires
	var f Float64Str
	if err := f.UnmarshalJSON([]byte(`1.5`)); err != nil {
		t.Fatalf("numeric fallback: %v", err)
	}
	if float64(f) != 1.5 {
		t.Errorf("numeric fallback: got %f want 1.5", float64(f))
	}
}

func TestFloat64Str_MarshalJSON_ProducesQuotedString(t *testing.T) {
	f := Float64Str(90.5)
	b, err := f.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	// Should produce a quoted string like "90.500"
	s := string(b)
	if s[0] != '"' || s[len(s)-1] != '"' {
		t.Errorf("MarshalJSON: got %s, want a quoted string", s)
	}
}

// ─── Concurrency — no data race under concurrent reads ────────────────────────

func TestGetDriverSettings_ConcurrentReads_NoRace(t *testing.T) {
	// Validates that concurrent GetDriverSettings calls do not race.
	// The settingsMutex.RLock() in GetDriverSettings allows concurrent readers.
	// Run with: go test -race -tags=unit ./settings/...
	resetSettings(t)
	SetDriverSettings("A", DriverSettings{JogFeed: 10, POT: 350})

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ds := GetDriverSettings("A")
			_ = ds.JogFeed
		}()
	}
	wg.Wait()
}

func TestGetAllSettings_ConcurrentReadsAndWrite_NoRace(t *testing.T) {
	// Validates that concurrent GetAllSettings calls do not race with
	// SetDriverSettings (write). The RWMutex correctly serialises the writer
	// against all readers.
	resetSettings(t)
	SetDriverSettings("A", DriverSettings{JogFeed: 5})

	var wg sync.WaitGroup
	// 40 readers
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = GetAllSettings()
		}()
	}
	// 5 writers interleaved
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			SetDriverSettings("A", DriverSettings{JogFeed: n})
		}(i)
	}
	wg.Wait()
}

// ─── RS232 helper functions ───────────────────────────────────────────────────

func TestRS232DataToBool_One_ReturnsTrue(t *testing.T) {
	if !RS232DataToBool("1") {
		t.Error("RS232DataToBool(1) should return true")
	}
}

func TestRS232DataToBool_Zero_ReturnsFalse(t *testing.T) {
	if RS232DataToBool("0") {
		t.Error("RS232DataToBool(0) should return false")
	}
}

func TestRS232DataToBool_Other_ReturnsFalse(t *testing.T) {
	for _, v := range []string{"", "true", "yes", "2"} {
		if RS232DataToBool(v) {
			t.Errorf("RS232DataToBool(%q) should return false", v)
		}
	}
}

func TestRS232BoolToData_True_ReturnsOne(t *testing.T) {
	if RS232BoolToData(true) != "1" {
		t.Error("RS232BoolToData(true) should return '1'")
	}
}

func TestRS232BoolToData_False_ReturnsZero(t *testing.T) {
	if RS232BoolToData(false) != "0" {
		t.Error("RS232BoolToData(false) should return '0'")
	}
}

func TestRS232BoolToData_RoundTrip(t *testing.T) {
	for _, b := range []bool{true, false} {
		if RS232DataToBool(RS232BoolToData(b)) != b {
			t.Errorf("RS232 bool round-trip failed for %v", b)
		}
	}
}

// ─── normalizeStringTaggedNumbers — regression tests ──────────────────────
//
// REAL BUG THIS FIXES: on real hardware, settings.json had "jog_feed": 20
// (a bare, unquoted number). JogFeed is tagged `json:"jog_feed,string"`,
// which requires encoding/json to see a QUOTED string. The strict
// json.Unmarshal in LoadDriverSettings silently left JogFeed at its zero
// value for that entry, so every ManualJog command computed a velocity of
// exactly 0 — jog appeared completely dead despite the settings UI showing
// 20. normalizeStringTaggedNumbers runs before unmarshaling and quotes any
// bare numeric value for the known ,string-tagged fields, without touching
// values that are already correctly quoted.

func TestNormalizeStringTaggedNumbers_QuotesBareJogFeed(t *testing.T) {
	in := []byte(`{"A": {"jog_feed": 20, "pot": 350}}`)
	out := normalizeStringTaggedNumbers(in)
	want := `{"A": {"jog_feed": "20", "pot": "350"}}`
	if string(out) != want {
		t.Errorf("normalizeStringTaggedNumbers(%s) = %s, want %s", in, out, want)
	}
}

func TestNormalizeStringTaggedNumbers_LeavesAlreadyQuotedValuesUntouched(t *testing.T) {
	in := []byte(`{"A": {"jog_feed": "20", "pot": "350"}}`)
	out := normalizeStringTaggedNumbers(in)
	if string(out) != string(in) {
		t.Errorf("normalizeStringTaggedNumbers should not modify already-quoted values: got %s, want unchanged %s", out, in)
	}
}

func TestNormalizeStringTaggedNumbers_HandlesNegativeNumbers(t *testing.T) {
	in := []byte(`{"A": {"not": -10}}`)
	out := normalizeStringTaggedNumbers(in)
	want := `{"A": {"not": "-10"}}`
	if string(out) != want {
		t.Errorf("normalizeStringTaggedNumbers(%s) = %s, want %s", in, out, want)
	}
}

func TestNormalizeStringTaggedNumbers_HandlesFloats(t *testing.T) {
	in := []byte(`{"A": {"work_offset": 12.5}}`)
	out := normalizeStringTaggedNumbers(in)
	want := `{"A": {"work_offset": "12.5"}}`
	if string(out) != want {
		t.Errorf("normalizeStringTaggedNumbers(%s) = %s, want %s", in, out, want)
	}
}

func TestNormalizeStringTaggedNumbers_HandlesEndOfObjectNoTrailingComma(t *testing.T) {
	in := []byte(`{"A": {"jog_feed":20}}`)
	out := normalizeStringTaggedNumbers(in)
	want := `{"A": {"jog_feed": "20"}}`
	if string(out) != want {
		t.Errorf("normalizeStringTaggedNumbers(%s) = %s, want %s", in, out, want)
	}
}

// TestNormalizeStringTaggedNumbers_SubstringFieldNamesDoNotCollide guards
// against a real risk in this kind of pattern-based fix: "cldl_timing" ends
// with the substring "timing", which is ALSO a distinct field name
// (ECSFinTiming). Both must be normalized independently and correctly,
// with neither pattern accidentally matching the other's key.
func TestNormalizeStringTaggedNumbers_SubstringFieldNamesDoNotCollide(t *testing.T) {
	in := []byte(`{"A": {"cldl_timing": 100, "timing": 50}}`)
	out := normalizeStringTaggedNumbers(in)
	want := `{"A": {"cldl_timing": "100", "timing": "50"}}`
	if string(out) != want {
		t.Errorf("normalizeStringTaggedNumbers(%s) = %s, want %s", in, out, want)
	}
}

// See settings_integration_test.go for the full end-to-end regression test
// against the real LoadDriverSettings/GetDriverSettings path (disk I/O —
// belongs with the other integration-tagged tests per this file's own
// convention, noted at the top).

// ─── Int32Str — same lenient pattern as Float64Str, for BinaryPosFeed.FeedRate ──

func TestInt32Str_UnmarshalJSON_QuotedString(t *testing.T) {
	var i Int32Str
	if err := i.UnmarshalJSON([]byte(`"8"`)); err != nil {
		t.Fatalf("quoted string: %v", err)
	}
	if int32(i) != 8 {
		t.Errorf("quoted string: got %d want 8", int32(i))
	}
}

func TestInt32Str_UnmarshalJSON_NumericFallback(t *testing.T) {
	// REAL BUG THIS FIXES: settings.json had "feed_rate": 20 (bare number)
	// for some binary_pos_feed entries and "feed_rate": "8" (quoted) for
	// others — inconsistent formatting written by different code paths
	// over this project's history. FeedRate was a plain int32 (no leniency
	// at all), so the quoted entries failed to unmarshal.
	var i Int32Str
	if err := i.UnmarshalJSON([]byte(`20`)); err != nil {
		t.Fatalf("numeric fallback: %v", err)
	}
	if int32(i) != 20 {
		t.Errorf("numeric fallback: got %d want 20", int32(i))
	}
}

func TestInt32Str_MarshalUnmarshal_RoundTrip(t *testing.T) {
	orig := Int32Str(-42)
	data, err := orig.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	var got Int32Str
	if err := got.UnmarshalJSON(data); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	if got != orig {
		t.Errorf("round trip: got %d want %d", got, orig)
	}
}
