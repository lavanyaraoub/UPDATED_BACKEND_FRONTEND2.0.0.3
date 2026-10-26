//go:build unit

package motordriver

// Tests for poll_driver_alarm.go's error-lookup and fault-history subsystem.
//
// THIS IS NEW FUNCTIONALITY WITH NO TESTENV EQUIVALENT — testenv's
// handleErrCode was a 2-parameter suppression filter only; HAL added an
// entire error-code-description lookup (with Nidec M700 normalization) and
// a 20-entry fault-history ring buffer persisted to disk. These tests are
// written fresh against HAL's actual behavior, not ported from anywhere.
//
// IMPORTANT SAFETY NOTE — real file, real data:
// RecordFault/persistFaultHistory write to a HARDCODED path,
// /var/tmp/ethercat_fault_history.json — not a test temp dir. On a real
// deployed Pi this file may contain genuine historical fault records from
// actual hardware operation. A careless test that calls ClearFaultHistory()
// or overwrites this file would destroy that data permanently.
//
// To avoid this, every test that touches the fault history uses
// snapshotFaultHistory()/restore(), which saves and restores BOTH the
// in-memory ring (faultRing/faultRingHead/faultRingCount, all
// package-private and directly accessible from this test file) AND the
// on-disk file's exact original bytes, byte-for-byte, regardless of what
// the test does in between. This is stricter than typical test cleanup
// specifically because the blast radius of getting it wrong is real data
// loss on a real machine.

import (
	"os"
	"testing"

	channels "EtherCAT/channels"
	"EtherCAT/motordriver/statusnotifier"
)

// ─── normalizeErrorCode ─────────────────────────────────────────────────
//
// Pure function, no shared state — safe to test directly with no setup.

func TestNormalizeErrorCode_PlainCodePassesThrough(t *testing.T) {
	// A6 Minas / Delta ASDA use plain codes (high byte = 0).
	got := normalizeErrorCode(87)
	if got != 87 {
		t.Errorf("normalizeErrorCode(87) = %d, want 87 (unchanged)", got)
	}
}

func TestNormalizeErrorCode_M700EncodedAlarm(t *testing.T) {
	// Nidec M700 encodes alarm 87 as 0xFF57 = 65367.
	got := normalizeErrorCode(0xFF57)
	if got != 87 {
		t.Errorf("normalizeErrorCode(0xFF57) = %d, want 87", got)
	}
}

func TestNormalizeErrorCode_M700EncodedAlarmOne(t *testing.T) {
	// Alarm 1 → 0xFF01 = 65281.
	got := normalizeErrorCode(0xFF01)
	if got != 1 {
		t.Errorf("normalizeErrorCode(0xFF01) = %d, want 1", got)
	}
}

func TestNormalizeErrorCode_ZeroPassesThrough(t *testing.T) {
	got := normalizeErrorCode(0)
	if got != 0 {
		t.Errorf("normalizeErrorCode(0) = %d, want 0", got)
	}
}

func TestNormalizeErrorCode_HighByteNotFF_PassesThrough(t *testing.T) {
	// High byte 0x12 (not 0xFF) — some other vendor's encoding, or garbage.
	// Must NOT be treated as M700-encoded.
	got := normalizeErrorCode(0x1234)
	if got != 0x1234 {
		t.Errorf("normalizeErrorCode(0x1234) = %d, want 0x1234 (unchanged, high byte != 0xFF)", got)
	}
}

// ─── LookupErrorDescription / FormatErrorCode ────────────────────────────
//
// NOTE ON sync.Once: errorDescOnce ensures loadErrorDescriptions runs
// exactly once per test binary run. In this test binary's working
// directory (the motordriver/ package dir), "configs/error_definition.txt"
// (a path relative to the repo root) will not resolve, so
// loadErrorDescriptions logs a warning and leaves errorDescriptions empty
// — every code looks "unknown" for the rest of this test binary's run.
// This is a stable, deterministic condition to test against; it is NOT
// testing the same thing as running the real binary from the repo root
// (where the file does resolve and real descriptions load).

func TestLookupErrorDescription_UnresolvableConfigFile_ReturnsUnknownFormat(t *testing.T) {
	got := LookupErrorDescription(87)
	want := "Unknown error code 87 (not in error_definition.txt)"
	if got != want {
		t.Errorf("LookupErrorDescription(87) = %q, want %q", got, want)
	}
}

func TestLookupErrorDescription_M700EncodedCode_NormalizesBeforeLookup(t *testing.T) {
	// Even though the description isn't found (see note above), the NUMBER
	// reported in the "unknown" message must be the normalized alarm ID
	// (87), not the raw M700-encoded value (65367) — otherwise operators
	// see a meaningless 5-digit number instead of the real alarm ID.
	got := LookupErrorDescription(0xFF57)
	want := "Unknown error code 87 (not in error_definition.txt)"
	if got != want {
		t.Errorf("LookupErrorDescription(0xFF57) = %q, want %q (normalized alarm ID)", got, want)
	}
}

func TestFormatErrorCode_CombinesNormalizedIDAndDescription(t *testing.T) {
	got := FormatErrorCode(87)
	want := "Error 87: Unknown error code 87 (not in error_definition.txt)"
	if got != want {
		t.Errorf("FormatErrorCode(87) = %q, want %q", got, want)
	}
}

func TestFormatErrorCode_M700EncodedCode_DisplaysNormalizedID(t *testing.T) {
	got := FormatErrorCode(0xFF57)
	want := "Error 87: Unknown error code 87 (not in error_definition.txt)"
	if got != want {
		t.Errorf("FormatErrorCode(0xFF57) = %q, want %q (displays 87, not 65367)", got, want)
	}
}

// ─── Fault history ring buffer ────────────────────────────────────────────
//
// See file-level comment: every test here snapshots and restores both the
// in-memory ring and the real on-disk file byte-for-byte.

// faultHistorySnapshot captures the complete state needed to restore the
// fault history exactly as it was before a test ran.
type faultHistorySnapshot struct {
	ring        [faultHistoryCapacity]FaultEntry
	head        int
	count       int
	fileBytes   []byte // nil if the file did not exist
	fileExisted bool
}

func snapshotFaultHistory(t *testing.T) faultHistorySnapshot {
	t.Helper()
	faultHistoryMu.RLock()
	snap := faultHistorySnapshot{
		ring:  faultRing,
		head:  faultRingHead,
		count: faultRingCount,
	}
	faultHistoryMu.RUnlock()

	data, err := os.ReadFile(faultHistoryFile)
	if err == nil {
		snap.fileBytes = data
		snap.fileExisted = true
	}
	return snap
}

func (s faultHistorySnapshot) restore(t *testing.T) {
	t.Helper()
	faultHistoryMu.Lock()
	faultRing = s.ring
	faultRingHead = s.head
	faultRingCount = s.count
	faultHistoryMu.Unlock()

	if s.fileExisted {
		if err := os.WriteFile(faultHistoryFile, s.fileBytes, 0644); err != nil {
			t.Errorf("CRITICAL: failed to restore original fault history file: %v", err)
		}
	} else {
		// File did not exist before the test — remove whatever the test created.
		_ = os.Remove(faultHistoryFile)
	}
}

func TestRecordFault_AddsEntryToRing(t *testing.T) {
	snap := snapshotFaultHistory(t)
	defer snap.restore(t)

	faultHistoryMu.Lock()
	faultRingHead = 0
	faultRingCount = 0
	faultHistoryMu.Unlock()

	RecordFault("TESTDEVICE", 87, 0x0637)

	history := GetFaultHistory()
	if len(history) != 1 {
		t.Fatalf("GetFaultHistory() len = %d, want 1", len(history))
	}
	if history[0].DeviceName != "TESTDEVICE" {
		t.Errorf("DeviceName = %q, want TESTDEVICE", history[0].DeviceName)
	}
	if history[0].ErrorCode != 87 {
		t.Errorf("ErrorCode = %d, want 87", history[0].ErrorCode)
	}
	if history[0].Statusword != 0x0637 {
		t.Errorf("Statusword = 0x%04X, want 0x0637", history[0].Statusword)
	}
}

func TestRecordFault_DeduplicatesConsecutiveIdenticalEntries(t *testing.T) {
	snap := snapshotFaultHistory(t)
	defer snap.restore(t)

	faultHistoryMu.Lock()
	faultRingHead = 0
	faultRingCount = 0
	faultHistoryMu.Unlock()

	// Same device + same error code, fired 3 times in a row (simulating
	// repeated poll ticks while the drive sits in fault state).
	RecordFault("TESTDEVICE", 87, 0x0637)
	RecordFault("TESTDEVICE", 87, 0x0637)
	RecordFault("TESTDEVICE", 87, 0x0637)

	history := GetFaultHistory()
	if len(history) != 1 {
		t.Errorf("GetFaultHistory() len = %d, want 1 (consecutive duplicates deduplicated)", len(history))
	}
}

func TestRecordFault_DifferentErrorCodeIsNotDeduplicated(t *testing.T) {
	snap := snapshotFaultHistory(t)
	defer snap.restore(t)

	faultHistoryMu.Lock()
	faultRingHead = 0
	faultRingCount = 0
	faultHistoryMu.Unlock()

	RecordFault("TESTDEVICE", 87, 0x0637)
	RecordFault("TESTDEVICE", 14, 0x0637) // different error code — not a dup

	history := GetFaultHistory()
	if len(history) != 2 {
		t.Errorf("GetFaultHistory() len = %d, want 2 (different error codes)", len(history))
	}
}

func TestRecordFault_DifferentDeviceIsNotDeduplicated(t *testing.T) {
	snap := snapshotFaultHistory(t)
	defer snap.restore(t)

	faultHistoryMu.Lock()
	faultRingHead = 0
	faultRingCount = 0
	faultHistoryMu.Unlock()

	RecordFault("DEVICE_A", 87, 0x0637)
	RecordFault("DEVICE_B", 87, 0x0637) // same code, different device — not a dup

	history := GetFaultHistory()
	if len(history) != 2 {
		t.Errorf("GetFaultHistory() len = %d, want 2 (different devices)", len(history))
	}
}

func TestGetFaultHistory_ReturnsChronologicalOrder(t *testing.T) {
	snap := snapshotFaultHistory(t)
	defer snap.restore(t)

	faultHistoryMu.Lock()
	faultRingHead = 0
	faultRingCount = 0
	faultHistoryMu.Unlock()

	RecordFault("A", 1, 0)
	RecordFault("A", 2, 0)
	RecordFault("A", 3, 0)

	history := GetFaultHistory()
	if len(history) != 3 {
		t.Fatalf("GetFaultHistory() len = %d, want 3", len(history))
	}
	for i, want := range []uint16{1, 2, 3} {
		if history[i].ErrorCode != want {
			t.Errorf("history[%d].ErrorCode = %d, want %d (chronological order)", i, history[i].ErrorCode, want)
		}
	}
}

func TestGetFaultHistory_EmptyRingReturnsNil(t *testing.T) {
	snap := snapshotFaultHistory(t)
	defer snap.restore(t)

	faultHistoryMu.Lock()
	faultRingHead = 0
	faultRingCount = 0
	faultHistoryMu.Unlock()

	history := GetFaultHistory()
	if history != nil {
		t.Errorf("GetFaultHistory() on empty ring = %v, want nil", history)
	}
}

func TestRecordFault_RingWrapsAtCapacity(t *testing.T) {
	snap := snapshotFaultHistory(t)
	defer snap.restore(t)

	faultHistoryMu.Lock()
	faultRingHead = 0
	faultRingCount = 0
	faultHistoryMu.Unlock()

	// Record more than capacity (20), each with a distinct code so none
	// are deduplicated.
	for i := 0; i < faultHistoryCapacity+5; i++ {
		RecordFault("A", uint16(i), 0)
	}

	history := GetFaultHistory()
	if len(history) != faultHistoryCapacity {
		t.Fatalf("GetFaultHistory() len = %d, want %d (capacity)", len(history), faultHistoryCapacity)
	}
	// The oldest 5 entries (codes 0-4) should have been evicted; the ring
	// should now hold codes 5..24 in order.
	if history[0].ErrorCode != 5 {
		t.Errorf("history[0].ErrorCode = %d, want 5 (oldest surviving entry after wrap)", history[0].ErrorCode)
	}
	if history[faultHistoryCapacity-1].ErrorCode != uint16(faultHistoryCapacity+4) {
		t.Errorf("history[last].ErrorCode = %d, want %d (newest entry)",
			history[faultHistoryCapacity-1].ErrorCode, faultHistoryCapacity+4)
	}
}

func TestClearFaultHistory_EmptiesRing(t *testing.T) {
	snap := snapshotFaultHistory(t)
	defer snap.restore(t)

	faultHistoryMu.Lock()
	faultRingHead = 0
	faultRingCount = 0
	faultHistoryMu.Unlock()

	RecordFault("A", 1, 0)
	if len(GetFaultHistory()) != 1 {
		t.Fatalf("precondition: expected 1 entry before clear")
	}

	ClearFaultHistory()

	if len(GetFaultHistory()) != 0 {
		t.Errorf("GetFaultHistory() after ClearFaultHistory() = %d entries, want 0", len(GetFaultHistory()))
	}
}

// ─── handleErrCode ─────────────────────────────────────────────────────────
//
// HAL's handleErrCode signature: (device *MasterDevice, errCode int,
// statusword uint16, lastReportedErrCode *int) — grew a device and
// statusword parameter compared to testenv's (errCode, *int) to support
// RecordFault and FormatErrorCode. Calls RecordFault internally on a new
// (non-zero, changed) error code, so these tests also protect the real
// fault history file via snapshot/restore.

func TestHandleErrCode_ZeroWithNoPriorFaultIsNoOp(t *testing.T) {
	// errCode=0, lastReported=0 → nothing to clear, should be silent.
	last := 0
	handleErrCode(stubDevice("A"), 0, 0, &last)
	if last != 0 {
		t.Errorf("lastReportedErrCode = %d after no-fault zero, want 0", last)
	}
}

func TestHandleErrCode_SameCodeIsSuppressed(t *testing.T) {
	// Same errCode as last → suppress (no RecordFault/statusnotifier call,
	// last unchanged). No fault-history side effect since RecordFault is
	// never reached — no snapshot needed for this one.
	last := 42
	handleErrCode(stubDevice("A"), 42, 0, &last)
	if last != 42 {
		t.Errorf("lastReportedErrCode = %d, want 42 (suppressed)", last)
	}
}

func TestHandleErrCode_NewErrorCodeUpdatesLastAndRecordsFault(t *testing.T) {
	snap := snapshotFaultHistory(t)
	defer snap.restore(t)

	ch := make(chan channels.SocketMessage, 16)
	channels.BroadCastUIChannel = ch
	t.Cleanup(func() {
		channels.BroadCastUIChannel = nil
		statusnotifier.SetCurrentErrorCode(0)
		for len(ch) > 0 {
			<-ch
		}
	})
	go func() {
		for range ch {
		}
	}()

	last := 0
	handleErrCode(stubDevice("HANDLEERR_TEST"), 65294, 0x0008, &last) // 65294-65280=errID 14
	if last != 65294 {
		t.Errorf("lastReportedErrCode = %d, want 65294", last)
	}

	history := GetFaultHistory()
	if len(history) == 0 {
		t.Fatalf("expected RecordFault to have added an entry")
	}
	latest := history[len(history)-1]
	if latest.DeviceName != "HANDLEERR_TEST" {
		t.Errorf("recorded fault DeviceName = %q, want HANDLEERR_TEST", latest.DeviceName)
	}
	if latest.ErrorCode != 65294 {
		t.Errorf("recorded fault ErrorCode = %d, want 65294", latest.ErrorCode)
	}
}

func TestHandleErrCode_ZeroAfterFaultClearsAlarm(t *testing.T) {
	ch := make(chan channels.SocketMessage, 16)
	channels.BroadCastUIChannel = ch
	t.Cleanup(func() {
		channels.BroadCastUIChannel = nil
		statusnotifier.SetCurrentErrorCode(0)
		for len(ch) > 0 {
			<-ch
		}
	})
	go func() {
		for range ch {
		}
	}()

	last := 65294
	handleErrCode(stubDevice("A"), 0, 0, &last)
	if last != errCodeCleared {
		t.Errorf("lastReportedErrCode = %d, want %d (errCodeCleared)", last, errCodeCleared)
	}
}
