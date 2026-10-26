//go:build unit

package motordriver

// Tests for the pure-Go branches in drive_rotation.go, plus adjacent
// functions in driver_status_keeper.go, poll_drive_position.go, and
// zero_reference.go that the original test suite bundled alongside them.
//
// reverseDir / nonReverseDir — return nil immediately when PDO is active
// StopJog                   — returns error immediately when PDO is NOT active
//
// PORTING NOTE: drive_rotation.go itself diverged extensively between HAL
// and testenv (ManualJog, StopJog's internals, hasTargetReached, doRotate —
// see MOTORDRIVER_FOLLOWUP.md-style analysis). None of that matters for
// THIS file: every test here only touches early-return guards or delegates
// to functions verified unchanged elsewhere. Specifically:
//
//   - reverseDir/nonReverseDir: unchanged between HAL/testenv.
//   - StopJog: only the PDO-not-active early-return path is tested here —
//     that guard is present in both versions even though StopJog's PDO-active
//     internals differ substantially.
//   - checkPotNotLimit (poll_drive_position.go): HAL is MISSING the explicit
//     `if !driverStatus.isMotorRunning { return false }` guard that testenv
//     has — worth knowing about, but doesn't affect these three tests,
//     which all reach `false` via the direction-matching branches instead
//     (verified by tracing HAL's actual logic by hand). This is noted here
//     rather than silently glossed over; if HAL's behavior is ever changed
//     to add that guard, these tests would still pass unchanged.
//   - driverCurrentStatus.reset(): testenv clears potNotExceeded/potExceeded/
//     notExceeded, HAL's version did not — this was a real gap (a POT/NOT
//     alarm would never clear after Reset) and has been fixed directly in
//     driver_status_keeper.go as part of this port, not just in the test.
//   - getPos / shortestPathToZero (zero_reference.go): byte-identical logic
//     between HAL and testenv (comment-only differences).

import (
	"math"
	"strings"
	"testing"
	"time"

	"EtherCAT/settings"

	cmap "github.com/orcaman/concurrent-map"
)

// ─── reverseDir ───────────────────────────────────────────────────────────

func TestReverseDir_PDOActiveReturnsNil(t *testing.T) {
	activatePDO(t)
	err := reverseDir(stubDevice("A"))
	if err != nil {
		t.Errorf("reverseDir PDO active: got error %v, want nil", err)
	}
}

// ─── nonReverseDir ────────────────────────────────────────────────────────

func TestNonReverseDir_PDOActiveReturnsNil(t *testing.T) {
	activatePDO(t)
	err := nonReverseDir(stubDevice("A"))
	if err != nil {
		t.Errorf("nonReverseDir PDO active: got error %v, want nil", err)
	}
}

// ─── StopJog ──────────────────────────────────────────────────────────────

func TestStopJog_PDONotActiveReturnsError(t *testing.T) {
	// PDO is NOT active (default state)
	d := stubDevice("A")
	d.PdoJogReady = false
	err := StopJog(d)
	if err == nil {
		t.Errorf("StopJog PDO not active: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "PDO not active") {
		t.Errorf("StopJog error = %q, want to contain 'PDO not active'", err.Error())
	}
}

// ─── driverCurrentStatus.reset() ─────────────────────────────────────────
//
// reset() must zero all transient fields, including the safety-limit
// flags (see fix in driver_status_keeper.go), and put mode back to "ABS".

func TestDriverCurrentStatus_Reset_ZerosTransientFields(t *testing.T) {
	s := driverCurrentStatus{
		currentPosition:     123.4,
		alarm:               "some alarm",
		mode:                "REL",
		shortestPathEnabled: true,
		destinationPosition: 99,
		backlash:            0.5,
		workOffset:          10,
		isMotorRunning:      true,
		potNotExceeded:      true,
		potExceeded:         true,
		notExceeded:         true,
	}
	s.reset()

	if s.currentPosition != 0 {
		t.Errorf("currentPosition = %v, want 0", s.currentPosition)
	}
	if s.alarm != "" {
		t.Errorf("alarm = %q, want empty", s.alarm)
	}
	if s.mode != "ABS" {
		t.Errorf("mode = %q, want ABS", s.mode)
	}
	if s.shortestPathEnabled {
		t.Errorf("shortestPathEnabled = true, want false")
	}
	if s.destinationPosition != -1 {
		t.Errorf("destinationPosition = %v, want -1", s.destinationPosition)
	}
	if s.backlash != 0 {
		t.Errorf("backlash = %v, want 0", s.backlash)
	}
	if s.workOffset != 0 {
		t.Errorf("workOffset = %v, want 0", s.workOffset)
	}
	if s.isMotorRunning {
		t.Errorf("isMotorRunning = true, want false")
	}
	if s.potNotExceeded {
		t.Errorf("potNotExceeded = true, want false")
	}
	if s.potExceeded {
		t.Errorf("potExceeded = true, want false")
	}
	if s.notExceeded {
		t.Errorf("notExceeded = true, want false")
	}
}

// ─── checkPotNotLimit (poll_drive_position.go) ────────────────────────────
//
// checkPotNotLimit is the runtime position guard that fires during active
// polling of the drive position — separate from the command-level guard in
// moveRotary.go. It operates on live position, not on a destination target.
//
// Branches testable without hardware (no channels, no FastPowerOff SDO call
// because FastPowerOff returns nil when PDO is active):

func makePotNotDevice(threshold float64) *MasterDevice {
	d := stubDevice("A")
	d.Device.PotNotThreshold = threshold
	return d
}

// TestCheckPotNotLimit_MotorNotRunningReturnsFalse: HAL has no explicit
// isMotorRunning guard, but with isMotorRunning=false the zero-value
// driverCurrentStatus also has direction=0, which matches neither the POT
// branch (direction==1) nor the NOT branch (direction==-1) — so the
// function still returns false, just via a different path than testenv's
// explicit early return. See the file-level comment above.
func TestCheckPotNotLimit_MotorNotRunningReturnsFalse(t *testing.T) {
	status := driverCurrentStatus{isMotorRunning: false}
	got := checkPotNotLimit(90, makePotNotDevice(1.5), settings.DriverSettings{POT: 100, NOT: -100}, status)
	if got {
		t.Errorf("checkPotNotLimit motor not running: got true, want false")
	}
}

// TestCheckPotNotLimit_DisabledLimitsReturnsFalse verifies that limits are
// skipped when NOT >= 0 and POT <= 0 (infinite-rotation / limits-off config).
func TestCheckPotNotLimit_DisabledLimitsReturnsFalse(t *testing.T) {
	status := driverCurrentStatus{isMotorRunning: true, direction: 1}
	got := checkPotNotLimit(350, makePotNotDevice(1.5), settings.DriverSettings{POT: 0, NOT: 0}, status)
	if got {
		t.Errorf("checkPotNotLimit disabled limits: got true, want false")
	}
}

// TestCheckPotNotLimit_WithinLimitsReturnsFalse verifies that a position safely
// inside both limits returns false without triggering any alarm path.
// PDO is activated so FastPowerOff/StopJog return nil immediately without SDO.
func TestCheckPotNotLimit_WithinLimitsReturnsFalse(t *testing.T) {
	activatePDO(t)
	status := driverCurrentStatus{isMotorRunning: true, direction: 1}
	got := checkPotNotLimit(50, makePotNotDevice(1.5), settings.DriverSettings{POT: 100, NOT: -100}, status)
	if got {
		t.Errorf("checkPotNotLimit within limits: got true, want false (position=50, POT=100)")
	}
}

// ─── getPos ───────────────────────────────────────────────────────────────
//
// getPos returns the clockwise (+) or counter-clockwise (-) angular distance
// from currentPos to targetPos.

func TestGetPos_ClockwiseFromZeroToNinety(t *testing.T) {
	// 0° → 90° clockwise = travel +90° (short arc clockwise)
	got := getPos(0, 90, true)
	if math.Abs(got-90) > 0.001 {
		t.Errorf("getPos(0, 90, CW) = %v, want 90", got)
	}
}

func TestGetPos_CounterClockwiseFromZeroToNinety(t *testing.T) {
	// 0° → 90° counter-clockwise = travel +90° (CCW result is positive here due to mod)
	got := getPos(0, 90, false)
	if math.Abs(got-90) > 0.001 {
		t.Errorf("getPos(0, 90, CCW) = %v, want 90", got)
	}
}

func TestGetPos_ClockwiseToZero(t *testing.T) {
	// 90° → 0° clockwise = travel +270° (long arc clockwise)
	got := getPos(90, 0, true)
	if math.Abs(got-270) > 0.001 {
		t.Errorf("getPos(90, 0, CW) = %v, want 270", got)
	}
}

func TestGetPos_CounterClockwiseToZero(t *testing.T) {
	// 90° → 0° counter-clockwise = travel -90° (short arc counter-clockwise)
	got := getPos(90, 0, false)
	if math.Abs(got-(-90)) > 0.001 {
		t.Errorf("getPos(90, 0, CCW) = %v, want -90", got)
	}
}

func TestGetPos_SamePositionReturnsZero(t *testing.T) {
	cw := getPos(45, 45, true)
	ccw := getPos(45, 45, false)
	if math.Abs(cw) > 0.001 {
		t.Errorf("getPos(45, 45, CW) = %v, want 0", cw)
	}
	if math.Abs(ccw) > 0.001 {
		t.Errorf("getPos(45, 45, CCW) = %v, want 0", ccw)
	}
}

func TestGetPos_ClockwiseAlwaysNonNegative(t *testing.T) {
	for deg := 0.0; deg < 360; deg += 10 {
		got := getPos(deg, 0, true)
		if got < 0 {
			t.Errorf("getPos(%.0f, 0, CW) = %v, want >= 0", deg, got)
		}
	}
}

func TestGetPos_CounterClockwiseAlwaysNonPositive(t *testing.T) {
	for deg := 0.0; deg < 360; deg += 10 {
		got := getPos(deg, 0, false)
		if got > 0 {
			t.Errorf("getPos(%.0f, 0, CCW) = %v, want <= 0", deg, got)
		}
	}
}

// ─── shortestPathToZero ───────────────────────────────────────────────────
//
// shortestPathToZero returns the signed shortest arc from currentPosition to 0°.

func TestShortestPathToZero_From90TakesCounterClockwise(t *testing.T) {
	// 90° to 0°: CCW = -90°, CW = +270° → shortest is CCW (-90°)
	got := shortestPathToZero(90, 1)
	if math.Abs(got-(-90)) > 0.001 {
		t.Errorf("shortestPathToZero(90, 1) = %v, want -90 (CCW is shorter)", got)
	}
}

func TestShortestPathToZero_From270TakesClockwise(t *testing.T) {
	// 270° to 0°: CW = +90°, CCW = -270° → shortest is CW (+90°)
	got := shortestPathToZero(270, 1)
	if math.Abs(got-90) > 0.001 {
		t.Errorf("shortestPathToZero(270, 1) = %v, want +90 (CW is shorter)", got)
	}
}

func TestShortestPathToZero_From180TieUsesHomeDirection(t *testing.T) {
	// 180° to 0°: both arcs are 180° — homeDirection breaks the tie
	// homeDir=1 → CW → positive result (+180)
	// homeDir=0 → CCW → negative result (-180)
	cwResult := shortestPathToZero(180, 1)
	ccwResult := shortestPathToZero(180, 0)

	if cwResult < 0 {
		t.Errorf("shortestPathToZero(180, homeDir=1) = %v, want positive (clockwise)", cwResult)
	}
	if ccwResult > 0 {
		t.Errorf("shortestPathToZero(180, homeDir=0) = %v, want negative (counter-clockwise)", ccwResult)
	}
}

func TestShortestPathToZero_FromZeroIsZero(t *testing.T) {
	got := shortestPathToZero(0, 1)
	if math.Abs(got) > 0.001 {
		t.Errorf("shortestPathToZero(0, 1) = %v, want 0", got)
	}
}

func TestShortestPathToZero_AbsoluteValueNeverExceeds180(t *testing.T) {
	for deg := 0.0; deg < 360; deg += 5 {
		got := shortestPathToZero(deg, 1)
		if math.Abs(got) > 180.001 {
			t.Errorf("shortestPathToZero(%.0f°) = %v, abs > 180°", deg, got)
		}
	}
}

// ─── ManualJog — guard clause and happy path ─────────────────────────────────

func TestManualJog_PotNotExceeded_ReturnsErrorImmediately(t *testing.T) {
	driverStatusMap = cmap.New()
	t.Cleanup(func() { driverStatusMap = cmap.New() })

	d := stubDevice("A")
	d.Device.Name = "A"
	setCurrentDriverStatus("A", driverCurrentStatus{potNotExceeded: true})

	if err := ManualJog(d, 1); err == nil {
		t.Fatal("potNotExceeded=true: expected error, got nil")
	}
}

func TestManualJog_PDOActiveAndReady_ClockwiseSetsPositiveSetpoints(t *testing.T) {
	driverStatusMap = cmap.New()
	t.Cleanup(func() { driverStatusMap = cmap.New() })

	activatePDO(t)
	d := stubDevice("A")
	d.Device.Name = "A"
	d.Driver = &mockDriver{}
	d.PdoReady = true
	d.PdoJogReady = true
	settings.SetDriverSettings("A", settings.DriverSettings{JogFeed: 5})
	d.Device.RPMConst = 100

	if err := ManualJog(d, 1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := d.desiredTargetVelocity.Load(); got != 500 {
		t.Errorf("desiredTargetVelocity = %d, want 500 (1 * 100 * 5)", got)
	}
	if !d.pdoJogEnabled.Load() {
		t.Error("pdoJogEnabled should be true after ManualJog")
	}
}

func TestManualJog_PDOActiveAndReady_CounterClockwiseSetsNegativeSetpoints(t *testing.T) {
	driverStatusMap = cmap.New()
	t.Cleanup(func() { driverStatusMap = cmap.New() })

	activatePDO(t)
	d := stubDevice("A")
	d.Device.Name = "A"
	d.Driver = &mockDriver{}
	d.PdoReady = true
	d.PdoJogReady = true
	settings.SetDriverSettings("A", settings.DriverSettings{JogFeed: 5})
	d.Device.RPMConst = 100

	if err := ManualJog(d, -1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := d.desiredTargetVelocity.Load(); got != -500 {
		t.Errorf("desiredTargetVelocity = %d, want -500 (-1 * 100 * 5)", got)
	}
}

func TestManualJog_PDONotActiveOrNotReady_JogGuardReturnsError(t *testing.T) {
	driverStatusMap = cmap.New()
	t.Cleanup(func() { driverStatusMap = cmap.New() })

	// PdoReady=false makes ManualJog attempt FastPowerOn first, which needs
	// real hardware (SDO calls) and will itself fail/hang without a live
	// master. Confirm instead that when PDO IS active but PdoJogReady=false,
	// the trailing PDO-only guard rejects the jog without reaching PDO setup.
	activatePDO(t)
	d := stubDevice("A")
	d.Device.Name = "A"
	d.Driver = &mockDriver{}
	d.PdoReady = true // skip FastPowerOn
	d.PdoJogReady = false

	if err := ManualJog(d, 1); err == nil {
		t.Fatal("PdoJogReady=false: expected error, got nil")
	}
}

// ─── StopJog — guard clause and happy path ───────────────────────────────────

func TestStopJog_PDOActiveAndReady_ZeroesVelocityAndDisablesJog(t *testing.T) {
	activatePDO(t)
	d := stubDevice("A")
	d.Driver = &mockDriver{}
	d.PdoJogReady = true
	d.desiredTargetVelocity.Store(12345)
	d.pdoJogEnabled.Store(true)

	if err := StopJog(d); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := d.desiredTargetVelocity.Load(); got != 0 {
		t.Errorf("desiredTargetVelocity = %d, want 0 after StopJog", got)
	}
	if d.pdoJogEnabled.Load() {
		t.Error("pdoJogEnabled should be false after StopJog")
	}
}

// ─── hasTargetReached — top-level guard ──────────────────────────────────────
//
// This function is pure Go/atomics — no cgo, no real hardware. The only
// thing that touches an interface is masterDevice.Driver.IsTargetReached(sw),
// which mockDriver already implements as a static bool field. Every test
// below sets d.Driver = &mockDriver{} so Phase 2 never nil-derefs, even in
// tests that never reach Phase 2.

func TestHasTargetReached_NotPdoPosReady_ReturnsError(t *testing.T) {
	activatePDO(t)
	d := stubDevice("A")
	d.Driver = &mockDriver{}
	d.PdoPosReady = false

	err := hasTargetReached(d)
	if err == nil {
		t.Fatal("PdoPosReady=false: expected error, got nil")
	}
}

func TestHasTargetReached_PDONotActive_ReturnsError(t *testing.T) {
	setPDOInactive(t)
	d := stubDevice("A")
	d.Driver = &mockDriver{}
	d.PdoPosReady = true

	err := hasTargetReached(d)
	if err == nil {
		t.Fatal("PDO not active: expected error, got nil")
	}
}

// ─── hasTargetReached — Phase 1 (CiA-402 handshake) ──────────────────────────

func TestHasTargetReached_Phase1_FaultDuringHandshake_ReturnsError(t *testing.T) {
	activatePDO(t)
	d := stubDevice("A")
	d.Driver = &mockDriver{}
	d.PdoPosReady = true
	d.ppSetpointPending.Store(true)
	d.PDOStatus.Store(1 << 3) // fault bit set

	err := hasTargetReached(d)
	if err == nil || !strings.Contains(err.Error(), "Phase1") || !strings.Contains(err.Error(), "fault") {
		t.Fatalf("expected Phase1 fault error, got %v", err)
	}
}

func TestHasTargetReached_Phase1_PDOStoppedDuringHandshake_ReturnsError(t *testing.T) {
	activatePDO(t) // must start true to clear the top-level guard
	d := stubDevice("A")
	d.Driver = &mockDriver{}
	d.PdoPosReady = true
	d.ppSetpointPending.Store(true) // keeps Phase 1's loop iterating

	// Flip pdoActive to false shortly after entry, so the top-level guard
	// passes first and Phase 1's *own* internal !IsPDOActive() check (inside
	// the handshake loop) is what actually triggers the error — not the
	// top-level guard rejecting the call before it even starts.
	go func() {
		time.Sleep(20 * time.Millisecond)
		pdoActive.Store(false)
	}()

	done := make(chan error, 1)
	go func() { done <- hasTargetReached(d) }()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "PDO cyclic stopped") {
			t.Fatalf("expected 'PDO cyclic stopped' error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("hasTargetReached (PDO stopped mid-handshake) did not return promptly")
	}
}

func TestHasTargetReached_Phase1_HandshakeTimeout_ReturnsError(t *testing.T) {
	// Deadline is a hardcoded 2s in the source — this test is inherently slow.
	activatePDO(t)
	d := stubDevice("A")
	d.Driver = &mockDriver{}
	d.PdoPosReady = true
	d.ppSetpointPending.Store(true) // never cleared -> handshake never completes

	start := time.Now()
	err := hasTargetReached(d)
	elapsed := time.Since(start)

	if err == nil || !strings.Contains(err.Error(), "handshake timeout") {
		t.Fatalf("expected handshake timeout error, got %v", err)
	}
	if elapsed < 1900*time.Millisecond {
		t.Errorf("returned too fast (%v) — expected ~2s handshake deadline", elapsed)
	}
}

// ─── hasTargetReached — Phase 1.5 (bit10 must clear) ─────────────────────────

func TestHasTargetReached_Phase15_FaultDuringWait_ReturnsError(t *testing.T) {
	activatePDO(t)
	d := stubDevice("A")
	d.Driver = &mockDriver{}
	d.PdoPosReady = true
	d.ppSetpointPending.Store(false) // skip Phase 1
	d.PDOStatus.Store(1<<3 | 1<<10)  // fault + stale bit10

	err := hasTargetReached(d)
	if err == nil || !strings.Contains(err.Error(), "Phase1.5") || !strings.Contains(err.Error(), "fault") {
		t.Fatalf("expected Phase1.5 fault error, got %v", err)
	}
}

func TestHasTargetReached_Phase15_Bit10NeverClears_ReturnsError(t *testing.T) {
	// Deadline is a hardcoded 500ms in the source.
	activatePDO(t)
	d := stubDevice("A")
	d.Driver = &mockDriver{}
	d.PdoPosReady = true
	d.ppSetpointPending.Store(false)
	d.PDOStatus.Store(1 << 10) // bit10 stuck HIGH — stale "target reached" from a prior move
	d.desiredTargetPosition.Store(1000)
	d.PDOPos.Store(1000)

	start := time.Now()
	err := hasTargetReached(d)
	elapsed := time.Since(start)

	if err == nil || !strings.Contains(err.Error(), "motor did not start") {
		t.Fatalf("expected 'motor did not start' error, got %v", err)
	}
	if elapsed < 450*time.Millisecond {
		t.Errorf("returned too fast (%v) — expected ~500ms bit10-clear deadline", elapsed)
	}
}

// ─── hasTargetReached — Phase 2 (stability + tolerance check) ────────────────
//
// bit10=0 in PDOStatus lets these tests clear Phase 1.5 immediately; Phase 2
// itself doesn't re-check bit10 from PDOStatus — completion is driven purely
// by masterDevice.Driver.IsTargetReached(sw), which mockDriver controls.

func TestHasTargetReached_Phase2_TargetReachedWithinTolerance_ReturnsNil(t *testing.T) {
	activatePDO(t)
	d := stubDevice("A")
	d.Driver = &mockDriver{targetReached: true}
	d.PdoPosReady = true
	d.ppSetpointPending.Store(false)
	d.PDOStatus.Store(0) // bit10=0, no fault -> clears Phase 1.5 instantly
	d.desiredTargetPosition.Store(20000)
	d.PDOPos.Store(20000) // diff=0, well within the 500-pulse tolerance

	done := make(chan error, 1)
	go func() { done <- hasTargetReached(d) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected nil (target reached within tolerance), got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("hasTargetReached (success path) did not return promptly")
	}
}

func TestHasTargetReached_Phase2_FalseTargetReached_LargeDiff_ReturnsError(t *testing.T) {
	activatePDO(t)
	d := stubDevice("A")
	d.Driver = &mockDriver{targetReached: true}
	d.PdoPosReady = true
	d.ppSetpointPending.Store(false)
	d.PDOStatus.Store(0)
	d.desiredTargetPosition.Store(20000)
	d.PDOPos.Store(10000) // diff=10000, far outside the 500-pulse tolerance

	done := make(chan error, 1)
	go func() { done <- hasTargetReached(d) }()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "false target reached") {
			t.Fatalf("expected 'false target reached' error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("hasTargetReached (false target reached) did not return promptly")
	}
}

func TestHasTargetReached_Phase2_EmergencyAbort_ReturnsErrorAndClearsFlag(t *testing.T) {
	activatePDO(t)
	d := stubDevice("A")
	d.Driver = &mockDriver{targetReached: false}
	d.PdoPosReady = true
	d.ppSetpointPending.Store(false)
	d.PDOStatus.Store(0)
	d.posMoveAborted.Store(true)

	err := hasTargetReached(d)
	if err == nil || !strings.Contains(err.Error(), "cancelled by emergency") {
		t.Fatalf("expected emergency-cancelled error, got %v", err)
	}
	if d.posMoveAborted.Load() {
		t.Error("posMoveAborted should be reset to false after being consumed")
	}
}
