//go:build unit

package motordriver

// Unit tests for pdo_setup.go, Sections 1-2 (pure Go/YAML, no cgo) plus the
// two guard clauses in setupPDOPositionGeneric that run before it touches
// any C.ecrt_* call. Everything from C.ecrt_master_create_domain onward
// needs a real EtherCAT master and is out of scope here.

import (
	"testing"
)

// withPDOLayoutMap swaps the package-level pdoLayoutMap for the duration of
// a test and restores the previous value (including "never initialized",
// i.e. nil) in t.Cleanup.
func withPDOLayoutMap(t *testing.T, m map[string]*PDOLayout) {
	t.Helper()
	prev := pdoLayoutMap
	pdoLayoutMap = m
	t.Cleanup(func() { pdoLayoutMap = prev })
}

func validRxSM() PDOSyncManager {
	return PDOSyncManager{
		SM: 2, Direction: "output", PDOIndex: 0x1600,
		Entries: []PDOEntry{
			{Index: 0x6040, Subindex: 0, Bits: 16, Name: "ctrl_word"},
			{Index: 0x6060, Subindex: 0, Bits: 8, Name: "op_mode"},
		},
	}
}

func validTxSM() PDOSyncManager {
	return PDOSyncManager{
		SM: 3, Direction: "input", PDOIndex: 0x1A00,
		Entries: []PDOEntry{
			{Index: 0x6041, Subindex: 0, Bits: 16, Name: "status_word"},
			{Index: 0x6064, Subindex: 0, Bits: 32, Name: "actual_pos"},
		},
	}
}

// ─── PDOLayout accessors (RxEntries/TxEntries/RxSM/TxSM) ─────────────────────

func TestPDOLayout_RxEntries_ReturnsOutputSMEntries(t *testing.T) {
	layout := PDOLayout{SyncManagers: []PDOSyncManager{validRxSM(), validTxSM()}}
	entries := layout.RxEntries()
	if len(entries) != 2 || entries[0].Name != "ctrl_word" {
		t.Errorf("RxEntries() = %+v, want 2 entries starting with ctrl_word", entries)
	}
}

func TestPDOLayout_TxEntries_ReturnsInputSMEntries(t *testing.T) {
	layout := PDOLayout{SyncManagers: []PDOSyncManager{validRxSM(), validTxSM()}}
	entries := layout.TxEntries()
	if len(entries) != 2 || entries[0].Name != "status_word" {
		t.Errorf("TxEntries() = %+v, want 2 entries starting with status_word", entries)
	}
}

func TestPDOLayout_RxEntries_NoOutputSM_ReturnsNil(t *testing.T) {
	layout := PDOLayout{SyncManagers: []PDOSyncManager{validTxSM()}}
	if entries := layout.RxEntries(); entries != nil {
		t.Errorf("RxEntries() with no output SM = %+v, want nil", entries)
	}
}

func TestPDOLayout_RxSM_TxSM_FindCorrectDirection(t *testing.T) {
	layout := PDOLayout{SyncManagers: []PDOSyncManager{validRxSM(), validTxSM()}}
	if sm := layout.RxSM(); sm == nil || sm.Direction != "output" {
		t.Errorf("RxSM() = %+v, want direction=output", sm)
	}
	if sm := layout.TxSM(); sm == nil || sm.Direction != "input" {
		t.Errorf("TxSM() = %+v, want direction=input", sm)
	}
}

func TestPDOLayout_RxSM_Missing_ReturnsNil(t *testing.T) {
	layout := PDOLayout{SyncManagers: []PDOSyncManager{validTxSM()}}
	if sm := layout.RxSM(); sm != nil {
		t.Errorf("RxSM() with no output SM = %+v, want nil", sm)
	}
}

// ─── ParsePDOLayout — valid file ──────────────────────────────────────────────

func TestParsePDOLayout_ValidFile_ReturnsLayout(t *testing.T) {
	content := `pdo:
  dc_assign_activate: 768
  sync_managers:
    - sm: 2
      direction: output
      pdo_index: 5632
      entries:
        - index: 24640
          subindex: 0
          bits: 16
          name: ctrl_word
    - sm: 3
      direction: input
      pdo_index: 6656
      entries:
        - index: 24641
          subindex: 0
          bits: 16
          name: status_word
`
	suffix := writeExecRelativeYAML(t, content)

	layout, err := ParsePDOLayout(suffix)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if layout.RxSM() == nil || layout.TxSM() == nil {
		t.Fatalf("layout missing Rx or Tx SM: %+v", layout)
	}
	if len(layout.RxEntries()) != 1 || layout.RxEntries()[0].Name != "ctrl_word" {
		t.Errorf("RxEntries() = %+v", layout.RxEntries())
	}
}

// ─── ParsePDOLayout — missing / malformed / incomplete ────────────────────────

func TestParsePDOLayout_MissingFile_ReturnsError(t *testing.T) {
	_, err := ParsePDOLayout("/nonexistent/drive.yml")
	if err == nil {
		t.Fatal("missing file: expected error, got nil")
	}
}

func TestParsePDOLayout_NoPDOSection_ReturnsError(t *testing.T) {
	suffix := writeExecRelativeYAML(t, "ethercat:\n  operations: []\n")

	_, err := ParsePDOLayout(suffix)
	if err == nil {
		t.Fatal("no pdo: section: expected error, got nil")
	}
}

func TestParsePDOLayout_MissingOutputSyncManager_ReturnsError(t *testing.T) {
	content := `pdo:
  sync_managers:
    - sm: 3
      direction: input
      entries:
        - index: 24641
          subindex: 0
          bits: 16
          name: status_word
`
	suffix := writeExecRelativeYAML(t, content)

	_, err := ParsePDOLayout(suffix)
	if err == nil {
		t.Fatal("missing output (Rx) sync manager: expected error, got nil")
	}
}

func TestParsePDOLayout_MissingInputSyncManager_ReturnsError(t *testing.T) {
	content := `pdo:
  sync_managers:
    - sm: 2
      direction: output
      entries:
        - index: 24640
          subindex: 0
          bits: 16
          name: ctrl_word
`
	suffix := writeExecRelativeYAML(t, content)

	_, err := ParsePDOLayout(suffix)
	if err == nil {
		t.Fatal("missing input (Tx) sync manager: expected error, got nil")
	}
}

func TestParsePDOLayout_EntryWithZeroBits_ReturnsError(t *testing.T) {
	content := `pdo:
  sync_managers:
    - sm: 2
      direction: output
      entries:
        - index: 24640
          subindex: 0
          bits: 0
          name: ctrl_word
    - sm: 3
      direction: input
      entries:
        - index: 24641
          subindex: 0
          bits: 16
          name: status_word
`
	suffix := writeExecRelativeYAML(t, content)

	_, err := ParsePDOLayout(suffix)
	if err == nil {
		t.Fatal("entry with bits=0: expected error, got nil")
	}
}

func TestParsePDOLayout_EntryWithNoName_ReturnsError(t *testing.T) {
	content := `pdo:
  sync_managers:
    - sm: 2
      direction: output
      entries:
        - index: 24640
          subindex: 0
          bits: 16
    - sm: 3
      direction: input
      entries:
        - index: 24641
          subindex: 0
          bits: 16
          name: status_word
`
	suffix := writeExecRelativeYAML(t, content)

	_, err := ParsePDOLayout(suffix)
	if err == nil {
		t.Fatal("entry with empty name: expected error, got nil")
	}
}

func TestParsePDOLayout_MalformedYAML_ReturnsError(t *testing.T) {
	suffix := writeExecRelativeYAML(t, "pdo: [this is not: valid")

	_, err := ParsePDOLayout(suffix)
	if err == nil {
		t.Fatal("malformed YAML: expected error, got nil")
	}
}

// ─── ValidatePDOLayout — known-name checking, duplicates, padding ─────────────

func TestValidatePDOLayout_AllKnownNames_ReturnsNoProblems(t *testing.T) {
	layout := &PDOLayout{SyncManagers: []PDOSyncManager{validRxSM(), validTxSM()}}
	problems := ValidatePDOLayout(layout)
	if len(problems) != 0 {
		t.Errorf("expected no problems, got %v", problems)
	}
}

func TestValidatePDOLayout_UnknownName_IsFlagged(t *testing.T) {
	layout := &PDOLayout{SyncManagers: []PDOSyncManager{
		{SM: 2, Direction: "output", Entries: []PDOEntry{
			{Index: 0x6040, Name: "ctrl_wrd"}, // typo
		}},
	}}
	problems := ValidatePDOLayout(layout)
	if len(problems) != 1 {
		t.Fatalf("expected 1 problem for unknown name, got %v", problems)
	}
}

func TestValidatePDOLayout_PaddingNamesAreAlwaysValid(t *testing.T) {
	layout := &PDOLayout{SyncManagers: []PDOSyncManager{
		{SM: 2, Direction: "output", Entries: []PDOEntry{
			{Index: 0x0000, Name: "_pad1"},
			{Index: 0x0000, Name: "_anything_starting_with_underscore"},
		}},
	}}
	problems := ValidatePDOLayout(layout)
	if len(problems) != 0 {
		t.Errorf("padding entries should never be flagged, got %v", problems)
	}
}

func TestValidatePDOLayout_DuplicateName_IsFlagged(t *testing.T) {
	layout := &PDOLayout{SyncManagers: []PDOSyncManager{
		{SM: 2, Direction: "output", Entries: []PDOEntry{
			{Index: 0x6040, Name: "ctrl_word"},
			{Index: 0x6041, Name: "ctrl_word"}, // duplicate name, different index
		}},
	}}
	problems := ValidatePDOLayout(layout)
	if len(problems) != 1 {
		t.Fatalf("expected 1 duplicate-name problem, got %v", problems)
	}
}

func TestValidatePDOLayout_AllDocumentedFieldsAreKnown(t *testing.T) {
	// Every field name that pdo_setup.go documents as a valid SlaveOffsets
	// mapping must round-trip through ValidatePDOLayout without complaint.
	// This is a regression guard for the exact bug described in the FIX
	// (Bug 4) comment: a real, valid entry getting flagged as a typo.
	names := []string{
		"ctrl_word", "op_mode", "target_pos", "target_vel",
		"dig_out_mask", "dig_out_val",
		"error_code", "status_word", "op_mode_disp", "actual_pos",
		"actual_vel", "touch_stat", "touch_pos1", "following_err", "digital_in",
	}
	var entries []PDOEntry
	for i, n := range names {
		entries = append(entries, PDOEntry{Index: uint16(0x6000 + i), Name: n, Bits: 16})
	}
	layout := &PDOLayout{SyncManagers: []PDOSyncManager{{SM: 2, Direction: "output", Entries: entries}}}

	problems := ValidatePDOLayout(layout)
	if len(problems) != 0 {
		t.Errorf("all-documented-names layout flagged as invalid: %v", problems)
	}
}

// ─── GetPDOLayout — map lookup ────────────────────────────────────────────────

func TestGetPDOLayout_NilMap_ReturnsNil(t *testing.T) {
	withPDOLayoutMap(t, nil)
	if got := GetPDOLayout("delta_asda2e"); got != nil {
		t.Errorf("GetPDOLayout on nil map = %+v, want nil", got)
	}
}

func TestGetPDOLayout_UnknownConfigName_ReturnsNil(t *testing.T) {
	withPDOLayoutMap(t, map[string]*PDOLayout{
		"delta_asda2e": {SyncManagers: []PDOSyncManager{validRxSM(), validTxSM()}},
	})
	if got := GetPDOLayout("a6minas"); got != nil {
		t.Errorf("GetPDOLayout(unregistered name) = %+v, want nil", got)
	}
}

func TestGetPDOLayout_MultipleDrives_ReturnsCorrectOne(t *testing.T) {
	deltaLayout := &PDOLayout{DCAssignActivate: 0, SyncManagers: []PDOSyncManager{validRxSM(), validTxSM()}}
	a6Layout := &PDOLayout{DCAssignActivate: 0x0300, SyncManagers: []PDOSyncManager{validRxSM(), validTxSM()}}
	withPDOLayoutMap(t, map[string]*PDOLayout{
		"delta_asda2e": deltaLayout,
		"a6minas":      a6Layout,
	})

	if got := GetPDOLayout("delta_asda2e"); got != deltaLayout {
		t.Error("GetPDOLayout(delta_asda2e) did not return the Delta layout")
	}
	if got := GetPDOLayout("a6minas"); got != a6Layout {
		t.Error("GetPDOLayout(a6minas) did not return the A6 layout")
	}
	if got := GetPDOLayout("a6minas"); got.DCAssignActivate != 0x0300 {
		t.Errorf("a6minas DCAssignActivate = 0x%04X, want 0x0300", got.DCAssignActivate)
	}
}

// ─── SetupPDOPosition — nil Driver guard ──────────────────────────────────────

func TestSetupPDOPosition_NilDriver_ReturnsError(t *testing.T) {
	d := stubDevice("A")
	d.Driver = nil
	if err := SetupPDOPosition(d); err == nil {
		t.Fatal("nil Driver: expected error, got nil")
	}
}

func TestSetupPDOPosition_DelegatesToDriverSetupPDO(t *testing.T) {
	d := stubDevice("A")
	mock := &mockDriver{}
	d.Driver = mock

	// Confirm SetupPDOPosition really calls through to Driver.SetupPDO
	// rather than short-circuiting, by making the mock return a distinct
	// sentinel error and checking it comes back unchanged.
	sentinel := errSentinel{}
	mock.setupPDOErr = sentinel

	err := SetupPDOPosition(d)
	if err != sentinel {
		t.Errorf("SetupPDOPosition did not delegate to Driver.SetupPDO: got %v, want sentinel", err)
	}
}

type errSentinel struct{}

func (errSentinel) Error() string { return "sentinel setupPDOErr" }

// ─── setupPDOPositionGeneric — pre-cgo guard clauses ──────────────────────────

func TestSetupPDOPositionGeneric_NoPDOLayoutForConfig_ReturnsError(t *testing.T) {
	withPDOLayoutMap(t, map[string]*PDOLayout{}) // registered but empty
	d := stubDevice("A")
	d.Device.AddressConfigName = "unregistered_drive"

	err := setupPDOPositionGeneric(d)
	if err == nil {
		t.Fatal("no PDO layout registered for this AddressConfigName: expected error, got nil")
	}
}

func TestSetupPDOPositionGeneric_EmptyRxEntries_ReturnsError(t *testing.T) {
	withPDOLayoutMap(t, map[string]*PDOLayout{
		"stubdrive": {SyncManagers: []PDOSyncManager{
			{SM: 2, Direction: "output", Entries: nil}, // Rx present but empty
			validTxSM(),
		}},
	})
	d := stubDevice("A")
	d.Device.AddressConfigName = "stubdrive"

	err := setupPDOPositionGeneric(d)
	if err == nil {
		t.Fatal("empty Rx entry list: expected error, got nil")
	}
}

func TestSetupPDOPositionGeneric_EmptyTxEntries_ReturnsError(t *testing.T) {
	withPDOLayoutMap(t, map[string]*PDOLayout{
		"stubdrive": {SyncManagers: []PDOSyncManager{
			validRxSM(),
			{SM: 3, Direction: "input", Entries: nil}, // Tx present but empty
		}},
	})
	d := stubDevice("A")
	d.Device.AddressConfigName = "stubdrive"

	err := setupPDOPositionGeneric(d)
	if err == nil {
		t.Fatal("empty Tx entry list: expected error, got nil")
	}
}
