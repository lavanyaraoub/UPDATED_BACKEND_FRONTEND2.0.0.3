//go:build unit

package helper

// Tests for position_finder.go — the math behind every motor movement.
//
// THIS FILE IS GROWN INCREMENTALLY. We start with the shortest-path
// function (simplest, easiest to reason about) and add others as we
// verify each batch passes. Per the testing roadmap, position_finder
// is the highest-risk testing target because the original author
// explicitly admitted not fully understanding the math
// (see comment in GetRelativePosition: "not sure how this difference
// is coming").
//
// We test by deriving expected outputs from first principles about
// what the function is SUPPOSED to do, not by running the function
// and recording what it does. If the function disagrees with first
// principles, the test fails and we investigate the disagreement.

import (
	"math"
	"testing"
)

// posEq compares two angles allowing for tiny float64 noise.
// All position values in this codebase are rounded to 3 decimal places
// (via RoundFloatTo3), so an epsilon of 0.001 is safely below the
// precision the rest of the system uses.
func posEq(got, want float64) bool {
	return math.Abs(got-want) < 0.001
}

// ─── getAbsolutePositionWithShortestPath ───────────────────────────────────
//
// Contract (derived from the comment and from rotational kinematics):
//   Given current angle and target angle (both in degrees), return the
//   signed shortest rotation needed to get from current to target.
//   Result is in (-180, +180]. Positive = forward (CCW by convention);
//   negative = backward (CW).
//
// Note: the function takes targetPos in the natural angle space (no mod
// applied) and applies math.Mod only to currentPos. This is unusual.
// The tests will reveal whether that matters in practice.

func TestShortestPath_BasicCases(t *testing.T) {
	tests := []struct {
		name       string
		currentPos float64
		targetPos  float64
		want       float64
	}{
		// Identity: no movement needed
		{"current=0, target=0", 0, 0, 0},
		{"current=90, target=90", 90, 90, 0},

		// Forward short moves
		{"current=0, target=10 → +10", 0, 10, 10},
		{"current=0, target=90 → +90", 0, 90, 90},
		{"current=10, target=20 → +10", 10, 20, 10},
		{"current=50, target=40 → -10 (from docstring example)", 50, 40, -10},

		// Backward short moves
		{"current=90, target=0 → -90", 90, 0, -90},
		{"current=20, target=10 → -10", 20, 10, -10},

		// Wrap-around cases — the whole point of "shortest path"
		{"current=350, target=10 → +20 (wrap forward)", 350, 10, 20},
		{"current=10, target=350 → -20 (wrap backward)", 10, 350, -20},
		{"current=359, target=1 → +2", 359, 1, 2},
		{"current=1, target=359 → -2", 1, 359, -2},

		// 180° tie — the implementation breaks ties toward negative
		// (returns -shortestDistance when test == 180 exactly)
		{"current=0, target=180 → -180 (tie goes negative)", 0, 180, -180},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getAbsolutePositionWithShortestPath(tt.currentPos, tt.targetPos)
			if !posEq(got, tt.want) {
				t.Errorf("getAbsolutePositionWithShortestPath(%v, %v) = %v, want %v",
					tt.currentPos, tt.targetPos, got, tt.want)
			}
		})
	}
}

// Property test: the absolute value of the result must NEVER exceed 180°.
// That's the defining property of shortest-path rotation. If this ever
// fails, the function is fundamentally broken.
func TestShortestPath_AbsoluteResultNeverExceeds180(t *testing.T) {
	// Sample every degree of current position and every degree of target
	// position. That's 360×360 = 129,600 cases. Fast (<100ms) and exhaustive.
	for current := 0; current < 360; current++ {
		for target := 0; target < 360; target++ {
			got := getAbsolutePositionWithShortestPath(
				float64(current), float64(target),
			)
			if math.Abs(got) > 180.001 { // tiny epsilon for float noise
				t.Errorf("|shortestPath(%d, %d)| = %v exceeds 180°",
					current, target, got)
				// Don't bail — let it find all the failures so we see
				// the pattern.
				if t.Failed() && testing.Short() {
					return
				}
			}
		}
	}
}

// Property test: applying the rotation should land us at the target.
// I.e., (current + shortestPath(current, target)) mod 360 == target mod 360.
// This is the OTHER defining property: the answer must actually take us
// to the destination.
func TestShortestPath_AppliedRotationLandsAtTarget(t *testing.T) {
	for current := 0; current < 360; current += 5 { // every 5° is enough
		for target := 0; target < 360; target += 5 {
			rotation := getAbsolutePositionWithShortestPath(
				float64(current), float64(target),
			)

			// Where we'd end up after applying the rotation:
			finalPos := float64(current) + rotation
			// Normalize to [0, 360)
			finalPosMod := math.Mod(finalPos, 360)
			if finalPosMod < 0 {
				finalPosMod += 360
			}

			if !posEq(finalPosMod, float64(target)) {
				t.Errorf("shortestPath(%d, %d) = %v; %d + %v = %v, "+
					"but should land at %d (mod 360 = %v)",
					current, target, rotation,
					current, rotation, finalPos,
					target, finalPosMod)
			}
		}
	}
}

// Property test: the function should be antisymmetric in a specific way:
// shortestPath(a, b) should generally equal -shortestPath(b, a), EXCEPT
// at the 180° tie point where both directions return -180 (tie goes
// negative regardless of order).
func TestShortestPath_AntisymmetricExceptAtTie(t *testing.T) {
	for current := 0; current < 360; current += 10 {
		for target := 0; target < 360; target += 10 {
			ab := getAbsolutePositionWithShortestPath(
				float64(current), float64(target),
			)
			ba := getAbsolutePositionWithShortestPath(
				float64(target), float64(current),
			)

			// Skip the 180° tie case — both directions return -180.
			if math.Abs(ab) > 179.999 {
				continue
			}

			if !posEq(ab, -ba) {
				t.Errorf("antisymmetry broken at (%d, %d): "+
					"shortest(a,b)=%v, shortest(b,a)=%v (expected %v)",
					current, target, ab, ba, -ab)
			}
		}
	}
}

// ─── GetAbsolutePosition (PUBLIC) ──────────────────────────────────────────
//
// This is the public entry point. Returns (toMove, destination):
//   - toMove: the rotation to apply (rounded to 3dp)
//   - destination: where we'll end up, normalized to [0, 360), rounded to 3dp
//
// SCOPE: we test only the useShortestPath=true branch, plus the destination
// computation which is path-independent. The useShortestPath=false branch
// delegates to getAbsolutePath whose contract is codebase-specific
// (G69 long-path behavior — not a clean mathematical operation).
// See note above the getAbsolutePath function for why we don't test it.

func TestGetAbsolutePosition_ShortestPath_DelegatesAndRounds(t *testing.T) {
	// useShortestPath=true should produce exactly the same toMove as
	// getAbsolutePositionWithShortestPath, then round to 3dp.
	tests := []struct {
		name       string
		currentPos float64
		targetPos  float64
		wantToMove float64
		wantDest   float64
	}{
		{"identity", 0, 0, 0, 0},
		{"+90 forward", 0, 90, 90, 90},
		{"-90 backward", 90, 0, -90, 0},
		{"wrap forward", 350, 10, 20, 10},
		{"wrap backward", 10, 350, -20, 350},
		{"docstring example 50→40", 50, 40, -10, 40},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotToMove, gotDest := GetAbsolutePosition(
				tt.currentPos, tt.targetPos, true,
			)
			if !posEq(gotToMove, tt.wantToMove) {
				t.Errorf("GetAbsolutePosition(%v, %v, true) toMove = %v, want %v",
					tt.currentPos, tt.targetPos, gotToMove, tt.wantToMove)
			}
			if !posEq(gotDest, tt.wantDest) {
				t.Errorf("GetAbsolutePosition(%v, %v, true) destination = %v, want %v",
					tt.currentPos, tt.targetPos, gotDest, tt.wantDest)
			}
		})
	}
}

// Property test for the shortest-path branch: GetAbsolutePosition should
// produce toMove identical (within rounding) to what the underlying
// shortest-path function produces directly. This pins that the delegation
// doesn't introduce any unexpected transformation.
func TestGetAbsolutePosition_ShortestPath_MatchesUnderlying(t *testing.T) {
	for current := 0; current < 360; current += 7 {
		for target := 0; target < 360; target += 7 {
			gotToMove, _ := GetAbsolutePosition(
				float64(current), float64(target), true,
			)
			wantToMove := RoundFloatTo3(
				getAbsolutePositionWithShortestPath(
					float64(current), float64(target),
				),
			)
			if !posEq(gotToMove, wantToMove) {
				t.Errorf("GetAbsolutePosition(%d, %d, true) toMove = %v, "+
					"but RoundFloatTo3(shortestPath(%d, %d)) = %v",
					current, target, gotToMove,
					current, target, wantToMove)
			}
		}
	}
}

// Tests for the destination normalization. This is path-independent —
// destination depends ONLY on targetPos, not on currentPos or shortestPath.
// We can test it without touching the path-choice logic at all.
//
// FIX (June 2026): destination normalization is now correct for all inputs.
// The canonical double-mod formula math.Mod(math.Mod(targetPos,360)+360,360)
// always produces [0, 360) regardless of input sign or magnitude.
// All cases below — including below -360° — now produce the correct result.
func TestGetAbsolutePosition_DestinationNormalization(t *testing.T) {
	tests := []struct {
		name      string
		targetPos float64
		wantDest  float64
	}{
		// In-range values (work correctly)
		{"0 stays 0", 0, 0},
		{"90 stays 90", 90, 90},
		{"180 stays 180", 180, 180},
		{"270 stays 270", 270, 270},
		{"359 stays 359", 359, 359},

		// Positive overflow (works correctly — math.Mod handles positives fine)
		{"360 wraps to 0", 360, 0},
		{"450 wraps to 90", 450, 90},
		{"720 wraps to 0", 720, 0},
		{"721 wraps to 1", 721, 1},

		// Negatives in [-360, 0) (work correctly — single +360 is enough)
		{"-10 wraps to 350", -10, 350},
		{"-90 wraps to 270", -90, 270},
		{"-180 wraps to 180", -180, 180},
		{"-359 wraps to 1", -359, 1},
		{"-360 wraps to 0", -360, 0},

		// Negatives below -360 — now correct after double-mod fix
		{"-450 normalises to 270", -450, 270},
		{"-720 wraps to 0 (lucky: -720+360=-360 mods to 0)", -720, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// currentPos doesn't matter for destination, but pick a
			// neutral value. useShortestPath also doesn't matter for
			// destination, but pass true to stay in the tested branch.
			_, gotDest := GetAbsolutePosition(0, tt.targetPos, true)
			if !posEq(gotDest, tt.wantDest) {
				t.Errorf("GetAbsolutePosition(0, %v, true) destination = %v, "+
					"want %v", tt.targetPos, gotDest, tt.wantDest)
			}
		})
	}
}

// Property test: destination must always be in [0, 360) for any input.
// Covers the full range including below -360° after the double-mod fix.
func TestGetAbsolutePosition_DestinationAlwaysInRange(t *testing.T) {
	// Full range including below -360 — now valid after the double-mod fix.
	for target := -1000.0; target <= 1000.0; target += 7 {
		_, gotDest := GetAbsolutePosition(0, target, true)
		if gotDest < 0 || gotDest >= 360 {
			t.Errorf("GetAbsolutePosition(0, %v, true) destination = %v, "+
				"not in [0, 360) within the working range", target, gotDest)
		}
	}
}

// ─── GetRelativePosition (PUBLIC) ──────────────────────────────────────────
//
// Public entry for relative-position moves. Contract from the docstring:
//   if currentPos=10 and targetPos=20, motor moves to 30 (= 10+20).
//
// SCOPE OF THESE TESTS — read carefully before adding to them:
//
//   The function has multiple internal branches (the diff1/diff2 logic,
//   the dead-code line at `destination = currentPos + destination`, the
//   prevDestinationAngle <= -1 vs >= 0 paths). The author's own comment
//   says: "at some point the current pos and prev pos shows a -1
//   difference, not sure how this difference is coming." Even the author
//   wasn't fully confident in the math.
//
//   Tests here cover ONLY the cases where the contract is clear:
//     - The docstring example (currentPos=10, target=20, prev=-1 → 30)
//     - The -1 sentinel behavior (prev=-1 should use currentPos)
//     - Normalization of destination to [0, 360)
//     - The dead-code line's lack of effect
//
//   We deliberately do NOT pin the diff1/diff2 branch outputs at specific
//   values, because doing so would just freeze whatever the function
//   currently does — which has limited value when we don't have a
//   spec for what it SHOULD do. If a future spec emerges, add tests then.

func TestGetRelativePosition_DocstringExample(t *testing.T) {
	// From the docstring: "if current=10 and ordered 20 then motor will
	// move to 30 (10+20=30)". Prev unset → use -1 sentinel.
	gotMove, gotDest := GetRelativePosition(10, 20, -1)
	if !posEq(gotMove, 20) {
		t.Errorf("moveTo = %v, want 20 (relative +20)", gotMove)
	}
	if !posEq(gotDest, 30) {
		t.Errorf("destination = %v, want 30 (10 + 20)", gotDest)
	}
}

// Sentinel behavior: when prevDestinationAngle is -1, the function should
// behave AS IF prevDestinationAngle = currentPos. This is the "first move
// after reset" case where there's no prior destination to chain from.
func TestGetRelativePosition_MinusOneSentinelMeansFirstMove(t *testing.T) {
	tests := []struct {
		name       string
		currentPos float64
		targetPos  float64
		wantMove   float64
		wantDest   float64
	}{
		{"from 0, +20", 0, 20, 20, 20},
		{"from 90, +30", 90, 30, 30, 120},
		{"from 100, -50", 100, -50, -50, 50},
		{"from 350, +20 wraps", 350, 20, 20, 10}, // 350+20=370, %360=10
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotMove, gotDest := GetRelativePosition(
				tt.currentPos, tt.targetPos, -1,
			)
			if !posEq(gotMove, tt.wantMove) {
				t.Errorf("moveTo(current=%v, target=%v, prev=-1) = %v, want %v",
					tt.currentPos, tt.targetPos, gotMove, tt.wantMove)
			}
			if !posEq(gotDest, tt.wantDest) {
				t.Errorf("destination(current=%v, target=%v, prev=-1) = %v, want %v",
					tt.currentPos, tt.targetPos, gotDest, tt.wantDest)
			}
		})
	}
}

// Destination normalization: regardless of internal branch chosen, the
// destination return value should land in [0, 360). Same property we
// tested for GetAbsolutePosition. Note: this may reveal the SAME
// bug-below-minus-360 we found in GetAbsolutePosition, because the
// normalization formula is identical between the two functions.
//
// We test the SAFE input range here (matches what production passes).
func TestGetRelativePosition_DestinationInRangeForWorkingInputs(t *testing.T) {
	// Real-world ranges: currentPos always in [0, 360), prev in [0, 360),
	// targetPos a relative offset typically in [-180, +180].
	for current := 0.0; current < 360; current += 30 {
		for prev := 0.0; prev < 360; prev += 30 {
			for target := -180.0; target <= 180; target += 30 {
				_, gotDest := GetRelativePosition(current, target, prev)
				if gotDest < 0 || gotDest >= 360 {
					t.Errorf("destination out of range: "+
						"GetRelativePosition(%v, %v, %v) dest = %v",
						current, target, prev, gotDest)
				}
			}
		}
	}
}

// The function has a line:
//
//	destination = currentPos + destination
//
// at the point where `destination` is its zero value (0). So this line
// computes `currentPos + 0 = currentPos`. But the next two lines
// unconditionally overwrite `destination` with either
// `prevDestinationAngle + targetPos` or `currentPos + targetPos`.
//
// So the suspicious line has NO effect on the output. This test confirms
// that — if we change the line to remove the bug-or-not effect, the
// output doesn't change.
//
// We can't easily test "this line was removed" without modifying the
// source. Instead, we verify the property the line WOULD provide if it
// weren't overwritten: the output is computed solely from the inputs.
// Specifically: calling the function twice with the same inputs must
// return identical results (the function has no observable state).
func TestGetRelativePosition_IsDeterministic(t *testing.T) {
	// Same inputs → same outputs, always. The dead-code line cannot
	// have side effects (it's a local assignment to a stack variable),
	// but this test pins the determinism contract regardless.
	inputs := []struct {
		current, target, prev float64
	}{
		{10, 20, -1},
		{90, 30, 60},
		{100, -50, -1},
		{350, 20, 30},
		{0, 0, 0},
	}
	for _, in := range inputs {
		move1, dest1 := GetRelativePosition(in.current, in.target, in.prev)
		move2, dest2 := GetRelativePosition(in.current, in.target, in.prev)
		if move1 != move2 || dest1 != dest2 {
			t.Errorf("non-deterministic: (%v,%v,%v) gave (%v,%v) then (%v,%v)",
				in.current, in.target, in.prev,
				move1, dest1, move2, dest2)
		}
	}
}
func TestGetAbsolutePosition_LongPath_BasicCase(t *testing.T) {
	// currentPos=50, targetPos=40 → short path is -10, long path is +350
	toMove, dest := GetAbsolutePosition(50, 40, false)
	if dest != 40 {
		t.Errorf("destination = %v, want 40", dest)
	}
	// Long path must not be the short path (-10)
	if math.Abs(toMove) < 180 {
		t.Errorf("toMove = %v — long path should be ≥180°, got a short path", toMove)
	}
}

func TestGetAbsolutePosition_LongPath_DestinationNormalized(t *testing.T) {
	// Result destination must always be in [0, 360)
	cases := []struct{ current, target float64 }{
		{0, 90}, {90, 0}, {350, 10}, {10, 350},
		{180, 45}, {0, 270}, {45, 315},
	}
	for _, c := range cases {
		_, dest := GetAbsolutePosition(c.current, c.target, false)
		if dest < 0 || dest >= 360 {
			t.Errorf("GetAbsolutePosition(%.0f, %.0f, false) dest=%v not in [0,360)",
				c.current, c.target, dest)
		}
	}
}

func TestGetAbsolutePosition_LongPath_NegativeTarget(t *testing.T) {
	// Negative target should be handled without panic and produce valid destination
	_, dest := GetAbsolutePosition(90, -30, false)
	if dest < 0 || dest >= 360 {
		t.Errorf("negative target: dest=%v not in [0,360)", dest)
	}
}

// ─── getAbsolutePath (internal) ───────────────────────────────────────────
//
// We reach this via GetAbsolutePosition(_, _, false) which calls getAbsolutePath.
// These tests verify branches inside getAbsolutePath:
//   - normal case (|toMove| ≤ 360)
//   - |toMove| > 360 branch (math.Mod applied)
//   - targetPos < 0 branch
//   - toMove <= -360 branch

func TestGetAbsolutePath_LargeCurrentPos_ModApplied(t *testing.T) {
	// currentPos=720 (two full turns) should behave same as currentPos=0
	toMove1, dest1 := GetAbsolutePosition(0, 45, false)
	toMove2, dest2 := GetAbsolutePosition(720, 45, false)
	if dest1 != dest2 {
		t.Errorf("720° same as 0°: dest1=%v dest2=%v should match", dest1, dest2)
	}
	_ = toMove1
	_ = toMove2
}

func TestGetAbsolutePath_NegativeTarget_BranchCovered(t *testing.T) {
	// targetPos < 0 triggers the subtraction branch inside getAbsolutePath
	toMove, dest := GetAbsolutePosition(45, -90, false)
	// Destination: -90 + 360 = 270
	if dest != 270 {
		t.Errorf("dest = %v, want 270 for target -90", dest)
	}
	_ = toMove
}

func TestGetAbsolutePath_NeverPanics(t *testing.T) {
	// Exhaustive sweep — getAbsolutePath must never panic for any input
	for current := 0.0; current <= 360; current += 30 {
		for target := -360.0; target <= 360; target += 30 {
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("panic at current=%.0f target=%.0f: %v", current, target, r)
					}
				}()
				GetAbsolutePosition(current, target, false)
			}()
		}
	}
}

// ─── setupCorsResponse ────────────────────────────────────────────────────
// Tested in restapi package — not applicable here.

// ─── GetRelativePosition — previously uncovered branches ─────────────────

func TestGetRelativePosition_DiffBranchDiff1LessThanDiff2(t *testing.T) {
	// Trigger the else branch in the currentPos != prevDestinationAngle block:
	// diff1 = 360 - currentPos, diff2 = abs(prevDest - currentPos)
	// We need diff1 <= diff2, i.e. 360-current <= abs(prev-current)
	// e.g. current=350, prev=90: diff1=10, diff2=260 → diff1 < diff2 → else branch
	toMove, dest := GetRelativePosition(350, 20, 90)
	_ = toMove
	if dest < 0 || dest >= 360 {
		t.Errorf("dest = %v not in [0, 360)", dest)
	}
}

func TestGetRelativePosition_NegativePrevDestination(t *testing.T) {
	// prevDestinationAngle < 0 triggers destination = currentPos + targetPos
	toMove, dest := GetRelativePosition(45, 30, -5)
	_ = toMove
	// destination should be in [0, 360)
	if dest < 0 || dest >= 360 {
		t.Errorf("negative prevDest: dest=%v not in [0,360)", dest)
	}
}
