//go:build unit

package helper

// Tests for float_helper.go — rounding utilities.
//
// These pin the exact rounding behavior, including how the function
// handles the boundary case of exactly 0.5 (rounds away from zero,
// NOT Go's default banker's rounding) and how it handles negatives.
//
// Motivation: RoundFloatTo3 is used throughout position_finder.go
// to scrub float64 noise from motion calculations. If it ever changes
// rounding mode, every motor command position changes too.

import (
	"math"
	"testing"
)

// ─── RoundFloat ────────────────────────────────────────────────────────────

func TestRoundFloat_BasicCases(t *testing.T) {
	tests := []struct {
		name      string
		num       float64
		precision int
		want      float64
	}{
		// Round to whole numbers (precision=0)
		{"3.4 rounds to 3", 3.4, 0, 3},
		{"3.6 rounds to 4", 3.6, 0, 4},
		{"3.5 rounds AWAY from zero to 4", 3.5, 0, 4},
		{"-3.5 rounds AWAY from zero to -4", -3.5, 0, -4},

		// Round to 1 decimal
		{"1.24 to 1dp → 1.2", 1.24, 1, 1.2},
		{"1.25 to 1dp → 1.3 (away from zero)", 1.25, 1, 1.3},
		{"1.26 to 1dp → 1.3", 1.26, 1, 1.3},

		// Round to 3 decimal
		{"1.2344 to 3dp → 1.234", 1.2344, 3, 1.234},
		{"1.2345 to 3dp → 1.235", 1.2345, 3, 1.235},
		{"-1.2345 to 3dp → -1.235", -1.2345, 3, -1.235},

		// Zero
		{"zero stays zero", 0, 3, 0},
		{"negative zero stays zero", -0.0, 3, 0},

		// Already-rounded values should be unchanged
		{"already rounded 1.234 stays 1.234", 1.234, 3, 1.234},
		{"already rounded 100 at precision 3 stays 100", 100, 3, 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RoundFloat(tt.num, tt.precision)
			// Use a tiny epsilon to handle float comparison.
			// The values returned ARE exact in IEEE-754 for these
			// test cases, but using epsilon makes the test robust
			// to representation noise in case the implementation
			// changes.
			if math.Abs(got-tt.want) > 1e-9 {
				t.Errorf("RoundFloat(%v, %d) = %v, want %v",
					tt.num, tt.precision, got, tt.want)
			}
		})
	}
}

// Exhaustive rotation-domain test. The function is heavily used in
// position calculations. Try every tenth of a degree from -360 to 360
// and confirm the result is consistent with a known-good rounding rule.
func TestRoundFloat_RotationDomain(t *testing.T) {
	const precision = 3
	for tenthDeg := -3600; tenthDeg <= 3600; tenthDeg++ {
		input := float64(tenthDeg) / 10.0
		got := RoundFloat(input, precision)

		// For inputs that already have exactly 1 decimal place at most,
		// rounding to 3dp should not change the value.
		if math.Abs(got-input) > 1e-9 {
			t.Errorf("RoundFloat(%v, 3) = %v, expected unchanged",
				input, got)
		}
	}
}

// ─── RoundFloatTo3 ─────────────────────────────────────────────────────────

func TestRoundFloatTo3_DelegatesCorrectly(t *testing.T) {
	// RoundFloatTo3 must be exactly equivalent to RoundFloat(x, 3).
	// This guards against someone "optimizing" RoundFloatTo3 with a
	// different implementation that subtly disagrees on edge cases.
	tests := []float64{
		0, 1.2345, -1.2345, 359.999, 360.0, 0.0005, -0.0005,
		90.0009, 270.0009, 180.5,
	}
	for _, num := range tests {
		got := RoundFloatTo3(num)
		want := RoundFloat(num, 3)
		if got != want {
			t.Errorf("RoundFloatTo3(%v) = %v, but RoundFloat(%v, 3) = %v — must agree",
				num, got, num, want)
		}
	}
}

// Specifically test values that appear in the production logs — pitchErr,
// backlash compensation values that I've seen in real motion logs. These
// values being correctly rounded is what makes the position calculations
// stable.
func TestRoundFloatTo3_ProductionLogValues(t *testing.T) {
	tests := []struct {
		name string
		num  float64
		want float64
	}{
		// From production logs of motor moves:
		{"pitch_error_negative", -0.009, -0.009},
		{"pitch_error_positive", 0.009, 0.009},
		{"compensated_position_artifact", 89.99600000000001, 89.996},
		{"backlash_zero", 0, 0},
		{"position_360_wraparound", 359.991, 359.991},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RoundFloatTo3(tt.num)
			if math.Abs(got-tt.want) > 1e-9 {
				t.Errorf("RoundFloatTo3(%v) = %v, want %v",
					tt.num, got, tt.want)
			}
		})
	}
}

// ─── round (the internal helper) ───────────────────────────────────────────
//
// We test `round` indirectly through RoundFloat above, but a direct test
// of the boundary cases here makes the intent explicit. If `round` is ever
// "optimized" to use math.Round() instead, this test catches the change
// in semantics: math.Round uses banker's rounding, this uses away-from-zero.

func TestRound_HalfAwayFromZero(t *testing.T) {
	tests := []struct {
		name string
		num  float64
		want int
	}{
		{"positive half → up", 0.5, 1},
		{"positive 1.5 → 2", 1.5, 2},
		{"positive 2.5 → 3 (NOT banker's 2)", 2.5, 3},
		{"negative half → down", -0.5, -1},
		{"negative 1.5 → -2", -1.5, -2},
		{"negative 2.5 → -3 (NOT banker's -2)", -2.5, -3},
		{"zero → zero", 0, 0},
		{"positive 0.4 → 0", 0.4, 0},
		{"positive 0.6 → 1", 0.6, 1},
		{"negative 0.4 → 0", -0.4, 0},
		{"negative 0.6 → -1", -0.6, -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := round(tt.num)
			if got != tt.want {
				t.Errorf("round(%v) = %d, want %d", tt.num, got, tt.want)
			}
		})
	}
}
