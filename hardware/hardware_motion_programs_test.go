//go:build hardware

package hardware_test

// ============================================================================
// Motion Program Test Suite
// ============================================================================

import (
	channels "EtherCAT/channels"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	gosocketio "github.com/graarh/golang-socketio"
)

// ─── helpers ──────────────────────────────────────────────────────────────────

func normalizeTarget(deg float64) float64 {
	if deg > 0 {
		return math.Mod(deg, 360)
	}
	return math.Mod(deg+360, 360)
}

func buildProgram(feedrate int, positions ...float64) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "G01 F%d;\nG90;\nG68;\n", feedrate)
	for _, p := range positions {
		fmt.Fprintf(&sb, "A%.3f;\n", p)
	}
	sb.WriteString("M30;\n")
	return sb.String()
}

func runProgram(t *testing.T, name, ncContent string, timeout time.Duration) float64 {
	t.Helper()
	saveNC(t, name, ncContent)
	stopExecution(t)

	completed := make(chan struct{}, 1)
	ex := sioConnect(t)
	defer ex.close()
	ex.on("program_complete", func(c *gosocketio.Channel, msg channels.SocketMessage) {
		select {
		case completed <- struct{}{}:
		default:
		}
	})

	go func() { ex.emit(t, "execute", map[string]string{"file_name": name}) }()
	time.Sleep(200 * time.Millisecond)

	const pollInterval = 80 * time.Millisecond
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		select {
		case <-completed:
			pos := actualPosDeg(t)
			t.Logf("program complete at %.3f°", pos)
			return pos
		case <-time.After(pollInterval):
		}
	}

	pos := actualPosDeg(t)
	t.Errorf("program %s timed out after %s at pos=%.3f°", name, timeout, pos)
	emergencyReset(t)
	return pos
}

func assertPosAfterProgram(t *testing.T, gotPos, wantPos, tol float64) {
	t.Helper()
	diff := math.Abs(gotPos - wantPos)
	if diff > 180 {
		diff = 360 - diff
	}
	if diff > tol {
		t.Errorf("final position=%.3f° want=%.3f° ±%.2f° (diff=%.3f°)",
			gotPos, wantPos, tol, diff)
	} else {
		t.Logf("✓ position=%.3f° want=%.3f° diff=%.3f°", gotPos, wantPos, diff)
	}
}

func returnToStart(t *testing.T, startPos float64, timeout time.Duration) {
	t.Helper()
	current := actualPosDeg(t)
	diff := math.Abs(current - startPos)
	if diff > 180 {
		diff = 360 - diff
	}
	if diff < 0.1 {
		return
	}
	t.Logf("returning to start: %.3f° (current=%.3f°)", startPos, current)
	prog := fmt.Sprintf("G01 F20;\nG90;\nG68;\nA%.3f;\nM30;\n", startPos)
	runProgram(t, "motion_return.nc", prog, timeout)
}

// ─── test setup ───────────────────────────────────────────────────────────────

func setupMotionPrograms(t *testing.T) (startPos float64, timeout time.Duration) {
	t.Helper()
	if os.Getenv("ALLOW_HARDWARE_TESTS") != "1" {
		t.Skip("set ALLOW_HARDWARE_TESTS=1")
	}
	if os.Getenv("ALLOW_MOTION_TESTS") != "1" {
		t.Skip("set ALLOW_MOTION_TESTS=1 — ensure shaft is free")
	}
	if masterIsIdle(t) {
		t.Skip("master in Idle phase")
	}

	settings := loadSettings(t)
	if a, ok := settings["A"]; ok && a.FinSignal != 0 {
		if os.Getenv("ALLOW_ECS_SIMULATION") == "1" {
			t.Logf("fin_signal=%d — keeping enabled for ECS co-simulation", a.FinSignal)
			startMockPLC(t)
		} else {
			t.Logf("fin_signal=%d — temporarily setting to 0 for standard motion tests", a.FinSignal)
			if err := setFinSignal(t, 0); err != nil {
				t.Fatalf("cannot set fin_signal=0: %v", err)
			}
			t.Cleanup(func() {
				setFinSignal(t, a.FinSignal)
				t.Logf("fin_signal restored to %d", a.FinSignal)
			})
			time.Sleep(400 * time.Millisecond)
		}
	}

	startPos = actualPosDeg(t)
	timeout = 30 * time.Second
	t.Logf("start position: %.3f°", startPos)
	return
}

// ─── Test: sample program from spec ───────────────────────────────────────────

func TestMotionProgram_SampleFromSpec(t *testing.T) {
	startPos, timeout := setupMotionPrograms(t)
	totalTimeout := timeout * 8

	const prog = "G01 F20;\nG90;\nG68;\nA-90;\nA-60;\nA-90;\nA-120;\nA0;\nA-180;\nA-210;\nA240;\nM30;\n"
	finalPos := runProgram(t, "motion_spec_sample.nc", prog, totalTimeout)

	wantFinal := normalizeTarget(240)
	assertPosAfterProgram(t, finalPos, wantFinal, positionTolerance)

	t.Cleanup(func() { returnToStart(t, startPos, timeout) })
}

// ─── Test: negative positions ─────────────────────────────────────────────────

func TestMotionProgram_NegativePositions(t *testing.T) {
	startPos, timeout := setupMotionPrograms(t)

	tests := []struct {
		name    string
		command float64
		wantDeg float64
	}{
		{"A-90_becomes_270", -90, 270},
		{"A-60_becomes_300", -60, 300},
		{"A-120_becomes_240", -120, 240},
		{"A-180_becomes_180", -180, 180},
		{"A-210_becomes_150", -210, 150},
		{"A-270_becomes_90", -270, 90},
		{"A-360_becomes_0", -360, 0},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			prog := fmt.Sprintf("G01 F20;\nG90;\nG68;\nA%.1f;\nM30;\n", tc.command)
			got := runProgram(t, "motion_neg.nc", prog, timeout)
			assertPosAfterProgram(t, got, tc.wantDeg, positionTolerance)
		})
	}

	t.Cleanup(func() { returnToStart(t, startPos, timeout) })
}

// ─── Test: positive positions ─────────────────────────────────────────────────

func TestMotionProgram_PositivePositions(t *testing.T) {
	startPos, timeout := setupMotionPrograms(t)

	tests := []struct {
		name    string
		command float64
		wantDeg float64
	}{
		{"A0", 0, 0},
		{"A90", 90, 90},
		{"A180", 180, 180},
		{"A240", 240, 240},
		{"A270", 270, 270},
		{"A350", 350, 350},
		{"A360_wraps_to_0", 360, 0},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			prog := fmt.Sprintf("G01 F20;\nG90;\nG68;\nA%.1f;\nM30;\n", tc.command)
			got := runProgram(t, "motion_pos.nc", prog, timeout)
			assertPosAfterProgram(t, got, tc.wantDeg, positionTolerance)
		})
	}

	t.Cleanup(func() { returnToStart(t, startPos, timeout) })
}

// ─── Test: back-to-back same direction moves ──────────────────────────────────

func TestMotionProgram_SequentialSameDirection(t *testing.T) {
	startPos, timeout := setupMotionPrograms(t)

	prog := buildProgram(20, 0, 30, 60, 90, 120)
	got := runProgram(t, "motion_seq_cw.nc", prog, timeout*5)
	assertPosAfterProgram(t, got, 120, positionTolerance)
	t.Logf("✓ CW sequential moves: ended at %.3f°", got)

	t.Cleanup(func() { returnToStart(t, startPos, timeout) })
}

// ─── Test: direction reversals ────────────────────────────────────────────────

func TestMotionProgram_DirectionReversals(t *testing.T) {
	startPos, timeout := setupMotionPrograms(t)

	prog := buildProgram(20, 90, 270, 90, 180)
	got := runProgram(t, "motion_zigzag.nc", prog, timeout*4)
	assertPosAfterProgram(t, got, 180, positionTolerance)
	t.Logf("✓ direction reversals: ended at %.3f°", got)

	t.Cleanup(func() { returnToStart(t, startPos, timeout) })
}

// ─── Test: large moves ────────────────────────────────────────────────────────

func TestMotionProgram_LargeMoves(t *testing.T) {
	startPos, timeout := setupMotionPrograms(t)
	largeTimeout := 45 * time.Second

	tests := []struct {
		name    string
		from    float64
		to      float64
		wantDeg float64
	}{
		{"0_to_180", 0, 180, 180},
		{"180_to_0", 180, 0, 0},
		{"0_to_270", 0, 270, 270},
		{"270_to_90", 270, 90, 90},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			startProg := fmt.Sprintf("G01 F20;\nG90;\nG68;\nA%.1f;\nM30;\n", tc.from)
			runProgram(t, "motion_large_start.nc", startProg, timeout)

			moveProg := fmt.Sprintf("G01 F20;\nG90;\nG68;\nA%.1f;\nM30;\n", tc.to)
			got := runProgram(t, "motion_large_move.nc", moveProg, largeTimeout)
			assertPosAfterProgram(t, got, tc.wantDeg, positionTolerance)
		})
	}

	t.Cleanup(func() { returnToStart(t, startPos, largeTimeout) })
}

// ─── Test: feedrate variations ────────────────────────────────────────────────

func TestMotionProgram_FeedrateVariations(t *testing.T) {
	startPos, timeout := setupMotionPrograms(t)

	feedrates := []int{1, 5, 10, 15, 20}
	target := 90.0

	for _, f := range feedrates {
		f := f
		t.Run(fmt.Sprintf("F%d", f), func(t *testing.T) {
			prog := fmt.Sprintf("G01 F%d;\nG90;\nG68;\nA%.1f;\nM30;\n", f, target)
			got := runProgram(t, "motion_feed.nc", prog, timeout)
			assertPosAfterProgram(t, got, target, positionTolerance)
			t.Logf("✓ F%d move to %.1f°: final=%.3f°", f, target, got)

			returnToStart(t, startPos, timeout)
		})
	}
}

// ─── Test: same position (no-op fast-path) ────────────────────────────────────

func TestMotionProgram_SamePosition_FastPath(t *testing.T) {
	_, timeout := setupMotionPrograms(t)

	start := actualPosDeg(t)
	prog := fmt.Sprintf("G01 F20;\nG90;\nG68;\nA%.3f;\nM30;\n", start)
	got := runProgram(t, "motion_noop.nc", prog, timeout)
	assertPosAfterProgram(t, got, start, positionTolerance)
	t.Logf("✓ no-op fast-path: stayed at %.3f°", got)
}

// ─── Test: G91 relative mode ──────────────────────────────────────────────────

func TestMotionProgram_RelativeMode_MultiStep(t *testing.T) {
	startPos, timeout := setupMotionPrograms(t)

	prog := "G01 F20;\nG91;\nA30;\nA30;\nA30;\nA-90;\nM30;\n"
	got := runProgram(t, "motion_rel_multi.nc", prog, timeout*4)

	assertPosAfterProgram(t, got, startPos, positionTolerance)
	t.Logf("✓ relative multi-step: returned to start %.3f°", got)

	t.Cleanup(func() { returnToStart(t, startPos, timeout) })
}

// ─── Test: switch between G90 and G91 mid-program ────────────────────────────

func TestMotionProgram_MixedAbsoluteRelative(t *testing.T) {
	startPos, timeout := setupMotionPrograms(t)

	prog := "G01 F20;\nG90;\nG68;\nA90;\nG91;\nA30;\nG90;\nG68;\nA0;\nM30;\n"
	got := runProgram(t, "motion_mixed.nc", prog, timeout*3)
	assertPosAfterProgram(t, got, 0, positionTolerance)
	t.Logf("✓ mixed G90/G91: ended at %.3f° (want 0°)", got)

	t.Cleanup(func() { returnToStart(t, startPos, timeout) })
}

// ─── Test: drive health after all programs ────────────────────────────────────

func TestMotionProgram_DriveHealthAfterAllMoves(t *testing.T) {
	startPos, timeout := setupMotionPrograms(t)

	prog := buildProgram(20, 90, 180, 270, 0)
	runProgram(t, "motion_health.nc", prog, timeout*4)

	val, raw, err := cia402UploadSDO("0", "0x603f", "0", "uint16")
	if err != nil {
		t.Fatalf("read error code: %v", err)
	}
	if val != 0 {
		t.Errorf("drive fault after programs: 0x%04x raw=%s", uint16(val), raw)
	} else {
		t.Logf("✓ error code = 0x0000 after all programs — drive healthy")
	}

	sw, _, err := cia402UploadSDO("0", "0x6041", "0", "uint16")
	if err != nil {
		t.Fatalf("read statusword: %v", err)
	}
	if uint16(sw)&0x006F != 0x0027 {
		t.Errorf("statusword=0x%04x — drive not in Operation Enabled after programs", uint16(sw))
	} else {
		t.Logf("✓ drive still in Operation Enabled (statusword=0x%04x)", uint16(sw))
	}

	t.Cleanup(func() { returnToStart(t, startPos, timeout) })
}

// ─── visible motion helpers ───────────────────────────────────────────────────

func announceMove(t *testing.T, from, to float64, feedrate int) {
	t.Helper()
	degPerSec := float64(feedrate) * 6
	dist := math.Abs(to - from)
	if dist > 180 {
		dist = 360 - dist
	}
	durationSec := dist / degPerSec
	t.Logf("")
	t.Logf("══════════════════════════════════════════")
	t.Logf("  MOVING: %.1f° → %.1f°", from, to)
	t.Logf("  Distance : %.1f°   Speed: F%d (%.0f°/s)", dist, feedrate, degPerSec)
	t.Logf("  Duration : ~%.1f seconds", durationSec)
	t.Logf("══════════════════════════════════════════")
	time.Sleep(400 * time.Millisecond)
}

func moveAndWatch(t *testing.T, name, program string, targetDeg, timeoutSec float64) float64 {
	t.Helper()

	saveNC(t, name, program)

	go func() {
		s := sioConnect(t)
		s.emit(t, "stop_execution", map[string]string{})
		time.Sleep(200 * time.Millisecond)
		s.close()
	}()
	time.Sleep(800 * time.Millisecond)

	ex := sioConnect(t)
	defer ex.close()
	go func() { ex.emit(t, "execute", map[string]string{"file_name": name}) }()
	time.Sleep(200 * time.Millisecond)

	t.Logf("▶ Program started — watching position...")

	const stableWindow = 0.05
	timeoutDur := time.Duration(timeoutSec * float64(time.Second))
	deadline := time.Now().Add(timeoutDur)
	lastPos := actualPosDeg(t)
	motionSeen := false
	stableAt := time.Time{}
	nextLog := time.Now().Add(1 * time.Second)

	for time.Now().Before(deadline) {
		time.Sleep(80 * time.Millisecond)
		pos := actualPosDeg(t)

		moved := math.Abs(pos-lastPos) > stableWindow
		if moved {
			motionSeen = true
			stableAt = time.Time{}
		} else if stableAt.IsZero() {
			stableAt = time.Now()
		}
		lastPos = pos

		if time.Now().After(nextLog) {
			t.Logf("  ● pos = %.3f°  (target %.3f°)", pos, targetDeg)
			nextLog = time.Now().Add(1 * time.Second)
		}

		if !motionSeen && !stableAt.IsZero() && time.Since(stableAt) > 300*time.Millisecond {
			finalPos := actualPosDeg(t)
			diff := math.Abs(finalPos - targetDeg)
			if diff > 180 {
				diff = 360 - diff
			}
			t.Logf("\n■ Motion complete (already at target)")
			t.Logf("  Final position : %.3f°", finalPos)
			t.Logf("  Target         : %.3f°", targetDeg)
			t.Logf("  Error          : %.3f°", diff)
			if diff <= 0.5 {
				t.Logf("  ✓ PASS")
			} else {
				t.Errorf("  ✗ FAIL — error %.3f° exceeds 0.5°", diff)
			}
			return finalPos
		}

		if motionSeen && !stableAt.IsZero() && time.Since(stableAt) > 500*time.Millisecond {
			finalPos := actualPosDeg(t)
			diff := math.Abs(finalPos - targetDeg)
			if diff > 180 {
				diff = 360 - diff
			}
			t.Logf("")
			t.Logf("■ Motion complete")
			t.Logf("  Final position : %.3f°", finalPos)
			t.Logf("  Target         : %.3f°", targetDeg)
			t.Logf("  Error          : %.3f°", diff)
			if diff <= 0.5 {
				t.Logf("  ✓ PASS")
			} else {
				t.Errorf("  ✗ FAIL — error %.3f° exceeds 0.5°", diff)
			}
			return finalPos
		}
	}

	pos := actualPosDeg(t)
	t.Errorf("■ TIMEOUT after %.0fs — pos=%.3f° target=%.3f°", timeoutSec, pos, targetDeg)
	es := sioConnect(t)
	go func() {
		es.emit(t, "emergency", map[string]string{})
		time.Sleep(1500 * time.Millisecond)
		es.emit(t, "reset", map[string]string{})
		es.close()
	}()
	time.Sleep(2500 * time.Millisecond)
	return actualPosDeg(t)
}

// ─── visible motion tests ─────────────────────────────────────────────────────

func TestMotionProgram_Visible_SlowForwardAndBack(t *testing.T) {
	startPos, _ := setupMotionPrograms(t)
	const feedrate = 3
	const moveDeg = 90.0

	target := math.Mod(startPos+moveDeg, 360)

	announceMove(t, startPos, target, feedrate)
	prog := fmt.Sprintf("G01 F%d;\nG90;\nG68;\nA%.3f;\nM30;\n", feedrate, target)
	moveAndWatch(t, "visible_fwd.nc", prog, target, 30)

	t.Logf("--- Pausing 2s at %.1f° ---", target)
	time.Sleep(2 * time.Second)

	announceMove(t, target, startPos, feedrate)
	retProg := fmt.Sprintf("G01 F%d;\nG90;\nG68;\nA%.3f;\nM30;\n", feedrate, startPos)
	moveAndWatch(t, "visible_ret.nc", retProg, startPos, 30)
}

func TestMotionProgram_Visible_QuarterTurns(t *testing.T) {
	startPos, _ := setupMotionPrograms(t)
	const feedrate = 3

	t.Logf("══════════════════════════════════════════")
	t.Logf("  QUARTER TURN SEQUENCE  (F%d = 18°/s)", feedrate)
	t.Logf("  0° → 90° → 180° → 270° → 0°")
	t.Logf("══════════════════════════════════════════")

	waypoints := []float64{0, 90, 180, 270, 0}
	for i := 0; i < len(waypoints)-1; i++ {
		from := waypoints[i]
		to := waypoints[i+1]
		announceMove(t, from, to, feedrate)
		prog := fmt.Sprintf("G01 F%d;\nG90;\nG68;\nA%.1f;\nM30;\n", feedrate, to)
		moveAndWatch(t, "visible_quarter.nc", prog, to, 30)
		if i < len(waypoints)-2 {
			t.Logf("--- Pausing 2s at %.0f° ---", to)
			time.Sleep(2 * time.Second)
		}
	}

	current := actualPosDeg(t)
	diff := math.Abs(current - startPos)
	if diff > 180 {
		diff = 360 - diff
	}
	if diff > 0.5 {
		retProg := fmt.Sprintf("G01 F%d;\nG90;\nG68;\nA%.3f;\nM30;\n", feedrate, startPos)
		moveAndWatch(t, "visible_ret.nc", retProg, startPos, 30)
	}
}

func TestMotionProgram_Visible_SpecSweep(t *testing.T) {
	startPos, _ := setupMotionPrograms(t)

	t.Logf("══════════════════════════════════════════")
	t.Logf("  SPEC SWEEP  (F5 = 30°/s)")
	t.Logf("  A-90→270° A-60→300° A-90→270°")
	t.Logf("  A-120→240° A0→0° A-180→180°")
	t.Logf("  A-210→150° A240→240°")
	t.Logf("══════════════════════════════════════════")

	const prog = "G01 F5;\nG90;\nG68;\nA-90;\nA-60;\nA-90;\nA-120;\nA0;\nA-180;\nA-210;\nA240;\nM30;\n"
	moveAndWatch(t, "visible_sweep.nc", prog, 240, 90)

	time.Sleep(1 * time.Second)
	retProg := fmt.Sprintf("G01 F5;\nG90;\nG68;\nA%.3f;\nM30;\n", startPos)
	moveAndWatch(t, "visible_ret.nc", retProg, startPos, 60)
}

func TestMotionProgram_Visible_VerySlow(t *testing.T) {
	startPos, _ := setupMotionPrograms(t)
	const feedrate = 1

	t.Logf("══════════════════════════════════════════")
	t.Logf("  VERY SLOW: F1 = 6°/s")
	t.Logf("  10° steps × 9 = 90° total")
	t.Logf("  Each step ~1.7s")
	t.Logf("══════════════════════════════════════════")

	current := startPos
	for step := 1; step <= 9; step++ {
		target := math.Mod(startPos+float64(step)*10, 360)
		announceMove(t, current, target, feedrate)
		prog := fmt.Sprintf("G01 F%d;\nG90;\nG68;\nA%.3f;\nM30;\n", feedrate, target)
		moveAndWatch(t, "visible_slow.nc", prog, target, 30)
		current = target
		time.Sleep(300 * time.Millisecond)
	}

	announceMove(t, current, startPos, feedrate)
	retProg := fmt.Sprintf("G01 F%d;\nG90;\nG68;\nA%.3f;\nM30;\n", feedrate, startPos)
	moveAndWatch(t, "visible_ret.nc", retProg, startPos, 60)
}

// ─── Test: emergency during motion — stops cleanly and recovers ─────────────
//
// Covers the previously-untested "hardware fault/recovery" scenario: trigger
// a real emergency stop mid-motion, confirm the drive actually stops moving
// (not just that the executor stops issuing commands), then confirm a fresh
// move succeeds afterward — proving the emergency path leaves the drive in a
// fully recoverable state rather than some half-stopped intermediate mode.
func TestMotionProgram_EmergencyDuringMotion_StopsAndRecovers(t *testing.T) {
	startPos, timeout := setupMotionPrograms(t)

	// Slow + large move so there's a wide window to inject the emergency
	// signal well before the program would complete on its own.
	target := normalizeTarget(startPos + 300)
	prog := buildProgram(5, target) // F5 — deliberately slow
	saveNC(t, "motion_emergency.nc", prog)
	stopExecution(t)

	ex := sioConnect(t)
	defer ex.close()
	go func() { ex.emit(t, "execute", map[string]string{"file_name": "motion_emergency.nc"}) }()

	// Let the move actually get underway before interrupting it.
	time.Sleep(1500 * time.Millisecond)
	posBeforeEmergency := actualPosDeg(t)
	t.Logf("position before emergency: %.3f°", posBeforeEmergency)

	ex.emit(t, "emergency", map[string]string{})

	// emergencyRampStop takes ~10 steps * 10ms + 20ms settle ≈ 120ms in the
	// PDO-jog case; give it a generous margin to fully decelerate and settle
	// regardless of which mode (jog/PP) it caught the drive in.
	time.Sleep(1 * time.Second)
	posAfterRamp := actualPosDeg(t)

	// Confirm the motor actually stopped moving — not just that the executor
	// stopped issuing new commands. If the drive were still coasting/moving,
	// position would keep changing across this second sample window.
	time.Sleep(1 * time.Second)
	posAfterSettle := actualPosDeg(t)

	if diff := angularDiff(posAfterRamp, posAfterSettle); diff > 0.5 {
		t.Errorf("motor still moving after emergency stop: %.3f° -> %.3f° (diff=%.3f°)",
			posAfterRamp, posAfterSettle, diff)
	}
	t.Logf("position after emergency settle: %.3f° (was %.3f° before emergency)",
		posAfterSettle, posBeforeEmergency)

	// Recover: clear the emergency/fault state exactly as the UI would.
	ex.emit(t, "reset", map[string]string{})
	time.Sleep(2 * time.Second)

	// Confirm the drive is fully operational again with a small, fast move.
	recoverTarget := normalizeTarget(posAfterSettle + 10)
	recoveredPos := runProgram(t, "motion_emergency_recovery.nc",
		buildProgram(20, recoverTarget), timeout)
	assertPosAfterProgram(t, recoveredPos, recoverTarget, positionTolerance)

	t.Cleanup(func() { returnToStart(t, startPos, timeout) })
}

// ─── Test: repeated emergency/reset cycles — no cumulative state corruption ──
//
// A single emergency/reset cycle recovering cleanly doesn't prove the drive
// stays healthy under repeated real-world use (operator hitting E-stop
// multiple times per shift). This drives the cycle 3 times in a row and
// requires a successful small move after each one.
func TestMotionProgram_RepeatedEmergencyResetCycles(t *testing.T) {
	startPos, timeout := setupMotionPrograms(t)
	t.Cleanup(func() { returnToStart(t, startPos, timeout) })

	const cycles = 3
	pos := startPos

	for i := 1; i <= cycles; i++ {
		t.Logf("=== emergency/reset cycle %d/%d ===", i, cycles)

		target := normalizeTarget(pos + 60)
		prog := buildProgram(5, target)
		name := fmt.Sprintf("motion_emergency_cycle_%d.nc", i)
		saveNC(t, name, prog)
		stopExecution(t)

		ex := sioConnect(t)
		go func() { ex.emit(t, "execute", map[string]string{"file_name": name}) }()

		time.Sleep(1 * time.Second)
		ex.emit(t, "emergency", map[string]string{})
		time.Sleep(1 * time.Second)
		ex.close()

		posAfterEmergency := actualPosDeg(t)

		resetConn := sioConnect(t)
		resetConn.emit(t, "reset", map[string]string{})
		time.Sleep(2 * time.Second)
		resetConn.close()

		recoverTarget := normalizeTarget(posAfterEmergency + 10)
		recoveredPos := runProgram(t, fmt.Sprintf("motion_emergency_cycle_%d_recovery.nc", i),
			buildProgram(20, recoverTarget), timeout)
		assertPosAfterProgram(t, recoveredPos, recoverTarget, positionTolerance)

		pos = recoveredPos
		t.Logf("cycle %d recovered successfully at %.3f°", i, pos)
	}
}
