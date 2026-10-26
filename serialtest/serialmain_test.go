package serialtest

// Tests for serialtest's pure RS232 batch-command parsing/normalization
// logic: parseBatchCommands, splitConcatenatedBatch, normalizeControllerToken,
// insertSpacesBeforeLetters, isAxisKey, findInsertBeforeEnd.
//
// No build tag: serialmain.go imports EtherCAT/motordriver (cgo), so the
// whole package requires the real IgH EtherCAT toolchain to compile —
// same limitation as executors/restapi/systemupdate elsewhere in this
// project. Could not be verified in a sandbox; the functions tested here
// are pure string manipulation with zero actual dependency on motordriver
// or real hardware.

import (
	"reflect"
	"testing"
)

// ─── sanitizeRS232Frame ────────────────────────────────────────────────────

func TestSanitizeRS232Frame_FanucHeaderPaddingAndSpaces(t *testing.T) {
	got := sanitizeRS232Frame("\x00\x1b&HE: B   10\t")
	if got != "B10" {
		t.Errorf("sanitizeRS232Frame() = %q, want %q", got, "B10")
	}
}

func TestSanitizeRS232Frame_PreservesSignedDecimalCommand(t *testing.T) {
	got := sanitizeRS232Frame("  A -10.250  ")
	if got != "A-10.250" {
		t.Errorf("sanitizeRS232Frame() = %q, want %q", got, "A-10.250")
	}
}

func TestSanitizeRS232Frame_ControlBytesOnlyReturnsEmpty(t *testing.T) {
	if got := sanitizeRS232Frame("\x00\x12\x14\x1b"); got != "" {
		t.Errorf("sanitizeRS232Frame(control bytes) = %q, want empty", got)
	}
}

// ─── parseBatchCommands ─────────────────────────────────────────────────────

func TestParseBatchCommands_EmptyString_ReturnsNil(t *testing.T) {
	got := parseBatchCommands("")
	if got != nil {
		t.Errorf("parseBatchCommands('') = %v, want nil", got)
	}
}

func TestParseBatchCommands_WhitespaceOnly_ReturnsNil(t *testing.T) {
	got := parseBatchCommands("   \t  ")
	if got != nil {
		t.Errorf("parseBatchCommands(whitespace) = %v, want nil", got)
	}
}

func TestParseBatchCommands_SemicolonSeparated_MultipleCommands(t *testing.T) {
	got := parseBatchCommands("G90;G91;M30")
	want := []string{"G90", "G91", "M30"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseBatchCommands('G90;G91;M30') = %v, want %v", got, want)
	}
}

func TestParseBatchCommands_SingleTrailingSemicolon_CommaSeparated(t *testing.T) {
	got := parseBatchCommands("go1f20,g91,g68,a90;")
	want := []string{"go1f20", "g91", "g68", "a90"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseBatchCommands(comma-batch) = %v, want %v", got, want)
	}
}

func TestParseBatchCommands_SpaceBatch_GroupsParamsWithCommand(t *testing.T) {
	got := parseBatchCommands("G01 F20 G91 G68 A90")
	want := []string{"G01 F20", "G91", "G68", "A90"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseBatchCommands(space-batch) = %v, want %v", got, want)
	}
}

func TestParseBatchCommands_ConcatenatedFallsThroughToSplitConcatenated(t *testing.T) {
	got := parseBatchCommands("G01F20G91G68A90")
	want := []string{"G01F20", "G91", "G68", "A90"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseBatchCommands(concatenated) = %v, want %v", got, want)
	}
}

func TestParseBatchCommands_SingleCommandNoBatching(t *testing.T) {
	got := parseBatchCommands("G90;")
	want := []string{"G90"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseBatchCommands('G90;') = %v, want %v", got, want)
	}
}

// ─── splitConcatenatedBatch ─────────────────────────────────────────────────

func TestSplitConcatenatedBatch_EmptyString_ReturnsNil(t *testing.T) {
	got := splitConcatenatedBatch("")
	if got != nil {
		t.Errorf("splitConcatenatedBatch('') = %v, want nil", got)
	}
}

func TestSplitConcatenatedBatch_DocstringExample(t *testing.T) {
	got := splitConcatenatedBatch("G01F20G91G68A90")
	want := []string{"G01F20", "G91", "G68", "A90"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("splitConcatenatedBatch() = %v, want %v", got, want)
	}
}

func TestSplitConcatenatedBatch_SingleGCommand(t *testing.T) {
	got := splitConcatenatedBatch("G90")
	want := []string{"G90"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("splitConcatenatedBatch('G90') = %v, want %v", got, want)
	}
}

func TestSplitConcatenatedBatch_AxisVarOnly(t *testing.T) {
	got := splitConcatenatedBatch("A90")
	want := []string{"A90"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("splitConcatenatedBatch('A90') = %v, want %v", got, want)
	}
}

func TestSplitConcatenatedBatch_IgnoresLeadingWhitespace(t *testing.T) {
	got := splitConcatenatedBatch("  G90")
	want := []string{"G90"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("splitConcatenatedBatch('  G90') = %v, want %v", got, want)
	}
}

func TestSplitConcatenatedBatch_MultipleAxisVars(t *testing.T) {
	got := splitConcatenatedBatch("A90B45D10")
	want := []string{"A90", "B45", "D10"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("splitConcatenatedBatch('A90B45D10') = %v, want %v", got, want)
	}
}

func TestSplitConcatenatedBatch_UnknownPrintablePrefixIsRejected(t *testing.T) {
	if got := splitConcatenatedBatch("QBADB10"); got != nil {
		t.Errorf("splitConcatenatedBatch(invalid prefix) = %v, want nil", got)
	}
}

// ─── normalizeControllerToken ───────────────────────────────────────────────

func TestNormalizeControllerToken_EmptyString(t *testing.T) {
	got := normalizeControllerToken("")
	if got != "" {
		t.Errorf("normalizeControllerToken('') = %q, want empty", got)
	}
}

func TestNormalizeControllerToken_LegacyNumericOnly(t *testing.T) {
	got := normalizeControllerToken("45")
	want := "A45;"
	if got != want {
		t.Errorf("normalizeControllerToken('45') = %q, want %q", got, want)
	}
}

func TestNormalizeControllerToken_NegativeNumericOnly(t *testing.T) {
	got := normalizeControllerToken("-45.5")
	want := "A-45.5;"
	if got != want {
		t.Errorf("normalizeControllerToken('-45.5') = %q, want %q", got, want)
	}
}

func TestNormalizeControllerToken_GCommandWithParam(t *testing.T) {
	got := normalizeControllerToken("G01F20")
	want := "G01 F20;"
	if got != want {
		t.Errorf("normalizeControllerToken('G01F20') = %q, want %q", got, want)
	}
}

func TestNormalizeControllerToken_SimpleGCommand(t *testing.T) {
	got := normalizeControllerToken("G91")
	want := "G91;"
	if got != want {
		t.Errorf("normalizeControllerToken('G91') = %q, want %q", got, want)
	}
}

func TestNormalizeControllerToken_AxisCommand(t *testing.T) {
	got := normalizeControllerToken("A90")
	want := "A90;"
	if got != want {
		t.Errorf("normalizeControllerToken('A90') = %q, want %q", got, want)
	}
}

func TestNormalizeControllerToken_SpacedAxisValue(t *testing.T) {
	got := normalizeControllerToken("B 10")
	if got != "B10;" {
		t.Errorf("normalizeControllerToken('B 10') = %q, want %q", got, "B10;")
	}
}

func TestNormalizeControllerToken_MalformedAxisValueRejected(t *testing.T) {
	if got := normalizeControllerToken("BABC"); got != "" {
		t.Errorf("normalizeControllerToken('BABC') = %q, want empty", got)
	}
}

func TestNormalizeControllerToken_LetterOTypo_CorrectedToZero(t *testing.T) {
	// "GO1" (letter O) is a common typo/OCR-style error for "G01" (digit 0).
	got := normalizeControllerToken("GO1")
	want := "G01;"
	if got != want {
		t.Errorf("normalizeControllerToken('GO1') = %q, want %q", got, want)
	}
}

func TestNormalizeControllerToken_AlreadyHasTerminator(t *testing.T) {
	got := normalizeControllerToken("G91;")
	want := "G91;"
	if got != want {
		t.Errorf("normalizeControllerToken('G91;') = %q, want %q", got, want)
	}
}

func TestNormalizeControllerToken_CommaReplacedWithSpace(t *testing.T) {
	got := normalizeControllerToken("G01,F20")
	want := "G01 F20;"
	if got != want {
		t.Errorf("normalizeControllerToken('G01,F20') = %q, want %q", got, want)
	}
}

func TestNormalizeControllerToken_LowercaseNormalizedToUpper(t *testing.T) {
	got := normalizeControllerToken("g91")
	want := "G91;"
	if got != want {
		t.Errorf("normalizeControllerToken('g91') = %q, want %q", got, want)
	}
}

// ─── insertSpacesBeforeLetters ──────────────────────────────────────────────

func TestInsertSpacesBeforeLetters_DigitThenLetter(t *testing.T) {
	got := insertSpacesBeforeLetters("G01F20")
	want := "G01 F20"
	if got != want {
		t.Errorf("insertSpacesBeforeLetters('G01F20') = %q, want %q", got, want)
	}
}

func TestInsertSpacesBeforeLetters_NoDigitLetterBoundary(t *testing.T) {
	got := insertSpacesBeforeLetters("G91")
	want := "G91"
	if got != want {
		t.Errorf("insertSpacesBeforeLetters('G91') = %q, want %q", got, want)
	}
}

func TestInsertSpacesBeforeLetters_MultipleBoundaries(t *testing.T) {
	got := insertSpacesBeforeLetters("G01F20I5J10")
	want := "G01 F20 I5 J10"
	if got != want {
		t.Errorf("insertSpacesBeforeLetters('G01F20I5J10') = %q, want %q", got, want)
	}
}

func TestInsertSpacesBeforeLetters_EmptyString(t *testing.T) {
	got := insertSpacesBeforeLetters("")
	if got != "" {
		t.Errorf("insertSpacesBeforeLetters('') = %q, want empty", got)
	}
}

func TestInsertSpacesBeforeLetters_LeadingLetterNoSpaceInserted(t *testing.T) {
	// i==0 case is explicitly excluded from the digit-then-letter check.
	got := insertSpacesBeforeLetters("G1")
	want := "G1"
	if got != want {
		t.Errorf("insertSpacesBeforeLetters('G1') = %q, want %q", got, want)
	}
}

// ─── isAxisKey ───────────────────────────────────────────────────────────────

func TestIsAxisKey_ValidAxisLetters(t *testing.T) {
	for _, k := range []string{"A", "B", "X", "Y", "Z", "D", "A90", "X10.5"} {
		if !isAxisKey(k) {
			t.Errorf("isAxisKey(%q) = false, want true", k)
		}
	}
}

func TestIsAxisKey_NonAxisLetters(t *testing.T) {
	for _, k := range []string{"G", "M", "F", "G90", "M30"} {
		if isAxisKey(k) {
			t.Errorf("isAxisKey(%q) = true, want false", k)
		}
	}
}

func TestIsAxisKey_EmptyString(t *testing.T) {
	if isAxisKey("") {
		t.Error("isAxisKey('') = true, want false")
	}
}

// ─── findInsertBeforeEnd ────────────────────────────────────────────────────

func TestFindInsertBeforeEnd_FindsM99(t *testing.T) {
	lines := []string{"G90;", "G01 F20;", "M99;"}
	got := findInsertBeforeEnd(lines)
	if got != 2 {
		t.Errorf("findInsertBeforeEnd = %d, want 2 (index of M99)", got)
	}
}

func TestFindInsertBeforeEnd_FindsM30(t *testing.T) {
	lines := []string{"G90;", "M30;"}
	got := findInsertBeforeEnd(lines)
	if got != 1 {
		t.Errorf("findInsertBeforeEnd = %d, want 1 (index of M30)", got)
	}
}

func TestFindInsertBeforeEnd_NoTerminator_ReturnsLength(t *testing.T) {
	lines := []string{"G90;", "G01 F20;"}
	got := findInsertBeforeEnd(lines)
	if got != len(lines) {
		t.Errorf("findInsertBeforeEnd = %d, want %d (end of file)", got, len(lines))
	}
}

func TestFindInsertBeforeEnd_EmptyLines_ReturnsZero(t *testing.T) {
	got := findInsertBeforeEnd([]string{})
	if got != 0 {
		t.Errorf("findInsertBeforeEnd([]) = %d, want 0", got)
	}
}

func TestFindInsertBeforeEnd_IndentedTerminator_StillFound(t *testing.T) {
	lines := []string{"G90;", "   M99;"}
	got := findInsertBeforeEnd(lines)
	if got != 1 {
		t.Errorf("findInsertBeforeEnd with indented M99 = %d, want 1", got)
	}
}

func TestFindInsertBeforeEnd_FirstMatchWins(t *testing.T) {
	lines := []string{"M30;", "M99;"}
	got := findInsertBeforeEnd(lines)
	if got != 0 {
		t.Errorf("findInsertBeforeEnd = %d, want 0 (first M30 match)", got)
	}
}
