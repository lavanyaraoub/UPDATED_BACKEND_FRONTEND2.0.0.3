//go:build hardware

package hardware_test

// ============================================================================
// Long-Duration Reliability Test Suite
// ============================================================================
//
// These are soak tests, not functional correctness tests — they're designed
// to catch slow degradation (state drift, memory growth, watchdog false
// trips) that only shows up after sustained or repeated operation, which the
// rest of the hardware suite (each test running once, quickly) cannot catch.
//
// All of these are opt-in on top of the standard ALLOW_HARDWARE_TESTS /
// ALLOW_MOTION_TESTS gates, via ALLOW_LONG_DURATION_TESTS=1, since they take
// much longer than the rest of the suite (the watchdog test alone runs for
// 30 minutes by default) and are not something you want firing on every
// routine hardware test run.
//
// KNOWN GAP — goroutine-level leak detection: main.go currently has its
// pprof endpoint commented out, so there is no way for an external test to
// query the running jamun process's live goroutine count. What we CAN check
// from outside the process is resident memory (RSS) via /proc/<pid>/status,
// which catches a broad class of leaks (goroutines that hold buffers, growing
// caches, etc.) even though it can't isolate "goroutine count" specifically.
// If precise goroutine-level verification is wanted later, uncommenting the
// pprof line in main.go and adding a /debug/pprof/goroutine?debug=1 fetch
// here would close that gap.

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func requireLongDurationOptIn(t *testing.T) {
	t.Helper()
	if os.Getenv("ALLOW_LONG_DURATION_TESTS") != "1" {
		t.Skip("set ALLOW_LONG_DURATION_TESTS=1 to run long-duration reliability tests")
	}
}

// ─── Test: 100 repeated small motion cycles ──────────────────────────────────
//
// A single successful move proves the happy path works. Repeating it 100
// times back-to-back is what actually catches slow state drift: stale
// latchCounters, a leaking retry-count, position/pulse rounding error
// accumulating, or a PDO handshake race that only shows up after volume.
func TestReliability_Repeated100SmallMotionCycles(t *testing.T) {
	startPos, timeout := setupMotionPrograms(t)
	t.Cleanup(func() { returnToStart(t, startPos, timeout) })

	const cycles = 100
	const stepDeg = 5.0

	pos := startPos
	var maxDiff float64
	failures := 0

	for i := 1; i <= cycles; i++ {
		// Alternate direction each cycle so we don't just walk in one
		// direction for 500° — stay in a tight band around the start
		// position while still exercising forward AND reverse moves.
		delta := stepDeg
		if i%2 == 0 {
			delta = -stepDeg
		}
		target := normalizeTarget(pos + delta)

		prog := buildProgram(20, target)
		name := fmt.Sprintf("motion_reliability_%03d.nc", i)
		got := runProgram(t, name, prog, timeout)

		diff := angularDiff(got, target)
		if diff > maxDiff {
			maxDiff = diff
		}
		if diff > positionTolerance {
			failures++
			t.Errorf("cycle %d/%d: position=%.3f° want=%.3f° diff=%.3f° (exceeds tolerance)",
				i, cycles, got, target, diff)
		}
		pos = got

		if i%10 == 0 {
			t.Logf("progress: %d/%d cycles complete, current pos=%.3f°, max diff so far=%.3f°",
				i, cycles, pos, maxDiff)
		}
	}

	t.Logf("completed %d cycles: %d failures, max diff=%.3f°", cycles, failures, maxDiff)
	if failures > 0 {
		t.Errorf("%d/%d cycles exceeded position tolerance — see individual cycle errors above", failures, cycles)
	}
}

// ─── Test: 30-minute idle PDO watchdog stability ─────────────────────────────
//
// Confirms the EtherCAT master stays in Operation phase over a long idle
// period with no motion commands at all. Catches watchdog false-trips or
// slow bus-health degradation that a short test can't observe — the whole
// point of a watchdog test is giving it enough real time to either behave
// or misbehave.
func TestReliability_IdlePDOWatchdog_30Min(t *testing.T) {
	requireLongDurationOptIn(t)
	if os.Getenv("ALLOW_HARDWARE_TESTS") != "1" {
		t.Skip("set ALLOW_HARDWARE_TESTS=1")
	}

	duration := 30 * time.Minute
	if v := strings.TrimSpace(os.Getenv("WATCHDOG_TEST_DURATION")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			duration = d
		}
	}
	const pollInterval = 10 * time.Second

	t.Logf("watching EtherCAT master phase for %s (poll every %s)", duration, pollInterval)

	deadline := time.Now().Add(duration)
	checks, drops := 0, 0

	for time.Now().Before(deadline) {
		out, err := runEtherCATReadOnly(t, "master")
		checks++
		if err != nil {
			drops++
			t.Errorf("check %d: ethercat master command failed: %v\n%s", checks, err, out)
		} else if !strings.Contains(out, "Phase: Operation") {
			drops++
			t.Errorf("check %d: master not in Operation phase:\n%s", checks, out)
		}

		if checks%30 == 0 { // roughly every 5 minutes at the default poll interval
			t.Logf("progress: %d checks over %s elapsed, %d drop(s) so far",
				checks, time.Since(deadline.Add(-duration)).Round(time.Second), drops)
		}

		time.Sleep(pollInterval)
	}

	t.Logf("watchdog stability check complete: %d checks, %d drop(s) over %s", checks, drops, duration)
	if drops > 0 {
		t.Errorf("master phase dropped out of Operation %d time(s) during idle watchdog window", drops)
	}
}

// ─── Test: process memory stability over a repeated stress sequence ─────────
//
// Samples the jamun process's resident memory (RSS) before and after a
// smaller repeated-motion stress sequence, catching unbounded memory growth.
// This is a coarser signal than a real goroutine-level leak check (see the
// package doc comment above for why that isn't available from outside the
// process right now), but it catches the broad, operationally-relevant
// failure mode: the process's memory footprint growing without bound over
// sustained use.
func TestReliability_ProcessMemoryStability_Over50Cycles(t *testing.T) {
	requireLongDurationOptIn(t)
	startPos, timeout := setupMotionPrograms(t)
	t.Cleanup(func() { returnToStart(t, startPos, timeout) })

	pid, err := jamunPID(t)
	if err != nil {
		t.Skipf("could not determine jamun process PID (%v) — skipping memory stability check", err)
	}

	rssBefore, err := readProcRSSKB(pid)
	if err != nil {
		t.Skipf("could not read RSS for pid %d (%v) — skipping memory stability check", pid, err)
	}
	t.Logf("jamun pid=%d RSS before stress sequence: %d KB", pid, rssBefore)

	const cycles = 50
	pos := startPos
	for i := 1; i <= cycles; i++ {
		delta := 5.0
		if i%2 == 0 {
			delta = -5.0
		}
		target := normalizeTarget(pos + delta)
		prog := buildProgram(20, target)
		got := runProgram(t, fmt.Sprintf("motion_memcheck_%03d.nc", i), prog, timeout)
		pos = got
	}

	// Give the Go runtime a moment to run a GC cycle naturally rather than
	// sampling RSS at the instant the last allocation happened.
	time.Sleep(3 * time.Second)

	rssAfter, err := readProcRSSKB(pid)
	if err != nil {
		t.Fatalf("could not read RSS after stress sequence: %v", err)
	}
	growthKB := rssAfter - rssBefore
	t.Logf("jamun pid=%d RSS after %d cycles: %d KB (growth: %d KB)", pid, cycles, rssAfter, growthKB)

	const maxAllowedGrowthKB = 20 * 1024 // 20MB — generous; catches unbounded growth, not GC noise
	if growthKB > maxAllowedGrowthKB {
		t.Errorf("RSS grew by %d KB over %d cycles (allowed: %d KB) — possible memory leak",
			growthKB, cycles, maxAllowedGrowthKB)
	}
}

// jamunPID finds the running jamun process's PID via pgrep. Returns an error
// (not a t.Fatal) so callers can skip gracefully on systems where the process
// name differs or pgrep isn't installed, rather than failing the whole suite.
func jamunPID(t *testing.T) (int, error) {
	t.Helper()
	out, err := exec.Command("pgrep", "-x", "jamun").Output()
	if err != nil {
		return 0, fmt.Errorf("pgrep failed: %w", err)
	}
	lines := strings.Fields(strings.TrimSpace(string(out)))
	if len(lines) == 0 {
		return 0, fmt.Errorf("pgrep returned no matching process")
	}
	pid, err := strconv.Atoi(lines[0])
	if err != nil {
		return 0, fmt.Errorf("unexpected pgrep output %q: %w", lines[0], err)
	}
	return pid, nil
}

// readProcRSSKB reads VmRSS (resident memory, in KB) for the given PID from
// /proc/<pid>/status.
func readProcRSSKB(pid int) (int, error) {
	f, err := os.Open(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "VmRSS:") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				return 0, fmt.Errorf("unexpected VmRSS line format: %q", line)
			}
			return strconv.Atoi(fields[1])
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("VmRSS not found in /proc/%d/status", pid)
}
