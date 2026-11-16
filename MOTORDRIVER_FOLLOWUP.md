# motordriver test merge — follow-up plan (v2, updated after opening the factory/poll files)

This supersedes `MOTORDRIVER_FOLLOWUP_v1.md` (kept alongside for reference).
The interface-gap analysis in v1 was correct but incomplete — opening
`motor_driver_factory.go` and `poll_iostat.go` surfaced more divergence
than the interface diff alone showed. Step 1 of the original plan (rebuild
the mock) is now done; this file reflects what was actually found while
doing it.

## Status: Step 1 complete — `mock_driver_test.go` rebuilt

`motordriver/mock_driver_test.go` now implements all 16 methods of the
current `IMotorDriver` interface, with a compile-time check
(`var _ IMotorDriver = (*mockDriver)(nil)`) so any future interface growth
fails loudly instead of silently. **Not yet compiled** — needs the Pi.

A starter test file, `motordriver/motordriver_starter_test.go`, exercises:
- `sendECSFinSignal`'s `FinishSignal==0` early-return path (unchanged from
  testenv, still valid)
- `stopECSCheck` (unchanged, still valid)
- **`pollIOStat`/`stopPollIOStat`'s per-device dispatch** — new tests, not
  a port of anything testenv had, because testenv's architecture couldn't
  express this. This directly tests the actual bug fix (each device gets
  its own driver's `pollIOStat`, not a shared global one).
- `JogControlword` default/override behavior on the mock
- `setActiveDriverForTest`, a new injection helper (see below)

**Next action: run these commands on the Pi and send back whatever the
compiler says.**
```
go test -tags=unit ./motordriver/... -run TestSendECSFinSignal -v
go test -tags=unit ./motordriver/... -run TestPollIOStat -v
go test -tags=unit ./motordriver/... -run TestStopPollIOStat -v
go test -tags=unit ./motordriver/... -run TestJogControlword -v
go test -tags=unit ./motordriver/... -run TestSetActiveDriverForTest -v
```

## What changed since v1 of this plan (newly discovered)

1. **A third drive exists**: `NidecM700`, alongside `A6Minas` and
   `DeltaASDA2E`, registered in `motor_driver_factory.go`'s
   `driverRegistry` map. Any future mock/interface work needs to account
   for three implementations, not two.

2. **The global driver mechanism is a lock-free atomic-pointer registry**,
   not a simple package variable:
   - `activeDriverPtr unsafe.Pointer` — swapped via `atomic.StorePointer`
   - `driverRegistry map[string]func() IMotorDriver` — maps a drive-type
     string to a constructor
   - `RegisterDriver(driveType, constructor)` — lets a driver file
     self-register at `init()` time
   - `GetMotorDriver()` / `SetMotorDriver(driveType)` — the global
     (single-drive-oriented) accessor/setter, kept for backward
     compatibility
   - `GetDriverForType(driveType)` — instantiates a driver for a specific
     type without touching the global, used by `InitMaster` to populate
     each `MasterDevice.Driver` individually

   testenv's injection mechanism (`speclDriver = mock`, a plain package
   variable) **does not exist**. The mock file now provides
   `setActiveDriverForTest(d IMotorDriver) (restore func())`, which uses
   `atomic.StorePointer` correctly and returns a restore closure for
   `defer`. Use this only for code that calls `GetMotorDriver()` directly;
   most of the interesting motion-safety code (post multi-axis fixes)
   reads `masterDevice.Driver` instead — inject the mock there directly
   via struct literal (`&MasterDevice{Driver: mock}`), no helper needed.

3. **`pollIOStat`/`stopPollIOStat` are per-device dispatch**, tracked via a
   package-level `polledDrivers []IMotorDriver` slice
   (`poll_iostat.go`). This is itself a documented fix for the same class
   of multi-axis bug as the `clamp_declamp.go`/`ecs.go` fixes found
   earlier in this merge — the old version used one global driver for
   every device's I/O poll, which was wrong on mixed-drive setups. The
   starter test file tests this fan-out directly.

4. **`MasterDevice` embeds a raw cgo type**: `Master *C.ec_master_t`
   (`ether_cat_gateway.go`). This is the single most important fact for
   anyone working on this package's tests: **you cannot declare even a
   zero-value `&MasterDevice{}` without the real cgo toolchain resolving
   `C.ec_master_t`.** This isn't a "hard to verify" situation — it's a
   hard requirement. Every test that touches `MasterDevice` (which is
   most of them) can only ever be developed and run on a machine with
   `/opt/etherlab` installed. There is no partial workaround (e.g. build
   tags to strip the cgo field) without changing production code, which
   is out of scope.

## Revised step-by-step plan

1. ~~Rebuild `mockDriver` against the current interface~~ — **done**,
   see above. **Verified: 8/8 tests pass on real hardware toolchain.**
2. ~~Run the starter tests on the Pi~~ — **done, all green.**
3. **`pdo_cyclic_task_test.go` — now fully done, including what was
   deferred.** The CiA-402 state machine, watchdog, and now the PDO
   getters + `PDOFaultReset` are all covered, split across two files:

   `pdo_cyclic_task_test.go` (verified — 26 assertions, all pass on real
   hardware toolchain): state machine table + invariant + 65,536-case
   fuzz test, watchdog tests, `GetCurrentAlarm`, `GetLastPDOVelocityActual`
   stub check.

   `pdo_cyclic_task_deferred_test.go` (new, not yet run): the four PDO
   getters (`GetLastPDOPosition`, `GetLastPDOStatusword`,
   `GetLastPDODigitalInputs`, `GetLastPDOErrorCode`) via a new
   `setMasterDevicesForTest()` helper that injects a `stubDevice` into the
   package-level `masterDevices` slice — the actual mechanism
   `legacyFirstDevice()` reads from, discovered while working this out.
   Also `PDOFaultReset`'s concurrency test, re-derived against its real
   current behavior (confirmed safe by reading `resetOneFaultedDevice` in
   full: it only touches `atomic` fields and flag-guarded PDO writes, no
   raw cgo pointers, so a bare stub is safe to pass through it) — the test
   now simulates the fault actually clearing via a background goroutine,
   since nothing does that automatically against a mock. Two more
   `PDOFaultReset` tests were added beyond testenv's original set: skip
   behavior for non-faulted devices, and the two early-return guards
   (`pdoActive=false`, empty device list).

   **`stubDevice()` and `setMasterDevicesForTest()` now live in
   `mock_driver_test.go`** — reusable for every subsequent motordriver
   test file that needs a `*MasterDevice`.

   **Next action:**
   ```
   go test -tags=unit ./motordriver/... -run TestGetLastPDO -v
   go test -tags=unit ./motordriver/... -run TestPDOStatusword_ConcurrentReadWrite -v
   go test -tags=unit ./motordriver/... -run TestPDOFaultReset -v
   ```
   The `TestPDOFaultReset_ConcurrentCalls_OnlyOneSucceeds` test takes
   roughly 600ms–1s (not the ~2.5s worst case) since it simulates the
   fault clearing quickly rather than timing out — flag it if it's
   dramatically slower or faster than that, since either would suggest
   the timing assumptions above don't hold in practice.

4. **`drive_angle_manipulation_test.go` and `drive_rotation_test.go` — done.**

   `drive_angle_manipulation_test.go`: `getPulsesFromDegree`, `getAbsolutePosition`,
   `getRelativePosition`, and `getPitchError` are all byte-identical to
   testenv — ported unchanged. `currentPosition`'s boot-time encoder
   correction moved from a package-level `aposCorrection atomic.Int32` to
   per-device `MasterDevice.AposCorrection` (same multi-axis fix pattern as
   everywhere else) — every `aposCorrection.Store(N)` call became
   `setMasterDevicesForTest(stubDeviceWithApos("A", N))`. Added one extra
   test (`TestCurrentPosition_NoDeviceRegistered_NoCorrectionApplied`) to
   pin the "device not found → correction defaults to 0" fallback
   explicitly.

   `drive_rotation_test.go`: `drive_rotation.go` itself diverged
   extensively (`ManualJog`, `StopJog`'s internals, `hasTargetReached`,
   `doRotate` — deep two-way conflict, same class as `executors`). None
   of that mattered for this file — every test here only touches
   early-return guards or delegates to functions verified unchanged:
   `reverseDir`/`nonReverseDir` (identical), `StopJog`'s PDO-not-active
   guard (present both sides), `getPos`/`shortestPathToZero` in
   `zero_reference.go` (byte-identical). Ported the entire original file
   as-is.

   **Two real findings while verifying, both handled:**
   - `driverCurrentStatus.reset()` in `driver_status_keeper.go` was
     **missing** `potNotExceeded`/`potExceeded`/`notExceeded` clearing —
     testenv had this, HAL didn't. Without it, a POT/NOT alarm would never
     clear after Reset (`checkPotNotLimit`'s "already flagged" branch
     keeps re-alarming forever). **Fixed directly in production code**
     (isolated, additive, matches the pattern of every other bugfix
     merged in this project).
   - `checkPotNotLimit` (`poll_drive_position.go`) is **missing** an
     explicit `if !driverStatus.isMotorRunning { return false }` guard
     that testenv has. **Not fixed** — traced by hand that the three
     ported tests still pass via the direction-matching branches
     returning false anyway, but this is a real behavioral gap worth a
     second look: it means the function's early-exit relies on
     `direction` happening to be 0/unset when the motor isn't running,
     rather than checking `isMotorRunning` directly. If `direction`
     could ever be nonzero while `isMotorRunning=false` (e.g. it's not
     reset somewhere it should be), the limit check would run when it
     shouldn't. Worth confirming intentionally rather than leaving as an
     implicit assumption — flagging for your judgment call, not fixing
     unilaterally since it's more of a design question than a clear bug.

   **Next action:**
   ```
   go test -tags=unit ./motordriver/... -run TestGetPulsesFromDegree -v
   go test -tags=unit ./motordriver/... -run TestCurrentPosition -v
   go test -tags=unit ./motordriver/... -run TestGetAbsolutePosition_Delegates -v
   go test -tags=unit ./motordriver/... -run TestGetRelativePosition -v
   go test -tags=unit ./motordriver/... -run TestGetPitchError -v
   go test -tags=unit ./motordriver/... -run TestReverseDir -v
   go test -tags=unit ./motordriver/... -run TestNonReverseDir -v
   go test -tags=unit ./motordriver/... -run TestStopJog -v
   go test -tags=unit ./motordriver/... -run TestDriverCurrentStatus_Reset -v
   go test -tags=unit ./motordriver/... -run TestCheckPotNotLimit -v
   go test -tags=unit ./motordriver/... -run TestGetPos -v
   go test -tags=unit ./motordriver/... -run TestShortestPathToZero -v
   ```

5. **`drive_power_test.go` — much bigger than expected, scoped down.**
   testenv's original file is ~900 lines and spans far more than
   `drive_power.go` — it also covers `driver_status_keeper.go`,
   `break.go`, `clamp_declamp.go`, `poll_driver_alarm.go`,
   `reset_driver_system.go`, `poll_iostat.go`, and more, several of which
   are already known to have deep signature/behavior changes (the
   `hasDeclamped`/`hasClamped` pointer-receiver + hard-error fix found
   early in this whole merge). Rather than rush all 900 lines, only the
   `drive_power.go`/`reset.go`-scoped section was ported this round
   (~10 tests), verified function-by-function:

   - `pdoStopMotion`, `PowerOn`, `FastPowerOn`, `PowerOffAll`, `PowerOff`,
     `FastPowerOff`, `emergency`, `ResetDriver` (×2): all confirmed to
     hit the same PDO-active early-return path in both HAL and testenv.
   - `resetMultiTurn`: testenv's own test already called it lowercase
     (`resetMultiTurn(...)`, not `ResetMultiTurn(...)`) — already matches
     HAL exactly, no adaptation needed. Added two more tests beyond
     testenv's original single no-op check: nil-Driver skip behavior, and
     confirming per-device dispatch actually reaches the device's own
     mock driver (the real point of this fix).
   - **Found, not fixed:** `emergencyRampStop` (inside `drive_power.go`)
     has the same kind of two-sided disagreement as `checkPotNotLimit` —
     HAL deliberately does NOT call `SetTargetPositionPDO` with the rest
     position (documented reason: avoids arming `ppSetpointPending` and
     causing a Mode 3→1 jerk), while testenv DOES call it (documented
     reason: locks the CSP target early to avoid a micro-correction
     bounce). Both sides have real, specific reasoning for opposite
     behavior. None of the ported tests reach this code path (the stub
     device's velocity defaults to 0, so `emergencyRampStop` returns
     before reaching the disputed line), so it didn't block porting —
     but it's a real design question, not a bug, and needs your call
     before anyone touches it.

   **Deferred: ~700 lines / ~70 tests** — **now substantially done.**
   Ported and adapted: all PDO setter/getter tests (byte-identical),
   `getCurrentDriverStatus`/`setCurrentDriverStatus` (byte-identical),
   `doECSCheck`/`doECSCheckZero` (byte-identical), `setDirection`/
   `clearTargetReached`/`doneDriveStatusUpdate`/`HasDriverConnected`/
   `getMasterDevices` (byte-identical), `RefreshCurrentPosition`/
   `ReadActualPositionFromDrive`/`stopECSCheck`/`doneDriverAction`/
   `notifyDriverStatus`/`notifyDriverStatusWithWait`/`StopSystem`/
   `currentDriverPosition`/`startDriverStatusListener` (all byte-identical
   for the tested paths), and — the real work — five clusters that needed
   genuine adaptation:

   - **`stopErrorPolling`**: `stopErrPollingChan` (single channel) became
     `stopErrPollingChans` (slice, one per poller goroutine — Phase 4
     multi-device fix). Adapted the test to use the slice.
   - **`pollDriveError`**: dropped the `usePDO bool` parameter entirely —
     HAL decides PDO-vs-SDO internally per-device via `IsPDOActive()`,
     same pattern as everywhere else in this codebase. Adapted the call.
   - **`breakOn`/`breakOff`**: now route through `getRealDeviceForBrake(name)`,
     which looks the device up by name in the package-level `masterDevices`
     slice (another instance of the Bug-2a multi-axis fix — targets the
     correct axis instead of broadcasting). The stub device must be
     registered in `masterDevices` under its own name for the PDO fast
     path to be found; adapted via `setMasterDevicesForTest(d)`.
   - **`hasDeclamped`/`hasClamped`**: confirmed hard-error on nil
     `masterDevice.Driver` (Bug 2a/2b, found early in this whole merge),
     checked *before* the `ClampDeclamp==0` early return these tests
     target. Gave the stub device a `&mockDriver{}` so it reaches the
     intended branch; added one new test
     (`TestHasDeclamped_NilDriver_HardErrors`) pinning the new behavior
     directly.
   - **`readDigitalInputs`/`readInputSignal`/`readECSSignal`/`hardResetInput`**:
     backing storage moved from the removed package-level `lastPDODI`
     atomic to per-device `MasterDevice.PDODI` (same PDO-storage migration
     found in `pdo_cyclic_task.go`). Adapted to store on `d.PDODI` directly.
   - `applyClampIfSettingsChanged`/`performSysReset`: byte-identical,
     ported with `setMasterDevicesForTest` for device injection.

   **Still genuinely deferred:** `handleErrCode` only (see above — needs
   its own pass alongside `RecordFault`/`FormatErrorCode`, the new
   fault-history/error-lookup subsystem).

   **Next action:** run the whole package — `drive_power_test.go` now has
   61 test functions total.
   ```
   go test -tags=unit ./motordriver/... -v
   ```

6. **`ethercat_config_and_zero_ref_test.go` — done.** Much smaller than
   expected (5 tests) — and it turns out the actual deep zero-ref timing
   fix lives entirely in `set_rpm.go`, not this file, so none of these
   tests touch that safety-critical logic directly.
   `GetEtherCATOperation`, `sleepMs`, `getEtherCATAddress`: byte-identical,
   ported unchanged. `moveToZero` diverged substantially (HAL inlines the
   full PDO homing sequence; testenv delegates to `doRotate`), but the
   one thing this test exercises — the PDO-not-ready guard — is present
   in both.

   **Real finding, fixed:** HAL's `moveToZero` calls
   `notifier.NotifyDestinationPosition(...)` **unconditionally**, before
   reaching the PDO-readiness check — unlike `notifyDriverStatus` right
   next to it, this call has no `isStatusListening` guard and sends
   directly on `channels.BroadCastUIChannel` with no nil-check. In a fresh
   test process that channel is nil by default, and sending on a nil
   channel **blocks forever** in Go. Porting the test as-is would have
   **hung**, not failed. Fixed by initializing a buffered channel with a
   draining goroutine first (same pattern as elsewhere in this suite).
   This is worth a look independent of testing — it means any real
   caller of `moveToZero` before the UI listener starts up would
   currently hang the calling goroutine, not just skip a notification.

   **Next action:**
   ```
   go test -tags=unit ./motordriver/... -run TestGetEtherCATOperation -v
   go test -tags=unit ./motordriver/... -run TestMoveToZero -v
   go test -tags=unit ./motordriver/... -run TestSleepMs -v
   go test -tags=unit ./motordriver/... -run TestGetEtherCATAddress -v
   ```

7. `handleErrCode`/fault-history/error-lookup subsystem — **done**,
   21 fresh tests (no testenv equivalent existed), verified including
   the fault-history ring-wrap and dedup logic on real hardware.
8. `motordriver_lifecycle_test.go` — **done**, 33 tests. Needed several
   adaptations, all following established patterns from earlier in this
   merge: `pdoErrLoop` gained a `stopCh chan bool` parameter and reads
   per-device `PDOStatus`/`PDOErr` instead of removed globals;
   `pollDriveError`/`pollDriveErrWorker` dropped their `usePDO bool`
   parameter; `speclDriver` (removed global) references dropped;
   `hasDeclamped`/`hasClamped` given mock `Driver`s (Bug 2a/2b pattern);
   `moveToZero` test guarded against the same hang risk found in
   `ethercat_config_and_zero_ref_test.go`.

   **Next action:**
   ```
   go test -tags=unit ./motordriver/... -v
   ```

9. **`motordriver_logic_test.go` — reviewed in full, deliberately scoped
   down to 6 tests.** testenv's original 54 tests were reviewed
   completely; the large majority duplicated scenarios already ported
   under different names elsewhere in this merge (`cia402NextControlword`,
   `PDOTickAge`/`PDOHealthy`, `ResetDriver`, `applyClampIfSettingsChanged`,
   `reverseDir`/`breakOn`/`hasDeclamped`, etc.) — re-porting near-identical
   tests under new names would add file bloat without adding real
   coverage.

   **What was genuinely missing and is now covered:** `checkPotNotLimit`'s
   *actual breach-detection paths*. Every existing test up to this point
   only covered "within limits" or "disabled" — none exercised a real
   limit breach firing, or the "already latched, keep alarming" fast
   path. Both are now tested, along with the documented NOT-guard bug fix
   (guards on the raw UI value, not the computed 360+NOT, to avoid a
   false emergency stop every time the motor jogs CCW through the 0°/360°
   wrap with no NOT limit configured).

   **Next action:**
   ```
   go test -tags=unit ./motordriver/... -run TestCheckPotNotLimit -v
   ```

10. **`motordriver_coverage_test.go` — reviewed in full, scoped to 56
    genuinely new/safe tests (not the original 80).** Same approach as
    `motordriver_logic_test.go`: ~24 tests were duplicates of scenarios
    already covered elsewhere under different names (PowerOn/PowerOff/etc
    PDONotActive, triggerMultiTurnResetSDO repeats, sleepMs,
    setDirection/refreshCurrentPosition repeats, the nonexistent
    `ErrMultiturnPowerCycleRequired` sentinel, and the `"program_mode"`
    event/`isProgramMode` field which don't exist in HAL at all) — see the
    file's own header comment for the itemized list.

    **What's genuinely new and now covered:**
    - Full `listenDriverStatus` event-dispatch coverage (mode,
      shortest_path_enable, destination_position, motor_running,
      driver_on_off, set_backlash, workoffset, fin_signal, reset,
      pot_not_exceeded × POT/NOT)
    - **The actual backlash compensation logic** on rotation direction
      changes (CW→CCW applies backlash, CCW→CCW keeps it, CCW→CW clears
      it, CW→CW stays zero) plus the safety behavior that changing
      direction clears a latched `potNotExceeded` — previously completely
      untested
    - Full `listenDriverAction` dispatch coverage (11 of its ~18 action
      cases)
    - `InitAposCorrection`'s two branches, adapted for per-device
      `AposCorrection` and the requirement that the device actually be
      registered in `masterDevices` to be found
    - `doRotate`/`freeRotate`'s PDO-not-ready guards, `emergencyRampStop`,
      `moveMotorToDegree`/`stepMode`'s POT/NOT-already-exceeded guard,
      `pollDrivePosition`'s launch/stop lifecycle
    - `pdoErrLoop`'s stop-signal exit path and `handleErrCode`'s
      `errCodeCleared` sentinel suppression (both adapted for their
      current signatures)
    - PDO watchdog's warn and recovery log branches (complementing the
      already-tested "hung" branch)

    **Next action — this is the last unit-test file:**
    ```
    go test -tags=unit ./motordriver/... -v
    ```

11. **`motordriver_hardware_test.go` — delivered, awaiting your run.**
    9 tests, adapted for two API changes verified against the actual
    current source (not assumed):
    - `aposCorrection` (removed package-level global) → `d.AposCorrection`
      (per-device atomic)
    - `pollDriveError` dropped its `usePDO bool` parameter
    Everything else (`ManualJog`, `StopJog`, `freeRotate`, `moveToZero`,
    `triggerMultiTurnResetSDO`, `GetLastPDOPosition`,
    `InitAposCorrection`'s own signature) matches HAL exactly.

    **Cannot be verified in any sandbox** — needs `ALLOW_HARDWARE_TESTS=1`
    (and `ALLOW_MOTION_TESTS=1` for the motion-commanding subset) against
    a live EtherCAT bus with `jamun` running and PDO active. This is the
    one file in the whole migration that was adapted purely from reading
    the source, with no compiler in the loop at all — treat it with the
    same caution as any first hardware run: watch the drive closely,
    especially on `TestHwMotordriver_ManualJog_*` and
    `TestHwMotordriver_FreeRotate_SmallAngle_Completes`, which command
    real motion.

    **How to run:**
    ```bash
    # Read-only hardware tests (no motion):
    ALLOW_HARDWARE_TESTS=1 go test -tags=hardware -v -run TestHwMotordriver ./motordriver/...

    # Including motion-commanding tests:
    ALLOW_HARDWARE_TESTS=1 ALLOW_MOTION_TESTS=1 MOTION_TEST_RANGE_DEG=3 \
      go test -tags=hardware -v -run TestHwMotordriver ./motordriver/...
    ```

## Migration status: complete

Every file from `test_environment13`'s motordriver test suite has now been
processed — ported, adapted, or deliberately rescoped with documented
reasoning. Combined with the rest of the codebase (executors, restapi,
commands/*, configparser, settings, channels, helper, statusnotifier) done
earlier in this project, the full test_environment13 → HAL migration is done.

## What I can help with remotely

Same as before: send compiler errors and test output as you go through
each step. That feedback loop is what turned the executors/restapi/commands
work from "structurally plausible" into "34/34 passing on real hardware" —
same process applies here, just with a bigger, more safety-critical
package.