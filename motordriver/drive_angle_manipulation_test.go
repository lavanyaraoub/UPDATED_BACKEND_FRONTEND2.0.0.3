//go:build unit

package motordriver

// Tests for drive_angle_manipulation.go — the pure-math functions that
// translate between degrees, encoder pulses, and EtherCAT position values.
//
// None of these tests touch CGo, EtherCAT headers, or the IgH stack.
// They exercise the logic layer only, using a hand-built MasterDevice
// stub (no C pointers required).
//
// Coverage targets:
//   - getPulsesFromDegree   : degree → encoder count (linear, signed)
//   - currentPosition       : encoder count → normalised [0, 360) degree
//   - getAbsolutePosition   : thin wrapper around helper — verify delegation
//   - getRelativePosition   : threshold-gated wrapper around helper
//   - getPitchError         : index-mapped lookup with boundary protection
//
// PORTING NOTE: getPulsesFromDegree, getAbsolutePosition, getRelativePosition,
// and getPitchError are all byte-identical to testenv's versions — ported
// unchanged. currentPosition's boot-time encoder correction moved from a
// package-level `aposCorrection atomic.Int32` (testenv) to a per-device
// `MasterDevice.AposCorrection` field (HAL) — same multi-axis fix pattern
// seen throughout this codebase (the old global was shared across every
// drive, so a correction computed for axis A would silently apply to axis
// B too). Every `aposCorrection.Store(N)` call below becomes
// `setMasterDevicesForTest(stubDeviceWithApos("A", N))`, injecting a stub
// device with the correction into the package-level masterDevices slice
// that currentPosition's internal lookup reads from.
//
// NOT tested here (require hardware or CGo):
//   - InitAposCorrection (reads PDO live value — hardware path)

import (
	"math"
	"testing"

	ethercatDevice "EtherCAT/ethercatdevicedatatypes"
	"EtherCAT/settings"
)

// ─── helpers ───────────────────────────────────────────────────────────────

// makeDevice builds a minimal MasterDevice with the given DriveXRatio.
// No C pointers are set — only the fields used by the pure-math functions.
func makeDevice(driveXRatio int) *MasterDevice {
	return &MasterDevice{
		Name: "A",
		Device: ethercatDevice.Device{
			DriveXRatio: driveXRatio,
		},
	}
}

// stubDeviceWithApos builds a stub device named `name` with AposCorrection
// pre-set to `correction` pulses. Used to inject a specific boot-time
// encoder correction for currentPosition tests via setMasterDevicesForTest
// (see mock_driver_test.go).
func stubDeviceWithApos(name string, correction int32) *MasterDevice {
	d := stubDevice(name)
	d.AposCorrection.Store(correction)
	return d
}

// injectDriverSettings loads a synthetic DriverSettings into the settings
// package so functions like getPitchError and currentPosition can read them
// without a real settings.json on disk.
func injectDriverSettings(name string, ds settings.DriverSettings) {
	settings.SetDriverSettings(name, ds)
}

// almostEqual compares two float64 values within a small epsilon.
func almostEqual(a, b float64) bool {
	return math.Abs(a-b) < 0.001
}

// ─── fixed home-referenced absolute target ────────────────────────────────

func TestCalculateFixedAbsoluteGoal_IndependentOfStartingResidual(t *testing.T) {
	const home = int32(1_000_000)
	injectDriverSettings("A", settings.DriverSettings{HomingApos: home})
	dev := makeDevice(20_000)

	dev.PDOPos.Store(home + 20*20_000 + 9)
	goal1, err := calculateFixedAbsoluteGoal(dev, 300, 280, 0, 0)
	if err != nil {
		t.Fatal(err)
	}

	dev.PDOPos.Store(home + 20*20_000 - 11)
	goal2, err := calculateFixedAbsoluteGoal(dev, 300, 280, 0, 0)
	if err != nil {
		t.Fatal(err)
	}

	want := home + 300*20_000
	if goal1 != want || goal2 != want {
		t.Fatalf("goals = %d and %d, want identical fixed target %d", goal1, goal2, want)
	}
}

func TestCalculateFixedAbsoluteGoal_CWAndCCWSelectEquivalentRevolutions(t *testing.T) {
	const (
		home  = int32(1_000_000)
		ratio = int32(20_000)
	)
	injectDriverSettings("A", settings.DriverSettings{HomingApos: home})
	dev := makeDevice(int(ratio))
	dev.PDOPos.Store(home + 20*ratio)

	cwGoal, err := calculateFixedAbsoluteGoal(dev, 300, 280, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if want := home + 300*ratio; cwGoal != want {
		t.Fatalf("CW goal = %d, want %d", cwGoal, want)
	}

	ccwGoal, err := calculateFixedAbsoluteGoal(dev, 300, -80, 0, 0.003)
	if err != nil {
		t.Fatal(err)
	}
	// 300 degrees on the previous revolution, shifted -0.003 degrees
	// for the configured CCW backlash compensation.
	wantCCW := int64(home) + int64(math.Round((300.0-0.003)*float64(ratio))) - 360*int64(ratio)
	if int64(ccwGoal) != wantCCW {
		t.Fatalf("CCW goal = %d, want %d", ccwGoal, wantCCW)
	}
}

func TestCalculateFixedAbsoluteGoal_CrossingZeroUsesNextRevolution(t *testing.T) {
	const (
		home  = int32(1_000_000)
		ratio = int32(20_000)
	)
	injectDriverSettings("A", settings.DriverSettings{HomingApos: home})
	dev := makeDevice(int(ratio))
	dev.PDOPos.Store(home + 350*ratio)

	goal, err := calculateFixedAbsoluteGoal(dev, 10, 20, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := int64(home) + 10*int64(ratio) + 360*int64(ratio)
	if int64(goal) != want {
		t.Fatalf("350->10 CW goal = %d, want next-revolution target %d", goal, want)
	}
}

func TestCalculateFixedAbsoluteGoal_AppliesAposCorrection(t *testing.T) {
	const home = int32(1_000_000)
	injectDriverSettings("A", settings.DriverSettings{HomingApos: home})
	dev := makeDevice(20_000)
	dev.AposCorrection.Store(1234)
	dev.PDOPos.Store(home + 1234)

	goal, err := calculateFixedAbsoluteGoal(dev, 90, 90, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := home + 1234 + 90*20_000
	if goal != want {
		t.Fatalf("goal with APOS correction = %d, want %d", goal, want)
	}
}

func TestCalculateFixedAbsoluteGoal_RequiresHomingReference(t *testing.T) {
	injectDriverSettings("A", settings.DriverSettings{HomingApos: 0})
	dev := makeDevice(20_000)
	if _, err := calculateFixedAbsoluteGoal(dev, 90, 90, 0, 0); err == nil {
		t.Fatal("expected error when homing_apos is zero")
	}
}

// ─── getPulsesFromDegree ───────────────────────────────────────────────────

func TestGetPulsesFromDegree_BasicMultiplication(t *testing.T) {
	tests := []struct {
		name        string
		driveXRatio int
		degree      float64
		want        int64
	}{
		// Standard production ratio: 20000 pulses/degree
		{"zero degrees", 20000, 0, 0},
		{"positive 90 degrees", 20000, 90, 1800000},
		{"positive 180 degrees", 20000, 180, 3600000},
		{"positive 360 degrees", 20000, 360, 7200000},

		// Negative (reverse direction)
		{"negative 90 degrees", 20000, -90, -1800000},
		{"negative 180 degrees", 20000, -180, -3600000},

		// Fractional degree — int64 truncation expected
		{"fractional 0.5 degrees", 20000, 0.5, 10000},
		{"fractional 0.1 degrees", 20000, 0.1, 2000},

		// Different drive ratio
		{"ratio 1000, 90 degrees", 1000, 90, 90000},
		{"ratio 5000, 45 degrees", 5000, 45, 225000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dev := makeDevice(tt.driveXRatio)
			got := getPulsesFromDegree(dev, tt.degree)
			if got != tt.want {
				t.Errorf("getPulsesFromDegree(ratio=%d, %.4f) = %d, want %d",
					tt.driveXRatio, tt.degree, got, tt.want)
			}
		})
	}
}

// Property: result must be linear (doubling the degree doubles the pulses).
func TestGetPulsesFromDegree_IsLinear(t *testing.T) {
	dev := makeDevice(20000)
	for deg := 1.0; deg <= 180.0; deg += 7 {
		single := getPulsesFromDegree(dev, deg)
		double := getPulsesFromDegree(dev, deg*2)
		if double != single*2 {
			t.Errorf("not linear at degree=%.1f: single=%d double=%d (want %d)",
				deg, single, double, single*2)
		}
	}
}

// Property: result must be antisymmetric (positive == -negative).
func TestGetPulsesFromDegree_AntisymmetricSign(t *testing.T) {
	dev := makeDevice(20000)
	for deg := 0.0; deg <= 360; deg += 10 {
		pos := getPulsesFromDegree(dev, deg)
		neg := getPulsesFromDegree(dev, -deg)
		if pos != -neg {
			t.Errorf("sign not antisymmetric at %.1f: +%.1f→%d, -%.1f→%d",
				deg, deg, pos, deg, neg)
		}
	}
}

// ─── currentPosition ───────────────────────────────────────────────────────
//
// currentPosition(pos int32, driveXRatio int, driveName string) (float64, float64)
//
// Contract:
//   Returns (position, positionWithErrorCorrection) — both normalised to [0, 360).
//   With HomingOffset=0, HomingApos=0, PitchError all zeros, and no
//   AposCorrection registered for the device:
//     position = (pos / driveXRatio) mod 360, normalised to [0, 360)

func TestCurrentPosition_ZeroEncoderAtZeroDegrees(t *testing.T) {
	injectDriverSettings("A", settings.DriverSettings{})
	restore := setMasterDevicesForTest(stubDeviceWithApos("A", 0))
	defer restore()

	pos, posEC := currentPosition(0, 20000, "A")
	if !almostEqual(pos, 0) {
		t.Errorf("currentPosition(0) = %v, want 0", pos)
	}
	if !almostEqual(posEC, 0) {
		t.Errorf("currentPosition(0) EC = %v, want 0", posEC)
	}
}

func TestCurrentPosition_QuarterTurn(t *testing.T) {
	injectDriverSettings("A", settings.DriverSettings{})
	restore := setMasterDevicesForTest(stubDeviceWithApos("A", 0))
	defer restore()

	// 90 degrees * 20000 = 1_800_000 pulses
	pos, _ := currentPosition(1_800_000, 20000, "A")
	if !almostEqual(pos, 90) {
		t.Errorf("currentPosition(1_800_000, 20000) = %v, want 90", pos)
	}
}

func TestCurrentPosition_HalfTurn(t *testing.T) {
	injectDriverSettings("A", settings.DriverSettings{})
	restore := setMasterDevicesForTest(stubDeviceWithApos("A", 0))
	defer restore()

	pos, _ := currentPosition(3_600_000, 20000, "A")
	if !almostEqual(pos, 180) {
		t.Errorf("currentPosition(3_600_000, 20000) = %v, want 180", pos)
	}
}

func TestCurrentPosition_FullTurnWrapsToZero(t *testing.T) {
	injectDriverSettings("A", settings.DriverSettings{})
	restore := setMasterDevicesForTest(stubDeviceWithApos("A", 0))
	defer restore()

	// 360 degrees * 20000 = 7_200_000 should mod to 0
	pos, _ := currentPosition(7_200_000, 20000, "A")
	if !almostEqual(pos, 0) {
		t.Errorf("full turn position = %v, want 0 (mod 360)", pos)
	}
}

func TestCurrentPosition_NegativeEncoderNormalisedToPositive(t *testing.T) {
	injectDriverSettings("A", settings.DriverSettings{})
	restore := setMasterDevicesForTest(stubDeviceWithApos("A", 0))
	defer restore()

	// -90 degrees should produce 270 after normalisation
	pos, _ := currentPosition(-1_800_000, 20000, "A")
	if !almostEqual(pos, 270) {
		t.Errorf("currentPosition(-1_800_000) = %v, want 270", pos)
	}
}

func TestCurrentPosition_AlwaysInRange(t *testing.T) {
	injectDriverSettings("A", settings.DriverSettings{})
	restore := setMasterDevicesForTest(stubDeviceWithApos("A", 0))
	defer restore()

	for encoderTicks := int32(-14_400_000); encoderTicks <= 14_400_000; encoderTicks += 500_000 {
		pos, _ := currentPosition(encoderTicks, 20000, "A")
		if pos < 0 || pos >= 360 {
			t.Errorf("currentPosition(%d) = %v, not in [0, 360)", encoderTicks, pos)
		}
	}
}

func TestCurrentPosition_AposCorrectionApplied(t *testing.T) {
	injectDriverSettings("A", settings.DriverSettings{})

	// With a correction of 200_000 pulses (10 degrees), encoder at 1_800_000
	// should produce 80 degrees (90 - 10), not 90.
	restore := setMasterDevicesForTest(stubDeviceWithApos("A", 200_000))
	defer restore()

	pos, _ := currentPosition(1_800_000, 20000, "A")
	if !almostEqual(pos, 80) {
		t.Errorf("currentPosition with 10-degree correction = %v, want 80", pos)
	}
}

func TestCurrentPosition_NoDeviceRegistered_NoCorrectionApplied(t *testing.T) {
	// If no device named "A" exists in masterDevices at all, HAL's internal
	// lookup finds nothing and correction defaults to 0 — this pins that
	// fallback behavior explicitly, distinct from "device exists with
	// AposCorrection=0".
	injectDriverSettings("A", settings.DriverSettings{})
	restore := setMasterDevicesForTest() // no devices at all
	defer restore()

	pos, _ := currentPosition(1_800_000, 20000, "A")
	if !almostEqual(pos, 90) {
		t.Errorf("currentPosition with no registered device = %v, want 90 (no correction)", pos)
	}
}

// ─── getAbsolutePosition ───────────────────────────────────────────────────
//
// getAbsolutePosition is a one-liner that delegates to helper.GetAbsolutePosition.
// We test that delegation is correct — not the underlying math (covered in
// helper/position_finder_test.go).

func TestGetAbsolutePosition_DelegatesCorrectly(t *testing.T) {
	tests := []struct {
		current, target float64
		shortestPath    bool
		wantToMove      float64
		wantDest        float64
	}{
		{0, 90, true, 90, 90},
		{90, 0, true, -90, 0},
		{350, 10, true, 20, 10},
		{10, 350, true, -20, 350},
	}

	for _, tt := range tests {
		toMove, dest := getAbsolutePosition(tt.current, tt.target, tt.shortestPath)
		if !almostEqual(toMove, tt.wantToMove) {
			t.Errorf("getAbsolutePosition(%.1f, %.1f, %v) toMove = %v, want %v",
				tt.current, tt.target, tt.shortestPath, toMove, tt.wantToMove)
		}
		if !almostEqual(dest, tt.wantDest) {
			t.Errorf("getAbsolutePosition(%.1f, %.1f, %v) dest = %v, want %v",
				tt.current, tt.target, tt.shortestPath, dest, tt.wantDest)
		}
	}
}

// ─── getRelativePosition ───────────────────────────────────────────────────
//
// getRelativePosition adds a threshold guard on top of helper.GetRelativePosition:
// if |current - prev| > 1.0, it replaces prev with current (first-move protection).

func TestGetRelativePosition_UsesCurrentWhenPrevFarAway(t *testing.T) {
	// prev=0, current=90, target=+10 → should treat prev=90 (threshold guard),
	// giving move=+10, dest=100.
	toMove, dest := getRelativePosition(90, 10, 0)
	if !almostEqual(toMove, 10) {
		t.Errorf("toMove = %v, want 10", toMove)
	}
	if !almostEqual(dest, 100) {
		t.Errorf("dest = %v, want 100", dest)
	}
}

func TestGetRelativePosition_UsesPrevWhenClose(t *testing.T) {
	// prev=90.5, current=90 (difference = 0.5, within 1.0 threshold).
	// target=+10 → move from prev=90.5, giving dest=100.5.
	_, dest := getRelativePosition(90, 10, 90.5)
	if !almostEqual(dest, 100.5) {
		t.Errorf("dest = %v, want 100.5 (prev within threshold)", dest)
	}
}

func TestGetRelativePosition_MinusOneSentinelFallsBackToCurrent(t *testing.T) {
	// prev=-1: |current - (-1)| > 1 always, so guard fires, prev → current.
	// current=45, target=+15 → dest=60.
	toMove, dest := getRelativePosition(45, 15, -1)
	if !almostEqual(toMove, 15) {
		t.Errorf("toMove = %v, want 15", toMove)
	}
	if !almostEqual(dest, 60) {
		t.Errorf("dest = %v, want 60", dest)
	}
}

func TestGetRelativePosition_WrapAroundStaysInRange(t *testing.T) {
	// current=350, prev=350, target=+20 → dest should be 10 (wraps past 360).
	_, dest := getRelativePosition(350, 20, 350)
	if dest < 0 || dest >= 360 {
		t.Errorf("dest = %v, not in [0, 360) after wrap", dest)
	}
	if !almostEqual(dest, 10) {
		t.Errorf("dest = %v, want 10 (350 + 20 = 370 → mod 360 = 10)", dest)
	}
}

// ─── getPitchError ─────────────────────────────────────────────────────────
//
// getPitchError maps targetPos → a 36-element pitch-error array.
// index = int(abs(targetPos / 10)) - 1, clamped to [0, 35].

func TestGetPitchError_ZeroTargetReturnsZero(t *testing.T) {
	// index = int(0/10) - 1 = -1 → clamped, return 0
	ds := settings.DriverSettings{PitchError: make([]settings.Float64Str, 36)}
	for i := range ds.PitchError {
		ds.PitchError[i] = settings.Float64Str(float64(i+1) * 0.01)
	}
	injectDriverSettings("A", ds)

	got := getPitchError("A", 0)
	if got != 0 {
		t.Errorf("getPitchError('A', 0) = %v, want 0 (out-of-range index)", got)
	}
}

// TestGetPitchError_EmptyPitchErrorTable_ReturnsZeroInsteadOfPanicking is a
// regression test for a real crash found on actual hardware: a rig that has
// never been through pitch-error calibration has an EMPTY (or partially
// populated) PitchError slice. The index-bounds guards above only check
// that index falls in the documented 0-35 range — they never checked
// whether the slice actually has that many elements. Indexing directly
// into an empty slice panicked with "index out of range [26] with length
// 0", crashing the entire process mid-motion (an A-90 move on this
// particular rig resolved to interval 26) and taking the PDO cyclic task
// down with it, leaving the drive in an uncontrolled/faulted state.
func TestGetPitchError_EmptyPitchErrorTable_ReturnsZeroInsteadOfPanicking(t *testing.T) {
	ds := settings.DriverSettings{PitchError: nil} // uncalibrated rig — exactly what crashed
	injectDriverSettings("A", ds)

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("getPitchError panicked with an empty PitchError table: %v", r)
		}
	}()

	// 270 -> index = int(270/10) - 1 = 26, the exact index that crashed on
	// real hardware.
	got := getPitchError("A", 270)
	if got != 0 {
		t.Errorf("getPitchError with empty PitchError table = %v, want 0", got)
	}
}

// TestGetPitchError_PartiallyPopulatedTable_ReturnsZeroForMissingIndices
// covers a rig that's had SOME intervals calibrated but not all 36 — a
// realistic intermediate state, not just the fully-empty case above.
func TestGetPitchError_PartiallyPopulatedTable_ReturnsZeroForMissingIndices(t *testing.T) {
	ds := settings.DriverSettings{PitchError: make([]settings.Float64Str, 10)} // only first 10 intervals calibrated
	ds.PitchError[5] = settings.Float64Str(0.007)
	injectDriverSettings("A", ds)

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("getPitchError panicked with a partially populated PitchError table: %v", r)
		}
	}()

	// index 5 exists -> real value.
	if got := getPitchError("A", 60); !almostEqual(got, 0.007) {
		t.Errorf("getPitchError('A', 60) = %v, want 0.007", got)
	}
	// index 26 does not exist in a 10-element slice -> must return 0, not panic.
	if got := getPitchError("A", 270); got != 0 {
		t.Errorf("getPitchError('A', 270) with a 10-element table = %v, want 0", got)
	}
}

func TestGetPitchError_FirstInterval(t *testing.T) {
	// targetPos = 10 → index = int(10/10) - 1 = 0 → PitchError[0]
	ds := settings.DriverSettings{PitchError: make([]settings.Float64Str, 36)}
	ds.PitchError[0] = settings.Float64Str(0.005)
	injectDriverSettings("A", ds)

	got := getPitchError("A", 10)
	if !almostEqual(got, 0.005) {
		t.Errorf("getPitchError('A', 10) = %v, want 0.005", got)
	}
}

func TestGetPitchError_LastInterval(t *testing.T) {
	// targetPos = 360 → index = int(360/10) - 1 = 35 → PitchError[35]
	ds := settings.DriverSettings{PitchError: make([]settings.Float64Str, 36)}
	ds.PitchError[35] = settings.Float64Str(0.009)
	injectDriverSettings("A", ds)

	got := getPitchError("A", 360)
	if !almostEqual(got, 0.009) {
		t.Errorf("getPitchError('A', 360) = %v, want 0.009", got)
	}
}

func TestGetPitchError_BeyondLastIntervalReturnsZero(t *testing.T) {
	// targetPos = 370 → index = 36 → clamped, return 0
	ds := settings.DriverSettings{PitchError: make([]settings.Float64Str, 36)}
	for i := range ds.PitchError {
		ds.PitchError[i] = settings.Float64Str(99)
	}
	injectDriverSettings("A", ds)

	got := getPitchError("A", 370)
	if got != 0 {
		t.Errorf("getPitchError('A', 370) = %v, want 0 (index > 35)", got)
	}
}

func TestGetPitchError_NegativeTargetUsesAbsoluteValue(t *testing.T) {
	// targetPos = -90 → abs = 90 → index = 8 → PitchError[8]
	ds := settings.DriverSettings{PitchError: make([]settings.Float64Str, 36)}
	ds.PitchError[8] = settings.Float64Str(0.003)
	injectDriverSettings("A", ds)

	got := getPitchError("A", -90)
	if !almostEqual(got, 0.003) {
		t.Errorf("getPitchError('A', -90) = %v, want 0.003 (same as +90)", got)
	}
}

func TestGetPitchError_AllIntervalsReachable(t *testing.T) {
	// Load distinct values into all 36 slots and verify each is reachable.
	ds := settings.DriverSettings{PitchError: make([]settings.Float64Str, 36)}
	for i := range ds.PitchError {
		ds.PitchError[i] = settings.Float64Str(float64(i+1) * 0.001)
	}
	injectDriverSettings("A", ds)

	for i := 0; i < 36; i++ {
		targetPos := float64((i + 1) * 10) // 10, 20, ..., 360
		want := float64(i+1) * 0.001
		got := getPitchError("A", targetPos)
		if !almostEqual(got, want) {
			t.Errorf("getPitchError('A', %.0f) [index %d] = %v, want %v",
				targetPos, i, got, want)
		}
	}
}
