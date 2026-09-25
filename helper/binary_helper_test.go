//go:build unit

package helper

// Tests for binary_helper.go — integer to reversed-binary-string conversion.
//
// IntToBinary returns the binary representation of an integer as a string
// in REVERSED order. The reversal lets callers index by bit position:
//   inBinary[0] = bit 0 (least significant)
//   inBinary[1] = bit 1
//   ...
// instead of the natural "left-most digit is most significant" order.
//
// The implementation uses fmt.Sprintf("%032b", value) which produces a
// 32-character zero-padded binary string for non-negative ints. For
// NEGATIVE ints, %b produces a "-" sign prefix (sign-magnitude format,
// NOT two's complement). These tests document and pin that behavior.

import (
	"testing"
)

// ─── IntToBinary ───────────────────────────────────────────────────────────

func TestIntToBinary_BasicCases(t *testing.T) {
	tests := []struct {
		name  string
		value int
		want  string
	}{
		// Zero: 32 zeros, reversal is also 32 zeros
		{"zero", 0, "00000000000000000000000000000000"},

		// Small positive values — natural binary, then reversed.
		// 1 → "00000000000000000000000000000001" → reversed → "1000...0"
		{"one", 1, "10000000000000000000000000000000"},
		{"two", 2, "01000000000000000000000000000000"},
		{"three", 3, "11000000000000000000000000000000"},
		{"four", 4, "00100000000000000000000000000000"},

		// 8 = 0b1000 → reversed bit3=1
		{"eight", 8, "00010000000000000000000000000000"},

		// 0xFF = 255 = lowest 8 bits set
		{"two hundred fifty five", 255, "11111111000000000000000000000000"},

		// 0x100 = 256 = bit 8 set
		{"two hundred fifty six", 256, "00000000100000000000000000000000"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IntToBinary(tt.value)
			if got != tt.want {
				t.Errorf("IntToBinary(%d):\n  got  %q\n  want %q",
					tt.value, got, tt.want)
			}
		})
	}
}

// Length contract: the result should always be exactly 32 characters
// for non-negative inputs (since "%032b" pads to 32 chars). This test
// guards against someone changing the format string.
func TestIntToBinary_AlwaysProduces32CharsForNonNegative(t *testing.T) {
	tests := []int{0, 1, 100, 1000, 65535, 65536, 2147483647}
	for _, val := range tests {
		got := IntToBinary(val)
		if len(got) != 32 {
			t.Errorf("IntToBinary(%d) length = %d, want 32 (got %q)",
				val, len(got), got)
		}
	}
}

// Bit-position indexing: the whole point of the reversed format is that
// inBinary[N] should return '1' if bit N is set in the input, '0' otherwise.
// This test verifies that contract directly.
func TestIntToBinary_BitPositionIndexing(t *testing.T) {
	// 0b00010100 = 20 — bits 2 and 4 are set
	binary := IntToBinary(20)

	expectedSet := map[int]bool{2: true, 4: true}

	for i := 0; i < 32; i++ {
		expected := byte('0')
		if expectedSet[i] {
			expected = '1'
		}
		if binary[i] != expected {
			t.Errorf("IntToBinary(20)[%d] = %q, want %q (binary=%q)",
				i, binary[i], expected, binary)
		}
	}
}

// Negative inputs: this test pins the CURRENT, surprising behavior of
// IntToBinary for negative integers. Go's fmt.Sprintf("%032b", -1) uses
// the width specifier (32) as the TOTAL FIELD WIDTH including the sign
// character, NOT the number of binary digits. So:
//
//	fmt.Sprintf("%032b", -1)  →  "-0000000000000000000000000000001"
//	                             (31 zero-padded digits, then '1', prefixed
//	                              with '-' for a total of 32 chars)
//
// After reverse() that becomes:
//
//	"1000000000000000000000000000000-"
//
// This means TWO things go wrong for negative inputs:
//  1. The bit-position indexing contract is broken: binary[0] = '1'
//     for both IntToBinary(1) AND IntToBinary(-1) — same answer for
//     different inputs.
//  2. The final character is '-', not '0' or '1', so any caller doing
//     `if binary[31] == '1'` will silently get the wrong answer.
//
// THIS IS A LATENT BUG in the production code. It is NOT currently
// triggering any visible misbehavior because no caller in the codebase
// passes negative ints to IntToBinary. The function is used for parsing
// statusword/controlword bits, which are uint16 values that never go
// negative.
//
// We pin this behavior here so future maintainers see it clearly. If the
// function is ever fixed to use uint32(value) for two's-complement output,
// this test will fail and the comment block in binary_helper.go can be
// removed.
func TestIntToBinary_NegativeInput_TwosComplement(t *testing.T) {
	// IntToBinary now uses uint32(value), giving two's-complement output.
	// uint32(-1) = 0xFFFFFFFF = all 32 bits set.
	// Reversed: still all 32 ones (palindrome).
	got := IntToBinary(-1)
	want := "11111111111111111111111111111111"
	if got != want {
		t.Errorf("IntToBinary(-1) = %q, want %q (two's complement)", got, want)
	}
}

func TestIntToBinary_NegativeTwo_TwosComplement(t *testing.T) {
	// uint32(-2) = 0xFFFFFFFE = all bits set except bit 1.
	// Binary: 11111111111111111111111111111110
	// Reversed: 01111111111111111111111111111111
	got := IntToBinary(-2)
	want := "01111111111111111111111111111111"
	if got != want {
		t.Errorf("IntToBinary(-2) = %q, want %q", got, want)
	}
}

// ─── reverse ───────────────────────────────────────────────────────────────
//
// reverse is internal but worth testing directly because IntToBinary
// relies on it for correctness, and edge cases like empty strings or
// single characters could surprise the algorithm.

func TestReverse(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"single char", "a", "a"},
		{"two chars", "ab", "ba"},
		{"abc", "abc", "cba"},
		{"palindrome", "aba", "aba"},
		{"with spaces", "a b c", "c b a"},
		{"32-char binary all zeros", "00000000000000000000000000000000",
			"00000000000000000000000000000000"},
		{"32-char binary mixed",
			"00000000000000000000000000000001",
			"10000000000000000000000000000000"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reverse(tt.in)
			if got != tt.want {
				t.Errorf("reverse(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// reverse is idempotent: reversing twice should yield the original.
func TestReverse_IdempotentWhenDoubled(t *testing.T) {
	inputs := []string{
		"", "a", "hello", "01010", "abc 123",
		"00000000000000000000000000000001",
	}
	for _, in := range inputs {
		got := reverse(reverse(in))
		if got != in {
			t.Errorf("reverse(reverse(%q)) = %q, want %q", in, got, in)
		}
	}
}
