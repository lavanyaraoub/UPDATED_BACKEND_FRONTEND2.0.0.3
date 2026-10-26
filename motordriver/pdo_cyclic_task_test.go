//go:build unit

package motordriver

// Tests for the CiA-402 state machine logic in cia402NextControlword, and
// the PDO watchdog (PDOTickAge / PDOHealthy).
//
// SCOPE NOTE: this is a curated port of testenv's original
// pdo_cyclic_task_test.go, adapted to HAL's current implementation.
//
//   - cia402NextControlword: the switch statement itself is byte-identical
//     to testenv's version, but the function signature grew a 4th
//     parameter (`driver IMotorDriver`) — the fault-reset controlword
//     (0x0080) is no longer hard-coded, it's read from
//     driver.FaultResetControlword() to support drives other than the A6
//     (e.g. Delta ASDA-A2-E needs 0x008F, not 0x0080 — see
//     IMotorDriver.FaultResetControlword's doc comment). All test cases
//     configure a mock driver with faultResetControlword=0x0080 to
//     reproduce testenv's original expectations for the A6 code path.
//
//   - PDOTickAge / PDOHealthy: unchanged — pdoActive and lastPDOTickNanos
//     are still package-level atomics, same 50ms threshold.
//
//   - GetLastPDOPosition/Statusword/DigitalInputs/ErrorCode: the backing
//     storage moved from package-level atomics to per-device MasterDevice
//     fields (PDOPos, PDOStatus, PDODI, PDOErr), read via
//     legacyFirstDevice() -> masterDevices[0]. Tested here via
//     stubDevice()/setMasterDevicesForTest() (both in mock_driver_test.go),
//     which inject a stub device into the package-level masterDevices
//     slice for the duration of a test.
//
//   - GetLastPDOVelocityActual: now a documented stub that always returns
//     0 ("HAL PDO layout does not currently map 0x606C") — testenv's tests
//     for storing/reading a velocity value no longer apply; replaced with
//     a single test pinning the stub behavior.
//
//   - PDOFaultReset: behavior changed non-trivially (skips devices not in
//     fault state; resetOneFaultedDevice polls for up to 2s waiting for
//     the fault bit to clear). The concurrency test now simulates the
//     fault actually clearing via a background goroutine, since nothing
//     does that automatically against a mock. Two extra tests cover the
//     skip-when-not-faulted behavior and the two early-return guards.

import (
	"sync"
	"testing"
	"time"
)

// stateMachineCase describes one row of the truth table from
// SX-DSV03242 §8.2 (Panasonic A6 manual) and the comment block above
// cia402NextControlword. Each case is the smallest reproducer for one
// branch of the state machine.
type stateMachineCase struct {
	name            string
	statusword      uint16
	faultReset      bool
	wantControlword uint16
	wantOpEnabled   bool
}

// a6FaultResetMock configures a mock driver returning 0x0080 for
// FaultResetControlword(), matching the real A6Minas implementation.
// The other 15 IMotorDriver methods are never called by
// cia402NextControlword, so their zero values are fine.
func a6FaultResetMock() IMotorDriver {
	return &mockDriver{faultResetControlword: 0x0080}
}

func TestCia402NextControlword(t *testing.T) {
	driver := a6FaultResetMock()

	cases := []stateMachineCase{
		// ── Fault handling — bit 3 (0x0008) takes priority over state bits.
		{
			name:            "fault_no_reset_requested",
			statusword:      0x0008,
			faultReset:      false,
			wantControlword: 0x0000, // idle: do not pulse 0x80 every cycle
			wantOpEnabled:   false,
		},
		{
			name:            "fault_with_reset_requested",
			statusword:      0x0008,
			faultReset:      true,
			wantControlword: 0x0080, // CW bit 7 = Fault Reset (A6: driver.FaultResetControlword())
			wantOpEnabled:   false,
		},
		{
			// Fault bit + Operation Enabled bits set simultaneously is a
			// real condition that can arise mid-fault. Fault dominates.
			name:            "fault_dominates_op_enabled_bits",
			statusword:      0x0028, // bit 3 (fault) + bit 5 (Op Enabled bit)
			faultReset:      false,
			wantControlword: 0x0000,
			wantOpEnabled:   false,
		},
		{
			name:            "fault_dominates_even_with_reset",
			statusword:      0x0028,
			faultReset:      true,
			wantControlword: 0x0080,
			wantOpEnabled:   false,
		},

		// ── Normal CiA-402 state machine progression, no fault present.
		{
			name:            "state_not_ready_to_switch_on",
			statusword:      0x0000,
			faultReset:      false,
			wantControlword: 0x0006, // Shutdown — walks toward Ready To Switch On
			wantOpEnabled:   false,
		},
		{
			name:            "state_switch_on_disabled",
			statusword:      0x0040,
			faultReset:      false,
			wantControlword: 0x0006, // Shutdown
			wantOpEnabled:   false,
		},
		{
			name:            "state_ready_to_switch_on",
			statusword:      0x0021,
			faultReset:      false,
			wantControlword: 0x0007, // Switch On
			wantOpEnabled:   false,
		},
		{
			name:            "state_switched_on",
			statusword:      0x0023,
			faultReset:      false,
			wantControlword: 0x000F, // Enable Operation
			wantOpEnabled:   false,
		},
		{
			name:            "state_operation_enabled",
			statusword:      0x0027,
			faultReset:      false,
			wantControlword: 0x000F, // Maintain Operation Enabled
			wantOpEnabled:   true,
		},

		// ── High bits (bit 6, bit 8+) must not influence state matching.
		// The drive sets bits 9-11 (voltage enabled, quick stop, …) during
		// normal operation; the masking step should ignore them.
		{
			name:            "high_bits_ignored_op_enabled",
			statusword:      0xFF27, // 0x0027 with all unrelated bits set
			faultReset:      false,
			wantControlword: 0x000F,
			wantOpEnabled:   true,
		},
		{
			name:            "high_bits_ignored_switched_on",
			statusword:      0xAB23, // 0x0023 plus noise
			faultReset:      false,
			wantControlword: 0x000F,
			wantOpEnabled:   false,
		},

		// ── faultReset must be ignored when no fault is present.
		// Otherwise a stuck faultReset flag would corrupt the controlword
		// during normal operation.
		{
			name:            "fault_reset_ignored_when_op_enabled",
			statusword:      0x0027,
			faultReset:      true,
			wantControlword: 0x000F, // NOT 0x0080
			wantOpEnabled:   true,
		},

		// ── Default branch: unknown state code → restart sequence.
		// The CiA-402 spec leaves several bit combinations undefined.
		// We must not get stuck — restart from Shutdown.
		{
			name:            "unknown_state_falls_back_to_shutdown",
			statusword:      0x0035, // not any defined state
			faultReset:      false,
			wantControlword: 0x0006,
			wantOpEnabled:   false,
		},
	}

	for _, tc := range cases {
		tc := tc // capture loop var for parallel safety
		t.Run(tc.name, func(t *testing.T) {
			// Deliberately pre-set opEnabled to the OPPOSITE of expected.
			// This catches a class of bugs where the function forgets to
			// write the output and the caller sees stale data.
			opEnabled := !tc.wantOpEnabled

			got := cia402NextControlword(tc.statusword, &opEnabled, tc.faultReset, driver)

			if got != tc.wantControlword {
				t.Errorf("controlword mismatch:\n"+
					"  statusword=0x%04X faultReset=%v\n"+
					"  got  = 0x%04X\n"+
					"  want = 0x%04X",
					tc.statusword, tc.faultReset, got, tc.wantControlword)
			}
			if opEnabled != tc.wantOpEnabled {
				t.Errorf("opEnabled mismatch:\n"+
					"  statusword=0x%04X faultReset=%v\n"+
					"  got  = %v\n"+
					"  want = %v",
					tc.statusword, tc.faultReset, opEnabled, tc.wantOpEnabled)
			}
		})
	}
}

// TestCia402NextControlword_AlwaysWritesOpEnabled is a separate test
// because it's an INVARIANT (must hold for every input), not a row in
// the truth table. Documenting it as its own test makes the contract
// explicit: callers may pass an uninitialised bool and trust that the
// function will write it.
func TestCia402NextControlword_AlwaysWritesOpEnabled(t *testing.T) {
	driver := a6FaultResetMock()

	// A handful of representative statuswords — we don't need to be
	// exhaustive here since the table test above covers correctness.
	// We just need to confirm the WRITE happens unconditionally.
	inputs := []uint16{0x0000, 0x0008, 0x0021, 0x0023, 0x0027, 0x0040, 0x1234}

	for _, sw := range inputs {
		// Start with garbage "true" — the function must overwrite it on
		// any input that isn't Operation Enabled, and confirm "true" on
		// the one input that IS Operation Enabled.
		opEnabled := true
		_ = cia402NextControlword(sw, &opEnabled, false, driver)

		wantOpEnabled := (sw&0x006F == 0x0027) && (sw&0x0008 == 0)
		if opEnabled != wantOpEnabled {
			t.Errorf("statusword=0x%04X: opEnabled=%v, want %v "+
				"(function must always write opEnabled, not leave it stale)",
				sw, opEnabled, wantOpEnabled)
		}
	}
}

// ─── EXHAUSTIVE STATE MACHINE TEST ─────────────────────────────────────────
//
// The truth-table test above covers each defined branch with one or two
// inputs. This test covers EVERY possible uint16 statusword (all 65,536)
// and verifies that the function returns one of the five legal CiA-402
// controlwords AND that it never panics.
//
// Why it matters: the drive can send arbitrary garbage on the wire during
// startup, EMI, or firmware bugs. If a statusword we haven't thought of
// causes our function to return 0x42 or panic, the motor will do something
// surprising. This test is the safety net.

func TestCia402NextControlword_AllInputsReturnValidCW(t *testing.T) {
	driver := a6FaultResetMock()

	// The five legal controlwords the function can produce. Anything else
	// is a bug.
	validCWs := map[uint16]bool{
		0x0000: true, // idle (fault, no reset)
		0x0006: true, // Shutdown
		0x0007: true, // Switch On
		0x000F: true, // Enable Operation
		0x0080: true, // Fault Reset
	}

	for sw := uint32(0); sw <= 0xFFFF; sw++ {
		statusword := uint16(sw)

		// Try both faultReset values for each statusword — the function
		// should always return a legal CW regardless.
		for _, faultReset := range []bool{false, true} {
			var opEnabled bool
			cw := cia402NextControlword(statusword, &opEnabled, faultReset, driver)

			if !validCWs[cw] {
				t.Fatalf("illegal controlword returned:\n"+
					"  statusword=0x%04X faultReset=%v\n"+
					"  got CW=0x%04X (not in {0x00, 0x06, 0x07, 0x0F, 0x80})",
					statusword, faultReset, cw)
			}
		}
	}
}

// ─── PDO WATCHDOG TESTS ────────────────────────────────────────────────────
//
// These tests exercise the watchdog accessor functions (PDOTickAge and
// PDOHealthy) by directly manipulating the package atomics. They do NOT
// start the watchdog goroutine — they only test the predicate logic that
// the goroutine (and any external caller) relies on. Both pdoActive and
// lastPDOTickNanos are still package-level atomics in the current code
// (pdo_cyclic_task.go), unchanged from testenv's assumptions.
//
// State isolation: each test resets pdoActive and lastPDOTickNanos in
// a t.Cleanup so a failed test can't leak state into the next.

// resetPDOWatchdogState clears the package globals used by the watchdog.
// Call it at the start of any test that manipulates them, and register
// it as t.Cleanup so the state is restored even if the test fails.
func resetPDOWatchdogState() {
	pdoActive.Store(false)
	lastPDOTickNanos.Store(0)
}

func TestPDOTickAge_ReturnsZeroBeforeFirstTick(t *testing.T) {
	resetPDOWatchdogState()
	t.Cleanup(resetPDOWatchdogState)

	age := PDOTickAge()
	if age != 0 {
		t.Errorf("expected age=0 before any tick recorded, got %v", age)
	}
}

func TestPDOTickAge_ReturnsRecentAgeAfterTick(t *testing.T) {
	resetPDOWatchdogState()
	t.Cleanup(resetPDOWatchdogState)

	// Simulate a tick happening exactly now.
	lastPDOTickNanos.Store(time.Now().UnixNano())

	age := PDOTickAge()
	// We can't assert age == 0 because some nanoseconds elapse between
	// Store and Load. We CAN assert it's well under any meaningful
	// threshold. 5ms is generous even on a loaded Pi.
	if age < 0 || age > 5*time.Millisecond {
		t.Errorf("expected fresh age (0 < age < 5ms), got %v", age)
	}
}

func TestPDOTickAge_DetectsStaleness(t *testing.T) {
	resetPDOWatchdogState()
	t.Cleanup(resetPDOWatchdogState)

	// Simulate a tick that happened 100ms ago.
	lastPDOTickNanos.Store(time.Now().Add(-100 * time.Millisecond).UnixNano())

	age := PDOTickAge()
	// Should be approximately 100ms — allow ±10ms slop for scheduler jitter.
	if age < 90*time.Millisecond || age > 110*time.Millisecond {
		t.Errorf("expected age~100ms, got %v", age)
	}
}

func TestPDOHealthy_QuietWhenPDONotActive(t *testing.T) {
	resetPDOWatchdogState()
	t.Cleanup(resetPDOWatchdogState)

	// Before PDO starts: should report (true, 0). This is the documented
	// behavior — "not active" is not "unhealthy", it's "not yet relevant".
	healthy, age := PDOHealthy()
	if !healthy {
		t.Errorf("expected healthy=true when PDO not active, got false")
	}
	if age != 0 {
		t.Errorf("expected age=0 when PDO not active, got %v", age)
	}
}

func TestPDOHealthy_HealthyWhenTicksFresh(t *testing.T) {
	resetPDOWatchdogState()
	t.Cleanup(resetPDOWatchdogState)

	pdoActive.Store(true)
	// Tick just happened.
	lastPDOTickNanos.Store(time.Now().UnixNano())

	healthy, age := PDOHealthy()
	if !healthy {
		t.Errorf("expected healthy=true with fresh tick, got false (age=%v)", age)
	}
}

func TestPDOHealthy_UnhealthyWhenTicksStale(t *testing.T) {
	resetPDOWatchdogState()
	t.Cleanup(resetPDOWatchdogState)

	pdoActive.Store(true)
	// Tick happened 100ms ago — well beyond the 50ms warn threshold.
	lastPDOTickNanos.Store(time.Now().Add(-100 * time.Millisecond).UnixNano())

	healthy, age := PDOHealthy()
	if healthy {
		t.Errorf("expected healthy=false at 100ms staleness, got true (age=%v)", age)
	}
}

func TestPDOHealthy_BoundaryAt50ms(t *testing.T) {
	// The threshold is exactly 50ms. Anything < 50ms is healthy, anything
	// >= 50ms is unhealthy. This test verifies the boundary by sampling
	// just below and just above it.
	resetPDOWatchdogState()
	t.Cleanup(resetPDOWatchdogState)

	pdoActive.Store(true)

	// 40ms — should be healthy.
	lastPDOTickNanos.Store(time.Now().Add(-40 * time.Millisecond).UnixNano())
	healthy, _ := PDOHealthy()
	if !healthy {
		t.Errorf("expected healthy=true at 40ms age (below 50ms threshold)")
	}

	// 60ms — should be unhealthy.
	lastPDOTickNanos.Store(time.Now().Add(-60 * time.Millisecond).UnixNano())
	healthy, _ = PDOHealthy()
	if healthy {
		t.Errorf("expected healthy=false at 60ms age (above 50ms threshold)")
	}
}

// ─── CONCURRENCY TESTS (watchdog-only slice) ───────────────────────────────
//
// These exercise pdoActive / lastPDOTickNanos under concurrent load. Both
// are still plain package-level atomics, so these are unchanged from
// testenv. Run with -race to check for data races:
//
//   go test -tags=unit -race -count=1 ./motordriver/...
//
// (remember: -race is expected to SKIP on the Pi's 39-bit-VMA kernel —
// see the top-level Makefile's test-race target).

func TestPDOHealthy_ConcurrentAccess(t *testing.T) {
	resetPDOWatchdogState()
	t.Cleanup(resetPDOWatchdogState)
	pdoActive.Store(true)
	lastPDOTickNanos.Store(time.Now().UnixNano())

	const iters = 1_000
	var wg sync.WaitGroup

	// Half the goroutines update the tick timestamp
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				lastPDOTickNanos.Store(time.Now().UnixNano())
			}
		}()
	}

	// Half call PDOHealthy
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				healthy, age := PDOHealthy()
				if age < 0 {
					t.Errorf("PDOHealthy() returned negative age: %v", age)
				}
				_ = healthy
			}
		}()
	}

	wg.Wait()
}

// TestPDOActive_ToggleUnderLoad confirms toggling pdoActive while goroutines
// read IsPDOActive() produces no races.
func TestPDOActive_ToggleUnderLoad(t *testing.T) {
	pdoActive.Store(false)
	t.Cleanup(func() { pdoActive.Store(false) })

	const iters = 1_000
	var wg sync.WaitGroup

	// Writer toggles pdoActive
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			pdoActive.Store(i%2 == 0)
		}
	}()

	// Readers call IsPDOActive
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				_ = IsPDOActive()
			}
		}()
	}

	wg.Wait()
}

// ─── Misc ───────────────────────────────────────────────────────────────

func TestGetCurrentAlarm_DelegatesToStatusNotifier(t *testing.T) {
	got := GetCurrentAlarm()
	if got == "" {
		t.Errorf("GetCurrentAlarm() = empty, want at least \"No Alarms\"")
	}
}

// TestGetLastPDOVelocityActual_IsAlwaysZero pins the documented stub
// behavior: HAL's PDO layout does not currently map 0x606C (velocity
// actual value), so this always returns 0 regardless of any device state.
// This replaces testenv's original store/read tests for this function,
// which assumed it was backed by real storage.
func TestGetLastPDOVelocityActual_IsAlwaysZero(t *testing.T) {
	if got := GetLastPDOVelocityActual(); got != 0 {
		t.Errorf("GetLastPDOVelocityActual() = %d, want 0 (documented stub — 0x606C not mapped)", got)
	}
}

// ─── PDO getter tests (per-device storage, via masterDevices injection) ───
//
// These were originally deferred — the getters read masterDevices[0] via
// legacyFirstDevice() rather than removed package-level atomics
// (lastPDOPos, lastPDOStatus, lastPDODI, lastPDOErr). stubDevice() and
// setMasterDevicesForTest() (both in mock_driver_test.go) are the
// injection mechanism.

func TestGetLastPDOPosition_StoresAndReads(t *testing.T) {
	dev := stubDevice("A")
	restore := setMasterDevicesForTest(dev)
	defer restore()

	dev.PDOPos.Store(131072) // 1° at 131072 pulses/deg
	if got := GetLastPDOPosition(); got != 131072 {
		t.Errorf("GetLastPDOPosition() = %d, want 131072", got)
	}
}

func TestGetLastPDOPosition_NegativeValue(t *testing.T) {
	dev := stubDevice("A")
	restore := setMasterDevicesForTest(dev)
	defer restore()

	dev.PDOPos.Store(-131072)
	if got := GetLastPDOPosition(); got != -131072 {
		t.Errorf("GetLastPDOPosition() = %d, want -131072", got)
	}
}

func TestGetLastPDOPosition_NoDevices_ReturnsZero(t *testing.T) {
	restore := setMasterDevicesForTest() // empty
	defer restore()

	if got := GetLastPDOPosition(); got != 0 {
		t.Errorf("GetLastPDOPosition() with no devices = %d, want 0", got)
	}
}

func TestGetLastPDOStatusword_MasksHighBits(t *testing.T) {
	dev := stubDevice("A")
	restore := setMasterDevicesForTest(dev)
	defer restore()

	// Store a value with garbage in high bits. The accessor must return
	// only the low 16 bits.
	dev.PDOStatus.Store(0xDEAD0637) // 0x0637 is a valid Op-Enabled statusword
	got := GetLastPDOStatusword()
	if got != 0x0637 {
		t.Errorf("expected 0x0637 (low 16 bits), got 0x%04X", got)
	}
}

func TestGetLastPDODigitalInputs_StoresAndReads(t *testing.T) {
	dev := stubDevice("A")
	restore := setMasterDevicesForTest(dev)
	defer restore()

	dev.PDODI.Store(0xA5) // ECS=1, FIN=0, POT=1 pattern
	if got := GetLastPDODigitalInputs(); got != 0xA5 {
		t.Errorf("GetLastPDODigitalInputs() = 0x%X, want 0xA5", got)
	}
}

func TestGetLastPDODigitalInputs_AllBitsSet(t *testing.T) {
	dev := stubDevice("A")
	restore := setMasterDevicesForTest(dev)
	defer restore()

	dev.PDODI.Store(0xFFFFFFFF)
	if got := GetLastPDODigitalInputs(); got != 0xFFFFFFFF {
		t.Errorf("GetLastPDODigitalInputs() = 0x%X, want 0xFFFFFFFF", got)
	}
}

func TestGetLastPDOErrorCode_MasksHighBits(t *testing.T) {
	dev := stubDevice("A")
	restore := setMasterDevicesForTest(dev)
	defer restore()

	// Drive error 0x1A82 (vendor-specific overheat) plus high-bit garbage.
	dev.PDOErr.Store(0x12341A82)
	got := GetLastPDOErrorCode()
	if got != 0x1A82 {
		t.Errorf("expected 0x1A82 (low 16 bits), got 0x%04X", got)
	}
}

// TestPDOStatusword_ConcurrentReadWrite confirms no data race when the cyclic
// task writes the statusword while multiple readers poll it simultaneously.
// Rewritten against masterDevices[0].PDOStatus (the current storage location)
// instead of the removed lastPDOStatus package var.
func TestPDOStatusword_ConcurrentReadWrite(t *testing.T) {
	dev := stubDevice("A")
	restore := setMasterDevicesForTest(dev)
	defer restore()

	const iters = 1_000
	var wg sync.WaitGroup

	// Writer: cycles through realistic statuswords
	wg.Add(1)
	go func() {
		defer wg.Done()
		vals := []uint32{0x0027, 0x0023, 0x0021, 0x0040, 0x0008}
		for i := 0; i < iters; i++ {
			dev.PDOStatus.Store(vals[i%len(vals)])
		}
	}()

	// Readers: each reads iters times and checks the mask invariant
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				sw := GetLastPDOStatusword()
				if uint32(sw) > 0xFFFF {
					t.Errorf("GetLastPDOStatusword() has high bits set: 0x%08X", sw)
				}
			}
		}()
	}

	wg.Wait()
}

// ─── PDOFaultReset concurrency ─────────────────────────────────────────────
//
// Re-derived against the CURRENT behavior (verified by reading
// resetOneFaultedDevice in full):
//
//   - PDOFaultReset skips any device whose statusword doesn't have the
//     fault bit (0x0008) set — so the stub MUST start with that bit set.
//   - resetOneFaultedDevice sleeps 50ms, then polls PDOStatus for up to 2s
//     waiting for the fault bit to clear. Against a mock, nothing clears
//     it unless the test simulates that — so this test spawns one extra
//     goroutine that clears the fault bit shortly after the reset begins,
//     standing in for "the drive acknowledged the reset."
//   - Only the goroutine that wins the per-device FaultResetMu.TryLock()
//     actually calls resetOneFaultedDevice; the other 9 fail TryLock
//     immediately (the lock is held for the ~600ms+ duration of the
//     winning call) and return false without blocking.
//   - SetTargetPositionPDO/EnableJogPDO (called internally) are both
//     nil-safe against a bare stub: SetTargetPositionPDO checks
//     PdoPosReady (false on a zero-value stub) and returns early;
//     EnableJogPDO only touches atomic.Bool fields. Neither touches the
//     cgo-backed PDO domain memory that a stub doesn't have.
//
// This test takes ~600ms-1s wall time (50ms initial sleep + poll interval
// until the simulated clear + 500ms digital-output settle) — much faster
// than the worst case (~2.5s) because the fault is cleared quickly instead
// of timing out.
func TestPDOFaultReset_ConcurrentCalls_OnlyOneSucceeds(t *testing.T) {
	pdoActive.Store(true)
	t.Cleanup(func() { pdoActive.Store(false) })

	dev := stubDevice("A")
	dev.PDOStatus.Store(0x0008) // fault bit set — required for PDOFaultReset to attempt anything

	// Simulate the drive acknowledging the reset ~150ms in, well within
	// resetOneFaultedDevice's 2s poll window but well after every
	// concurrent caller has already attempted (and mostly failed) TryLock.
	go func() {
		time.Sleep(150 * time.Millisecond)
		dev.PDOStatus.Store(0x0027) // Operation Enabled, fault bit clear
	}()

	const concurrent = 10
	results := make([]bool, concurrent)
	var wg sync.WaitGroup

	for i := 0; i < concurrent; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = PDOFaultReset([]*MasterDevice{dev})
		}()
	}
	wg.Wait()

	trueCount := 0
	for _, r := range results {
		if r {
			trueCount++
		}
	}
	if trueCount != 1 {
		t.Errorf("expected exactly 1 of %d concurrent PDOFaultReset calls to succeed "+
			"(the one holding FaultResetMu when the fault cleared), got %d", concurrent, trueCount)
	}
	t.Logf("PDOFaultReset concurrent: %d/%d returned true", trueCount, concurrent)
}

// TestPDOFaultReset_SkipsDevicesNotInFault verifies the documented Phase 4
// fix: devices without the fault bit set are left alone entirely (never
// locked, never reset) rather than being touched unconditionally.
func TestPDOFaultReset_SkipsDevicesNotInFault(t *testing.T) {
	pdoActive.Store(true)
	t.Cleanup(func() { pdoActive.Store(false) })

	dev := stubDevice("A")
	dev.PDOStatus.Store(0x0027) // Operation Enabled — no fault

	got := PDOFaultReset([]*MasterDevice{dev})
	if !got {
		t.Errorf("PDOFaultReset with no faulted devices = false, want true (nothing to do = trivially succeeds)")
	}
	if dev.FaultResetMu.TryLock() {
		dev.FaultResetMu.Unlock()
	} else {
		t.Errorf("FaultResetMu was left locked — device without a fault should never be locked at all")
	}
}

// TestPDOFaultReset_NotPDOActive_ReturnsFalse verifies the early-return
// guard: PDOFaultReset must refuse to run at all when PDO isn't active,
// regardless of device fault state.
func TestPDOFaultReset_NotPDOActive_ReturnsFalse(t *testing.T) {
	pdoActive.Store(false)

	dev := stubDevice("A")
	dev.PDOStatus.Store(0x0008)

	got := PDOFaultReset([]*MasterDevice{dev})
	if got {
		t.Errorf("PDOFaultReset with pdoActive=false = true, want false")
	}
}

// TestPDOFaultReset_EmptyDeviceList_ReturnsFalse verifies the other
// early-return guard.
func TestPDOFaultReset_EmptyDeviceList_ReturnsFalse(t *testing.T) {
	pdoActive.Store(true)
	t.Cleanup(func() { pdoActive.Store(false) })

	got := PDOFaultReset(nil)
	if got {
		t.Errorf("PDOFaultReset with empty device list = true, want false")
	}
}

// TestPDOFaultReset_PreservesBrakeBitAcrossReset verifies the documented
// Bug 6 fix: resetOneFaultedDevice must NOT unconditionally zero
// desiredDigOutVal/Mask. Doing so would silently release a brake driven by
// bit 1 (brakeBitMask=0x00000002) mid-reset — catastrophic on a vertical or
// inclined axis, where the load would drop under gravity the instant a
// drive fault was reset. The fix clears only non-brake output bits during
// the settle window and restores the full mask afterward, while the brake
// bit itself is carried through unchanged the entire time.
func TestPDOFaultReset_PreservesBrakeBitAcrossReset(t *testing.T) {
	pdoActive.Store(true)
	t.Cleanup(func() { pdoActive.Store(false) })

	dev := stubDevice("A")
	dev.PDOStatus.Store(0x0008)             // fault bit set
	dev.desiredDigOutVal.Store(0x00000002)  // brake bit ON before the fault reset
	dev.desiredDigOutMask.Store(0xFFFFFFFF) // normal full-mask operation

	go func() {
		time.Sleep(100 * time.Millisecond)
		dev.PDOStatus.Store(0x0027) // fault clears, Operation Enabled reached
	}()

	got := PDOFaultReset([]*MasterDevice{dev})
	if !got {
		t.Fatal("PDOFaultReset: expected true (fault cleared successfully)")
	}

	const brakeBitMask = uint32(0x00000002)
	if mask := dev.desiredDigOutMask.Load(); mask != 0xFFFFFFFF {
		t.Errorf("desiredDigOutMask after reset = 0x%08X, want 0xFFFFFFFF (full mask restored)", mask)
	}
	if val := dev.desiredDigOutVal.Load(); val&brakeBitMask == 0 {
		t.Errorf("desiredDigOutVal after reset = 0x%08X — brake bit was cleared, want it preserved", val)
	}
	if err := dev.PDOErr.Load(); err != 0 {
		t.Errorf("PDOErr after successful reset = %d, want 0", err)
	}
}

// TestPDOFaultReset_BrakeBitStaysOffIfItWasOff verifies the fix's other
// direction: if the brake bit was OFF before the fault (brake engaged, motor
// not expected to be free), the reset must not accidentally turn it ON.
func TestPDOFaultReset_BrakeBitStaysOffIfItWasOff(t *testing.T) {
	pdoActive.Store(true)
	t.Cleanup(func() { pdoActive.Store(false) })

	dev := stubDevice("A")
	dev.PDOStatus.Store(0x0008)
	dev.desiredDigOutVal.Store(0x00000000) // brake bit OFF before the fault
	dev.desiredDigOutMask.Store(0xFFFFFFFF)

	go func() {
		time.Sleep(100 * time.Millisecond)
		dev.PDOStatus.Store(0x0027)
	}()

	got := PDOFaultReset([]*MasterDevice{dev})
	if !got {
		t.Fatal("PDOFaultReset: expected true (fault cleared successfully)")
	}

	const brakeBitMask = uint32(0x00000002)
	if val := dev.desiredDigOutVal.Load(); val&brakeBitMask != 0 {
		t.Errorf("desiredDigOutVal after reset = 0x%08X — brake bit was turned ON, want it to stay OFF", val)
	}
}

// ─── StopPDOCyclic / StartPDOCyclic — guard clauses ───────────────────────

func TestStopPDOCyclic_PDONotActive_ReturnsImmediately(t *testing.T) {
	pdoActive.Store(false)
	done := make(chan struct{})
	go func() {
		defer close(done)
		StopPDOCyclic()
	}()
	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("StopPDOCyclic with pdoActive=false did not return promptly")
	}
}

func TestStartPDOCyclic_EmptyDevices_ReturnsErrorWithoutTouchingHardware(t *testing.T) {
	err := StartPDOCyclic(nil)
	if err == nil {
		t.Error("StartPDOCyclic(nil): expected an error, got nil")
	}
}

// ─── PDOSetDigitalOutputForDevice / PDOSetDigitalOutputOnDevice ───────────

func TestPDOSetDigitalOutputForDevice_EmptyDevices_NoOp(t *testing.T) {
	restore := setMasterDevicesForTest()
	defer restore()

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("PDOSetDigitalOutputForDevice panicked on empty devices: %v", r)
		}
	}()
	PDOSetDigitalOutputForDevice("A", 0xFF, 0x01)
}

func TestPDOSetDigitalOutputForDevice_MatchingName_SetsOnCorrectDevice(t *testing.T) {
	a := stubDevice("A")
	b := stubDevice("B")
	a.PdoDigOutReady = true
	b.PdoDigOutReady = true
	restore := setMasterDevicesForTest(a, b)
	defer restore()

	PDOSetDigitalOutputForDevice("B", 0x000000FF, 0x00000001)

	if got := b.desiredDigOutMask.Load(); got != 0x000000FF {
		t.Errorf("device B desiredDigOutMask = 0x%08X, want 0x000000FF", got)
	}
	if got := b.desiredDigOutVal.Load(); got != 0x00000001 {
		t.Errorf("device B desiredDigOutVal = 0x%08X, want 0x00000001", got)
	}
	if got := a.desiredDigOutMask.Load(); got != 0 {
		t.Errorf("device A desiredDigOutMask = 0x%08X, want 0 (untouched)", got)
	}
}

func TestPDOSetDigitalOutputForDevice_NoNameMatch_FallsBackToFirstDevice(t *testing.T) {
	a := stubDevice("A")
	b := stubDevice("B")
	a.PdoDigOutReady = true
	b.PdoDigOutReady = true
	restore := setMasterDevicesForTest(a, b)
	defer restore()

	PDOSetDigitalOutputForDevice("nonexistent-drive", 0x0000000F, 0x00000002)

	if got := a.desiredDigOutMask.Load(); got != 0x0000000F {
		t.Errorf("fallback device A desiredDigOutMask = 0x%08X, want 0x0000000F", got)
	}
}

func TestPDOSetDigitalOutputOnDevice_NilDevice_NoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("PDOSetDigitalOutputOnDevice panicked on nil device: %v", r)
		}
	}()
	PDOSetDigitalOutputOnDevice(nil, 0xFF, 0x01)
}

func TestPDOSetDigitalOutputOnDevice_PdoDigOutNotReady_IsNoOp(t *testing.T) {
	dev := stubDevice("A")
	dev.PdoDigOutReady = false

	PDOSetDigitalOutputOnDevice(dev, 0x000000FF, 0x00000001)

	if got := dev.desiredDigOutMask.Load(); got != 0 {
		t.Errorf("desiredDigOutMask = 0x%08X, want 0 (PdoDigOutReady=false should no-op)", got)
	}
}

func TestPDOSetDigitalOutputOnDevice_Ready_StoresMaskAndValue(t *testing.T) {
	dev := stubDevice("A")
	dev.PdoDigOutReady = true

	PDOSetDigitalOutputOnDevice(dev, 0x0000ABCD, 0x00001234)

	if got := dev.desiredDigOutMask.Load(); got != 0x0000ABCD {
		t.Errorf("desiredDigOutMask = 0x%08X, want 0x0000ABCD", got)
	}
	if got := dev.desiredDigOutVal.Load(); got != 0x00001234 {
		t.Errorf("desiredDigOutVal = 0x%08X, want 0x00001234", got)
	}
}
