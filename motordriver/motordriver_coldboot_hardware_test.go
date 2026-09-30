//go:build hardware

package motordriver

// TestColdBoot_FullLifecycle — the complete system lifecycle, exercised from
// scratch against real hardware:
//
//   0 -> SDO:        InitMaster()'s pre-activation sequence (RequestMaster,
//                     ScanBus/auto-discovery, per-device configureDriver/
//                     PowerOn/SetupPDOPosition, setupDrivers).
//   SDO -> PDO:      StartPDOCyclic (ecrt_master_activate) — master reaches
//                     Operation phase, SDO window closes, PDO active.
//   Functional:      Jog, zero reference, system reset, emergency stop —
//                     each exercised via the same production entry points
//                     jamun itself calls, not a simulation of them.
//   Clean shutdown:  StopSystem() — stops every listener and gracefully
//                     walks the PDS down before releasing the master.
//
// WHY "PROGRAM RUN" (a real .nc G-code file end to end) IS NOT INCLUDED HERE:
// Running a real program requires the executors package (plugin loading,
// YAML parsing, RunCodeFile). executors already imports motordriver
// (command_executor.go), so motordriver importing executors back would be an
// illegal import cycle — this test cannot reach that layer from inside this
// package. Full end-to-end program execution is already covered separately
// by hardware/hardware_motion_programs_test.go, which drives a normally-
// booted jamun instance over its real REST/WebSocket API. Duplicating that
// here would mean rebuilding the plugin/YAML loading layer just for this one
// test, which is disproportionate to the value gained.
//
// WHY THIS IS SAFE EVEN IF YOU FORGET TO STOP JAMUN:
// The IgH EtherCAT master enforces single ownership at the driver level.
// ecrt_request_master() fails immediately if jamun already holds the master
// — InitMaster() returns a clean error rather than doing anything to a
// running system. Worst case: a fast, harmless failure with a clear message.
//
// WHY THIS IS DELIBERATELY EXCLUDED FROM EVERY AUTOMATED RUN:
// test-all-report.sh's hardware-readonly and hardware-ecs phases match tests
// via -run TestHardwareSmoke|...|TestHwMotordriver and
// -run ^(TestMotion_|TestMotionProgram_|TestHwMotordriver_) respectively.
// Neither pattern matches "TestColdBoot" — intentionally. Running this during
// a normal `test-all-report.sh full` pass (which explicitly pre-checks that
// jamun IS running) would immediately and correctly fail at InitMaster().
//
// HOW TO RUN:
//
//   sudo systemctl stop jamun.service
//   ALLOW_COLD_BOOT_TEST=1 ALLOW_MOTION_TESTS=1 go test -tags=hardware -count=1 \
//     -timeout 180s -run TestColdBoot -v ./motordriver/...
//   sudo systemctl start jamun.service   # restore normal operation afterward

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	channels "EtherCAT/channels"
	helper "EtherCAT/helper"
	"EtherCAT/settings"
)

func requireColdBootTest(t *testing.T) {
	t.Helper()
	if os.Getenv("ALLOW_COLD_BOOT_TEST") != "1" {
		t.Skip("set ALLOW_COLD_BOOT_TEST=1 to run the cold-boot test — " +
			"this calls InitMaster() for real and requires jamun.service to be stopped first " +
			"(sudo systemctl stop jamun.service)")
	}
	if os.Getenv("ALLOW_MOTION_TESTS") != "1" {
		t.Skip("set ALLOW_MOTION_TESTS=1 — this test commands real jog, zero-reference, and emergency-stop motion")
	}
	if IsPDOActive() {
		t.Fatal("PDO is already active — jamun appears to be running. " +
			"Stop it first: sudo systemctl stop jamun.service")
	}
}

func runEtherCATCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cli := os.Getenv("ETHERCAT_CLI")
	if cli == "" {
		cli = "ethercat"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, cli, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// setupRealConfigsForColdBoot copies the real configs/ and settings/
// directories into the test binary's own exec-relative location.
//
// WHY THIS IS NEEDED: InitMaster() (via ParseDeviceConfig, ParseEthercatAddressConfig,
// etc.) resolves config paths through helper.AppendWDPath, which is based on
// os.Executable() — the running BINARY's own directory. For a real deployed
// jamun binary that's a stable, known location with configs/ and settings/
// as sibling directories. For a `go test`-compiled binary it's a fresh
// temporary go-build directory every run, which has neither. This isn't a
// hardware or jamun-state problem — InitMaster() and settings.SaveHomingReference
// would fail this way regardless of whether jamun is running, because the
// mismatch is purely about where the test binary itself executes from.
//
// settings/ was added after the first cold-boot run surfaced it: the boot
// sequence and zero-reference fast path both ran correctly, but
// SaveHomingReference silently failed with "no such file or directory"
// because settings/settings.json was never copied — the exact same
// AppendWDPath mismatch as configs/, just for a different directory.
//
// This mirrors the exact same fix already used by writeExecRelativeYAML
// elsewhere in this test suite (which writes one synthetic file to
// helper.AppendWDPath("")) — here we copy the whole real directories
// instead, sourced from CONFIG_DIR (the same env var test-all-report.sh and
// hardware_smoke_test.go already use) and its sibling settings/ directory.
func setupRealConfigsForColdBoot(t *testing.T) {
	t.Helper()

	configSrc := strings.TrimSpace(os.Getenv("CONFIG_DIR"))
	if configSrc == "" {
		configSrc = "/home/pi/gosrc/src/EtherCAT/configs"
	}
	if _, err := os.Stat(configSrc); err != nil {
		t.Fatalf("real configs directory not found at %q (set CONFIG_DIR to override): %v", configSrc, err)
	}

	// settings/ is a sibling of configs/ under the same repo root.
	repoRoot := filepath.Dir(configSrc)
	settingsSrc := filepath.Join(repoRoot, "settings")
	if _, err := os.Stat(settingsSrc); err != nil {
		t.Fatalf("real settings directory not found at %q (derived as a sibling of CONFIG_DIR): %v", settingsSrc, err)
	}

	execDir := helper.AppendWDPath("")

	copyIfMissing := func(src, dstName string) {
		dst := filepath.Join(execDir, dstName)
		if _, err := os.Stat(dst); err == nil {
			t.Logf("%s already present at %s — leaving as-is", dstName, dst)
			return
		}
		t.Logf("copying real %s from %s to test exec dir %s", dstName, src, dst)
		if err := helper.CopyDir(src, dst); err != nil {
			t.Fatalf("failed to copy %s into test exec dir: %v", dstName, err)
		}
		t.Cleanup(func() {
			os.RemoveAll(dst)
		})
	}

	copyIfMissing(configSrc, "configs")
	copyIfMissing(settingsSrc, "settings")
}

func TestColdBoot_FullLifecycle(t *testing.T) {
	requireColdBootTest(t)
	setupRealConfigsForColdBoot(t)

	// ═══════════════════════════════════════════════════════════════════
	// PHASE 1: 0 -> SDO -> Operation/PDO
	// ═══════════════════════════════════════════════════════════════════
	t.Run("Boot_ZeroToSDOToOperation", func(t *testing.T) {
		// Matches main.go's real startup order exactly: LoadDriverSettings()
		// happens BEFORE InitMaster() there too. InitMaster() alone never
		// loads settings from disk — that's why JogFeed/HomingApos always
		// read as 0 in earlier runs of this test, regardless of what was
		// actually in settings.json: nothing had ever populated the
		// in-memory settingsRoot map at all.
		t.Log("=== Loading driver settings (matches main.go's real startup order) ===")
		if err := settings.LoadDriverSettings(); err != nil {
			t.Logf("LoadDriverSettings returned an error (may be partial — see driver_settings.go comments on lenient parsing): %v", err)
		}

		t.Log("=== Calling InitMaster(): RequestMaster, ScanBus, per-device SDO setup ===")
		start := time.Now()
		err := InitMaster()
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("InitMaster() failed after %v: %v — if jamun.service is running, "+
				"stop it first: sudo systemctl stop jamun.service", elapsed, err)
		}
		t.Logf("InitMaster() succeeded in %v", elapsed)

		out, cliErr := runEtherCATCLI(t, "master")
		if cliErr != nil {
			t.Errorf("could not query master state via ethercat CLI: %v", cliErr)
		} else if !strings.Contains(out, "Phase: Operation") {
			t.Fatalf("master not in Operation phase after InitMaster():\n%s", out)
		} else {
			t.Log("Confirmed: master in Operation phase")
		}

		if !IsPDOActive() {
			t.Fatal("IsPDOActive() = false after InitMaster() succeeded — expected true")
		}
		t.Log("Confirmed: IsPDOActive() = true")

		if len(masterDevices) == 0 {
			t.Fatal("masterDevices is empty after a successful InitMaster()")
		}
		for _, dev := range masterDevices {
			sw := uint16(dev.PDOStatus.Load() & 0xFFFF)
			t.Logf("device %s: PDOStatus=0x%04X PdoReady=%v PdoPosReady=%v",
				dev.Name, sw, dev.PdoReady, dev.PdoPosReady)
		}

		// Give the system time to genuinely stabilize before commanding
		// motion — matches what happens naturally in real production use
		// (jog was never attempted within seconds of boot there). Polls
		// the exact same status-bit check the cyclic task itself uses for
		// opEnabled (status & 0x006F == 0x0027, see cia402NextControlword
		// in pdo_cyclic_task.go) rather than guessing a fixed delay.
		t.Log("=== Waiting for drive to reach Operation Enabled before commanding any motion ===")
		for _, dev := range masterDevices {
			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				sw := uint16(dev.PDOStatus.Load() & 0xFFFF)
				if sw&0x006F == 0x0027 {
					t.Logf("device %s: reached Operation Enabled (sw=0x%04X)", dev.Name, sw)
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			finalSw := uint16(dev.PDOStatus.Load() & 0xFFFF)
			if finalSw&0x006F != 0x0027 {
				t.Logf("WARNING: device %s did not reach Operation Enabled within 10s (sw=0x%04X) — "+
					"motion phases below may not behave as expected", dev.Name, finalSw)
			}
		}
	})

	// If boot failed, nothing below can run safely — stop here.
	if !IsPDOActive() || len(masterDevices) == 0 {
		t.Fatal("boot phase did not leave the system in a usable state — aborting remaining phases")
	}
	dev := masterDevices[0]

	// ═══════════════════════════════════════════════════════════════════
	// PHASE 2: Jog
	// ═══════════════════════════════════════════════════════════════════
	t.Run("Jog_CWThenCCW", func(t *testing.T) {
		ds := settings.GetDriverSettings(dev.Name)
		if ds.JogFeed == 0 {
			t.Skip("JogFeed=0 — configure drive settings before testing jog")
		}

		// Jog for long enough to cover more than one full rotation, so
		// the drive stops at a genuinely non-zero position — this is
		// what makes ZeroReference (next phase) exercise a real PP-mode
		// move back to zero instead of always hitting the delta==0 fast
		// path. At JogFeed=20 * rpm_const=120000 = 120 deg/sec, 3.7s
		// covers ~444 degrees (one full rotation plus safety margin).
		const jogDuration = 3700 * time.Millisecond

		t.Log("=== ManualJog clockwise for over one full rotation ===")
		startPos := dev.PDOPos.Load()
		if err := ManualJog(dev, 1); err != nil {
			t.Fatalf("ManualJog(CW): %v", err)
		}
		time.Sleep(jogDuration)
		if err := StopJog(dev); err != nil {
			t.Fatalf("StopJog after CW: %v", err)
		}
		endPos := dev.PDOPos.Load()
		t.Logf("CW jog + stop completed — position moved from %d to %d (delta=%d pulses)",
			startPos, endPos, endPos-startPos)

		t.Log("=== ManualJog counter-clockwise ===")
		if err := ManualJog(dev, -1); err != nil {
			t.Fatalf("ManualJog(CCW): %v", err)
		}
		time.Sleep(200 * time.Millisecond)
		if err := StopJog(dev); err != nil {
			t.Fatalf("StopJog after CCW: %v", err)
		}
		t.Log("CCW jog + stop completed")
	})

	// Give the background position poller (pollDrivePositionProcess, runs
	// every 50ms) time to fully catch up before zero-reference reads the
	// cached position it depends on.
	//
	// moveToZero computes its move distance from driverStatus.currentPosition
	// — a cached DEGREE value updated by pollDrivePositionProcess calling
	// currentDriverPosition() roughly every 50ms — not from the live raw
	// PDO position directly. Calling zero-reference immediately after
	// StopJog risks reading a cache that's still catching up with where
	// the jog actually left the drive: at 120 deg/sec, even one missed
	// 50ms poll cycle bakes ~6 degrees of error into the shortest-path
	// calculation, even though the resulting move itself would still
	// complete without any error (it just wouldn't be going to the
	// correct place). A 2-second gap is roughly 40 poll cycles of margin.
	t.Log("=== Pausing 2s to let the position cache settle before zero-referencing ===")
	time.Sleep(2 * time.Second)

	// ═══════════════════════════════════════════════════════════════════
	// PHASE 3: Zero reference
	// ═══════════════════════════════════════════════════════════════════
	t.Run("ZeroReference", func(t *testing.T) {
		if !dev.PdoPosReady {
			t.Skip("PdoPosReady=false — PDO position not configured for this device")
		}
		beforeApos := settings.GetDriverSettings(dev.Name).HomingApos

		t.Log("=== moveToZero: move to homing reference and save it ===")
		if err := moveToZero(dev); err != nil {
			t.Fatalf("moveToZero: %v", err)
		}

		// Capture the device's actual position at the moment moveToZero
		// returned — this is what SaveHomingReference should have written.
		actualPos := dev.PDOPos.Load()
		afterApos := settings.GetDriverSettings(dev.Name).HomingApos
		t.Logf("HomingApos before=%d after=%d actualPDOPos=%d", beforeApos, afterApos, actualPos)

		// Two checks, not one exact-equality check:
		//
		// 1. beforeApos != afterApos — this is the real invariant that
		//    matters: confirms the in-memory cache genuinely updated, not
		//    just the file. This is exactly what was missing when
		//    SaveHomingReference's cache update silently failed (see the
		//    driver_settings.go fix) — before/after would stay identical
		//    even though moveToZero returned nil.
		//
		// 2. |afterApos - actualPos| within a small tolerance — NOT exact
		//    equality. Confirmed on real hardware: moveToZero snapshots
		//    the position internally and saves it, but by the time this
		//    test re-reads dev.PDOPos.Load() externally (after
		//    sendECSFinSignal's digital-output sequence completes), the
		//    live position can have drifted a couple of encoder counts
		//    from normal settling — that's real, expected behavior, not
		//    a save failure.
		const positionTolerance = 50 // encoder counts
		if beforeApos == afterApos {
			t.Errorf("HomingApos did not change (before=after=%d) — the homing reference "+
				"was not actually saved to the in-memory cache", afterApos)
		} else if diff := afterApos - actualPos; diff < -positionTolerance || diff > positionTolerance {
			t.Errorf("HomingApos after moveToZero = %d, actual PDO position = %d (diff=%d) — "+
				"expected these to be within %d counts of each other", afterApos, actualPos, diff, positionTolerance)
		} else {
			t.Log("Confirmed: HomingApos correctly saved and matches the device's actual position")
		}
		t.Log("Zero reference completed")
	})

	// ═══════════════════════════════════════════════════════════════════
	// PHASE 4: System reset — via the exact production trigger path
	// ═══════════════════════════════════════════════════════════════════
	t.Run("SystemReset", func(t *testing.T) {
		t.Log("=== Triggering reset via channels.ResetDriverSystem <- true (production trigger path) ===")
		channels.ResetDriverSystem <- true

		// resetSystemWorker's own sequence has a bounded ~2s fault-clear
		// wait plus a 500ms settle — give it generous headroom.
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if IsPDOActive() {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}

		if !IsPDOActive() {
			t.Fatal("PDO not active after reset — system did not survive the reset cycle")
		}
		t.Log("Confirmed: system still operational after reset (PDO active)")

		for _, d := range masterDevices {
			sw := uint16(d.PDOStatus.Load() & 0xFFFF)
			t.Logf("post-reset device %s: PDOStatus=0x%04X", d.Name, sw)
		}
	})

	// ═══════════════════════════════════════════════════════════════════
	// PHASE 5: Emergency stop
	// ═══════════════════════════════════════════════════════════════════
	t.Run("EmergencyStop_AndRecovery", func(t *testing.T) {
		ds := settings.GetDriverSettings(dev.Name)
		if ds.JogFeed == 0 {
			t.Skip("JogFeed=0 — configure drive settings before testing emergency stop")
		}

		t.Log("=== Starting jog, then triggering emergency() mid-motion ===")
		if err := ManualJog(dev, 1); err != nil {
			t.Fatalf("ManualJog before emergency: %v", err)
		}
		time.Sleep(100 * time.Millisecond)

		// emergency() blocks until emergencyRampStop() completes: velocity
		// is stepped down in 10 equal increments over 100ms (not zeroed
		// instantly), plus a 20ms settle before mode switching — this is
		// what actually prevents the abrupt "bounce-back jerk" that an
		// instant stop would cause (the drive's own internal ramp would
		// overshoot, then CSP standby would snap it back). Measuring the
		// elapsed time here is real evidence the smooth ramp actually ran,
		// not just that the function returned nil.
		emergencyStart := time.Now()
		if err := emergency(dev); err != nil {
			t.Fatalf("emergency(): %v", err)
		}
		emergencyElapsed := time.Since(emergencyStart)
		t.Logf("emergency() returned after %v — motion should now be stopped smoothly", emergencyElapsed)

		const minExpectedRampTime = 110 * time.Millisecond // 10 steps x 10ms + 20ms settle, minus scheduling slack
		if emergencyElapsed < minExpectedRampTime {
			t.Errorf("emergency() returned in only %v — expected at least %v for the "+
				"smooth velocity ramp-down to have actually run; this may indicate an "+
				"abrupt stop instead of the intended controlled deceleration",
				emergencyElapsed, minExpectedRampTime)
		} else {
			t.Logf("Confirmed: emergency() took %v, consistent with the full smooth ramp-down having executed", emergencyElapsed)
		}

		time.Sleep(200 * time.Millisecond)
		if !IsPDOActive() {
			t.Fatal("PDO not active after emergency stop — expected system to remain operational")
		}

		t.Log("=== Verifying recovery: system still responsive after emergency ===")
		channels.ResetDriverSystem <- true
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if IsPDOActive() {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if !IsPDOActive() {
			t.Fatal("PDO not active after post-emergency reset — recovery failed")
		}

		// Confirm the drive actually accepts a new command after recovery —
		// the real proof that emergency+reset left the system usable, not
		// just "a function returned nil".
		if err := ManualJog(dev, 1); err != nil {
			t.Fatalf("ManualJog after emergency+reset recovery: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
		if err := StopJog(dev); err != nil {
			t.Fatalf("StopJog after recovery jog: %v", err)
		}
		t.Log("Confirmed: system accepted and completed a new jog command after emergency+recovery")
	})

	// ═══════════════════════════════════════════════════════════════════
	// PHASE 6: Clean shutdown
	// ═══════════════════════════════════════════════════════════════════
	t.Run("CleanShutdown", func(t *testing.T) {
		t.Log("=== Calling StopSystem(): stops all listeners, then gracefully walks PDS down and releases master ===")
		start := time.Now()
		StopSystem()
		t.Logf("StopSystem() completed in %v", time.Since(start))

		if IsPDOActive() {
			t.Error("IsPDOActive() = true after StopSystem() — expected false")
		} else {
			t.Log("Confirmed: IsPDOActive() = false after shutdown")
		}
	})

	t.Log("=== Full lifecycle complete: 0 -> SDO -> Operation -> [jog, zero-ref, reset, emergency+recovery] -> clean shutdown ===")
	t.Log("Remember to restart jamun: sudo systemctl start jamun.service")
}
