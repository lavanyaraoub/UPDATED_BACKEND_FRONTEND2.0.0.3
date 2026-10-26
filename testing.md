# Testing

This document describes the current test system for the EtherCAT motor driver
controller (jamun). It is the canonical reference for how to run tests, what
each phase covers, and what happens at every step of the test runner.

The test suite is split by risk level using Go build tags. The default developer
workflow must remain safe: no EtherCAT bus access, no GPIO, no controlword
writes, no servo enable, and no motor motion.

---

## Current green baseline

**1,645 tests · 1,603 passed · 0 failures · 42 expected skips**

Verified on real hardware (Raspberry Pi, Panasonic A6 Minas drive) as of
July 2026:

```bash
MOTION_TEST_RANGE_DEG=3 \
MOTION_TIMEOUT_S=30 \
make test-coverage-full
```

All 5 phases pass cleanly. Coverage: **~55%**, measured honestly against the
*entire* repository (see [Coverage summary](#coverage-summary) — this number
used to be inflated by a script bug that has since been fixed).

---

## Test levels

| Phase | Build tag | Motor? | Command | Purpose |
|---|---|---|---|---|
| 1 — unit | `unit` | Never | `make test-unit` | Pure logic, no I/O, no hardware |
| 2 — integration | `integration` | Never | `make test-integration` | Multi-package with real files |
| 3 — hardware-readonly | `hardware` | Never | see below | Drive state, settings, REST API |
| 4 — hardware-ecs | `hardware` | **Yes** | see below | Full G-code motion on real hardware |
| 5 — e2e | `e2e` | Never | `make test-e2e` | REST API lifecycle against live app |

There is also a sixth, **separate, deliberately-not-automated** test —
see [Cold-boot lifecycle test](#cold-boot-lifecycle-test) below.

---

## Standard commands

### Full suite — recommended before every deployment

```bash
MOTION_TEST_RANGE_DEG=3 MOTION_TIMEOUT_S=30 make test-coverage-full
```

Runs all 5 automated phases, merges coverage profiles, and generates a
timestamped HTML/Markdown/JSON report under `test-reports/`.

### Daily developer workflow — no hardware required

```bash
make test-coverage
```

Runs unit + integration phases only. No hardware or app needed.

### Open coverage reports

```bash
go tool cover -html=test-reports/latest/coverage-merged.out
```

### Unit tests only

```bash
make test-unit
```

### Integration tests only

```bash
make test-integration
```

### E2E tests only (app must be running on port 5000)

```bash
make test-e2e
```

### Hardware read-only checks only — no motor movement

```bash
bash scripts/test-all-report.sh hardware-readonly
```

### Build and deploy safely

```bash
make all && make deploy
```

The `deploy` target stops the service, validates non-empty binaries, copies
all artefacts atomically, and restarts. **Never copy binaries manually while
the service is running** — this was the root cause of the Err 80 crash loop.

---

## Environment variable reference

| Variable | What it controls | Why it exists |
|---|---|---|
| `ALLOW_HARDWARE_TESTS=1` | Unlocks phases 3 and 4 | Prevents accidental hardware access during regular development |
| `ALLOW_MOTION_TESTS=1` | Unlocks motor movement in phase 4, and in the cold-boot test | Hardware-readonly can run without moving the motor |
| `ALLOW_COLD_BOOT_TEST=1` | Unlocks the cold-boot lifecycle test | Separate gate — this test calls `InitMaster()` directly and **requires jamun.service to be stopped**; see below |
| `ALLOW_ECS_SIMULATION=1` | Jamun simulates ECS signal internally | Without this, motion tests block forever waiting for an external clamp signal |
| `ETHERCAT_INTERFACE=end0` | Tells smoke tests which NIC to check | Wrong interface means smoke tests pass even if EtherCAT is down |
| `MOTION_TEST_RANGE_DEG=3` | Motor moves ±3° per test | Limits mechanical travel |
| `MOTION_TIMEOUT_S=30` | Per-test motion timeout | Prevents a stalled drive from hanging the entire suite |
| `E2E_BASE_URL` | REST API base URL for E2E tests | Default: `http://localhost:5000` |
| `CONFIG_DIR` | Real `configs/` directory to use | Needed by `hardware_smoke_test.go` and the cold-boot test; defaults to the repo's own `configs/` |

---

## What the script does — step by step

When you run `make test-coverage-full` the following happens in sequence.

### Step 0 — Setup (~instant)

Creates a timestamped output directory under `test-reports/`:

```
test-reports/20260713-HHMMSS/
  raw/
    unit.jsonl
    integration.jsonl
    hardware-readonly.jsonl
    hardware-ecs.jsonl
    e2e.jsonl
    phase-status.tsv
  coverage-unit.out
  coverage-integration.out
  coverage-hardware-readonly.out
  coverage-hardware-ecs.out
  coverage-merged.out
  summary.json
  report.html
  report.md
  environment.txt
```

Every run is isolated — no previous report is ever overwritten.

### Step 1 — Hardware pre-checks (~2 seconds)

Before running any tests, the script verifies the system is safe to test:

```bash
ethercat master          # must show "Phase: Operation"
ethercat slaves -v       # must show "State: OP"
ethercat upload 0x603F   # drive error code must be 0x0000
```

**If any check fails the script stops immediately — no tests run.**

### Step 2 — Phase 1: Unit tests (~10 seconds)

```bash
go test -json -tags=unit -count=1 -timeout 300s \
  -coverpkg=./... \
  ./motordriver/... ./helper/... ./configparser/... \
  ./datatypes/... ./executors/... ./channels \
  ./settings ./clientcommunication \
  ./ethercatdevicedatatypes ./serialtest ./licensechecker
```

Pure logic — no filesystem, no channels, no hardware. Note `-coverpkg=./...`
instruments the *entire* repository, not just the packages under test — see
[Coverage summary](#coverage-summary) for why this matters. `ethercatdevicedatatypes`,
`serialtest`, and `licensechecker` were added after discovering their real,
passing tests were never actually being invoked by any phase (only affected
by coverage instrumentation, never executed) — a gap distinct from, and found
while fixing, the coverage measurement issue.

Includes:
- CiA-402 state machine — all 65,536 statusword combinations
- Angle math (`shortestPathToZero`, `getPos`)
- All command plugin handlers (unit paths), including each package's
  `CreateHandler()` factory — previously untested in all 18 `commands/*`
  packages simultaneously (same gap, same fix, across every one)
- Channels (`command_exec_complete`, `motor_driver`, broadcast) — including
  nil-channel safety on every broadcast function (see
  [Fixed bugs](#fixed-bugs))
- Settings (`GetDriverSettings`, `Float64Str`, `ResetEnvSettings`)
- ClientCommunication (`removeClient`, struct validation, `socketEventsCreator`)
- ConfigParser error paths and malformed-line handling
- `pdo_cyclic_task.go`'s pure-Go helpers (`PDOSetDigitalOutputForDevice`,
  `StopPDOCyclic`/`StartPDOCyclic` guard clauses) — previously untested
  despite having zero cgo dependency

### Step 3 — Phase 2: Integration tests (~10 seconds)

```bash
go test -json -tags=integration -count=1 -timeout 600s \
  -coverpkg=./... \
  ./executors ./configparser ./restapi ./settings \
  ./commands/g90 ./commands/g91 ... (all 18 command packages)
```

Multi-package with real temp files. No EtherCAT. Covers:
- Full G-code executor pipeline with real command handlers
- All 18 command plugins including g0, g17, rpm, delay, divide360EnableDisable
- REST API via `net/http/httptest`, including `getFaultHistory`/`clearFaultHistory`'s
  safe surface (the success path of `clearFaultHistory` is deliberately not
  tested — it deletes a real file with genuine production fault history on it)
- Settings disk round-trips on Pi
- `listenCommandExecInput` goroutine all message types
- RS232 toggle paths GET and POST
- ConfigParser file-not-found and malformed YAML error paths

### Step 4 — Phase 3: Hardware read-only (~30 seconds)

Reads the live EtherCAT bus via SDO CLI. No PDO writes, no motor enable.

- Drive parameter checks (mode, error code, statusword, position)
- Settings validation (JogFeed, POT, NOT, work offsets, pitch error)
- REST API smoke tests (programs, file create/rename/delete, `/dac_params`)
- G-code compiler validation (valid accepted, invalid rejected)
- EtherCAT bus health (master Operation phase, slave OP state)
- Motordriver hardware tests (`TestHwMotordriver_*`)

### Step 5 — Phase 4: Hardware motion (~14 minutes)

**The motor physically rotates.** Motion tests verify position to ±0.5°.

- Single-move tests: absolute positive/negative, relative, work offset, feedrate
- G-code programme tests covering all motion scenarios
- `TestHwMotordriver_*` motion-gated tests (ManualJog, freeRotate, moveToZero)

### Step 6 — Phase 5: E2E tests (~5 seconds)

REST API black-box tests against the live running app on port 5000:

- Program lifecycle: save → list → read → rename → delete
- Compile validation: valid accepted, path traversal rejected, empty body rejected
- Concurrent saves (5 goroutines simultaneously)
- CORS preflight headers
- `/dac_params`, `/faq`, `/support` response validation

### Step 7 — Coverage merge + report generation (~5 seconds)

Concatenates all coverage profiles into `coverage-merged.out` and generates
HTML, Markdown, and JSON reports via `scripts/test_report.py`.

---

## Cold-boot lifecycle test

A separate, deliberately-not-automated test that exercises the *entire*
system lifecycle from a genuine cold start — something none of the five
phases above do, since they all assume `jamun` is already running.

**File:** `motordriver/motordriver_coldboot_hardware_test.go`
**Test:** `TestColdBoot_FullLifecycle`

### What it does

1. **0 → SDO → Operation**: calls `InitMaster()` directly — the real
   production boot sequence (`RequestMaster`, `ScanBus`, per-device SDO
   configuration, `StartPDOCyclic`) — and verifies the master reaches
   Operation phase with PDO active.
2. **Jog**: real `ManualJog` for over one full rotation (calculated from
   your actual `JogFeed`/`rpm_const`/`drive_x_ratio` settings), confirming
   genuine physical motion via position delta, not just "no error returned."
3. **Zero reference**: `moveToZero()` from a real, non-zero position, after
   a 2-second settling pause — confirms both the move itself and that the
   homing reference is genuinely saved to the in-memory settings cache
   (see [Fixed bugs](#fixed-bugs)).
4. **System reset**: triggers via `channels.ResetDriverSystem <- true` —
   the exact production trigger path, not a direct function call.
5. **Emergency stop + recovery**: starts a jog, triggers `emergency()`
   mid-motion, verifies the ramp-down actually took the expected ~110ms+
   (proving a real smooth deceleration happened, not an abrupt stop),
   then proves recovery by successfully running a *new* jog command
   afterward.
6. **Clean shutdown**: `StopSystem()` — the complete production shutdown
   sequence.

### Why it's not part of any automated phase

It requires `jamun.service` to be **stopped** — the exact opposite
precondition of every other hardware test, which all assume jamun is
already running. Its test name (`TestColdBoot_*`) deliberately doesn't
match either automated `-run` regex in `test-all-report.sh`
(`hardware-readonly`'s or `hardware-ecs`'s), so it can never be
accidentally swept into a normal `make test-coverage-full` run — which
would otherwise immediately fail, since that run's own pre-check requires
jamun to already be up.

### Why it's safe even if you forget to stop jamun

The IgH EtherCAT master enforces single ownership at the driver level.
If jamun is still running and holding the master, `RequestMaster()` (inside
`InitMaster()`) fails cleanly and immediately — the test cannot interfere
with a running instance. Worst case: a fast, harmless failure telling you
to stop jamun first.

### How to run it

```bash
sudo systemctl stop jamun.service
ALLOW_COLD_BOOT_TEST=1 ALLOW_MOTION_TESTS=1 \
  CONFIG_DIR=/home/pi/gosrc/src/EtherCAT/configs \
  go test -tags=hardware -count=1 -timeout 180s \
  -run TestColdBoot -v ./motordriver/...
sudo systemctl start jamun.service
```

The motor will visibly turn — more than one full rotation during the jog
phase alone, plus additional small confirmatory movements during the
emergency-stop and recovery phases (by design, not drift). Confirm clear
space around the mechanism before running.

### What it doesn't cover, and why

Running a real, complete G-code **program** end-to-end (not just a single
jog/move) requires the `executors` package's plugin-loading machinery.
`executors` already imports `motordriver` (for `command_executor.go`), so
`motordriver` importing `executors` back would be an illegal import cycle
— this test cannot reach that layer from inside `motordriver`. Full
program execution is already covered by the hardware-ecs phase's motion
tests, which drive a normally-booted jamun over its real REST/WebSocket
API instead.

---

## Coverage summary

| Package | Measured | Notes |
|---|---|---|
| `motordriver/statusnotifier` | 100% | Complete |
| `ethercatdevicedatatypes` | 100% | Fixed: tests existed but were never invoked (see [Fixed bugs](#fixed-bugs)) |
| `commands/*` (all 18) | ~90-100% | `CreateHandler()` gap closed across all 18 packages |
| `datatypes` | ~94% | Near-complete |
| `helper` | ~88% | Near-complete |
| `restapi` | ~85% | hotspot/tunnel/rollback success paths deliberately untested — real system calls |
| `executors` | ~80% | licensechecker paths unreachable without network mocking |
| `configparser` | ~80% | File-not-found and malformed paths covered |
| `channels` | ~90% | All broadcast functions now nil-channel-safe and tested |
| `settings` | ~68% | Disk paths covered on Pi |
| `motordriver` | ~50% | CGo/EtherCAT bridge — see note below |
| `licensechecker` | partial | Safe surface fully tested; network-dependent functions deliberately untested |
| `hotspot` | 100% | Full coverage, including wifi.go |
| `tunnel` | ~93% | Real parsing logic + script-launch logic all covered |
| `logger` | ~92% | Only `Fatal()` untested (calls `os.Exit`, can't test safely) |
| `systemupdate` | ~61% | Biggest, riskiest package — real update/rollback logic; the tail ends of `PerformRollback`/`PerformSystemUpdate` past their guard clauses (which call `os.Exit`) are deliberately untested |
| **Measured total** | **~55%** | `go tool cover`, merged all phases, `-coverpkg=./...` (whole repo, not a curated subset) |

**Important history on this number:** for a long time, this project's
`-coverpkg` flags only ever named a curated list of ~27 packages, so
packages with zero tests at all (`logger`, `hotspot`, `tunnel`,
`systemupdate`, `serialtest`, `gpiohandle`, `constants`, `ethercatdevicedatatypes`,
`licensechecker`, `ren`, and the root `EtherCAT` package) never factored
into the denominator — making the reported percentage (previously ~54%)
higher than the codebase's real, whole-repo coverage. This has been fixed:
every phase now uses `-coverpkg=./...`, and the number above is the honest,
whole-repository figure. It's lower than before, but it's real.

**Note on motordriver coverage:** roughly **4.5% of the entire codebase**
(not just motordriver) lives inside functions that make a direct cgo call
into the EtherCAT master library (`RequestMaster`, `SDODownload`,
`SDOUpload`/`SDOUpload2`, `DrivePosition`, the real `ecrt_master_activate`
call inside `InitMaster`, the cyclic task's 1ms hardware loop). This is a
genuine, permanent structural floor — no unit test can cross it, since it's
calling compiled C code that expects real hardware state. The
hardware-readonly/hardware-ecs phases and the cold-boot test *do* exercise
this code against real hardware; it's specifically unit-test coverage that
cannot reach it.

---

## What each phase covers

### Phase 1 — Unit tests

Packages tested: `motordriver`, `motordriver/statusnotifier`, `helper`,
`configparser`, `datatypes`, `executors`, `channels`, `settings`,
`clientcommunication`, `ethercatdevicedatatypes`, `serialtest`, `licensechecker`

Key functions covered:

- **CiA-402 state machine** — all 7 transitions + exhaustive fuzz over 65,536 statuswords
- **PDO watchdog** (`PDOTickAge`, `PDOHealthy`) — staleness detection
- **Alarm rising-edge filter** (`handleErrCode`) — new/repeat/cleared
- **Software safety limits** (`checkPotNotLimit`) — all 6 branch permutations
- **Arc calculations** (`shortestPathToZero`, `getPos`) — CW/CCW, tie-breaking,
  including the "already at target" fast path (skips the PP-mode handshake
  when delta is zero — see [Fixed bugs](#fixed-bugs))
- **WebSocket notification layer** — all broadcast functions, alarm pipeline,
  all nil-channel-safe
- **G-code command parsing** — all `GetCommand` branches, wildcard, multi-command
- **Settings** — GetDriverSettings, GetAllSettings, GetWorkOffset, Float64Str,
  ResetEnvSettings, `SaveHomingReference`'s in-memory cache update
- **Channels** — OpenWaitChannel, NotifyCmdComplete, NotifyMotorDriver,
  SendAlarm, SendLineNumber, DestinationReached — all non-blocking sends
- **ConfigParser** — malformed lines, file-not-found, YAML parse errors
- **MotorDriver** — InitAposCorrection, doRotate/freeRotate PDO guards,
  triggerMultiTurnResetSDO, stopErrorPolling, `PDOSetDigitalOutputForDevice`,
  `StopPDOCyclic`/`StartPDOCyclic` guard clauses
- **Helper utilities** — AES-CFB crypto, file management, path resolution
- **Concurrency safety** — the entire `motordriver` package has been verified
  data-race-free (10/10 clean `-race` runs); several genuine races were found
  and fixed, not just test artifacts (see [Fixed bugs](#fixed-bugs))

### Phase 2 — Integration tests

Packages tested: `executors`, `configparser`, `restapi`, `settings`,
all 18 `commands/*` handlers

Key scenarios covered:

- Programme execution persistence (resolveStartLine, resume, RS232 mode)
- Full G-code executor pipeline — all 18 command handlers, including each
  one's `CreateHandler()` entry point
- Loop handling, mode switching, error propagation, stop flag
- `listenCommandExecInput` — all 5 message types
- `notAPlugin` helper — directory, `.go` file, `.so` file
- RS232 GET/POST toggle with all payload variants
- YAML config parsers — all three parser types, error paths
- REST API file operations via httptest, including `getFaultHistory`

### Phase 3 — Hardware read-only

No motion. Reads live EtherCAT bus state, validates drive parameters,
exercises REST API and G-code compiler against the running app.

### Phase 4 — Hardware motion

Motor physically moves. Motion tests + motordriver hardware tests.
Position verified via socket.io `destination_position` event to ±0.5°.

### Phase 5 — E2E

Tests against the live REST API. Program lifecycle, validation,
concurrent saves, CORS, info endpoints.

---

## Build tag rules

Every tagged test file must start with the build tag on line 1, followed by
a blank line:

```go
//go:build unit

package helper
```

| Tag | Use for |
|---|---|
| `unit` | Pure logic, no I/O, no hardware |
| `integration` | Real files via `t.TempDir()`, no EtherCAT |
| `hardware` | Raspberry Pi only, requires `ALLOW_HARDWARE_TESTS=1` (or `ALLOW_COLD_BOOT_TEST=1` for the cold-boot test specifically) |
| `e2e` | Requires jamun running on port 5000 |

---

## Known skips

All 42 skips are intentional. None represent untested behaviour.

Most skips fall into these categories:

1. **Config-not-at-binary-path** — `ParseDeviceConfig`, `ParseEthercatAddressConfig`,
   `ParseExececutionConfigYML`, `GetErrorString` real-file tests. Expected in
   test binary environment.
2. **Hardware state dependent** — `TestHasDriverConnected`, `TestStopSystem`
   run when jamun is stopped.
3. **Motordriver hardware** — `TestHwMotordriver_*` skip when
   `IsPDOActive()=false` or `masterDevices` is empty. These require the app
   to be running with an active PDO connection.
4. **JogFeed-dependent** — jog and emergency-stop phases of the cold-boot
   test skip if `JogFeed=0` in the current drive settings — a real
   configuration state, not a code gap.

---

## Known issues

- `GetEnvSettings` caches on first call; `ResetEnvSettings()` clears the cache manually. Call it after updating `envconfig.yaml` at runtime.
- `command_exec_input_listener` goroutine: 2 of 6 switch cases not yet covered by tests (`single_mode_move`, `pause_exec`).
- `plugin_loader.go:27` (plugin.Open error path) — requires a corrupt `.so` file, not testable in standard integration environment.
- `logger`, `hotspot`, `tunnel`, `systemupdate` — previously had zero tests; now covered (92%/100%/93%/61% respectively) after making the relevant `exec.Command` calls, hardcoded paths, and `http.Get`/`syscall.Statfs` calls injectable. `logger.Fatal()` and the tails of `PerformRollback`/`PerformSystemUpdate` past their guard clauses remain deliberately untested since they call `os.Exit()`.
- Race detection cannot run natively on the Pi's kernel (39-bit VMA; ThreadSanitizer requires 48-bit). Verified instead in an x86_64 sandbox with the real EtherCAT library built from source — 10/10 clean runs, but this can't be re-verified directly on deployment hardware.

---

## Fixed bugs

The following bugs were discovered through testing and fixed. Grouped
roughly by when they were found.

### Earlier round (June 2026)

- **`GetAbsolutePosition` broken below -360°** — fixed with double-mod formula.
- **`IntToBinary` broken for negative inputs** — fixed with `uint32(value)` cast.
- **`err_definition_parser` data race** — fixed with `sync.Mutex`.
- **`err_definition_parser` malformed-line panic** — fixed with `len(splitted) < 2` guard and injectable `errDefinitionPath` var for testing.
- **`GetEnvSettings` cache never invalidates** — fixed by adding `envSettingsLoaded` bool flag and `ResetEnvSettings()` function.
- **Mock PLC goroutine panic** — `startMockPLC` goroutine logged after test completion. Fixed with `sync.WaitGroup`.
- **`rpm-const: 120000.006`** — float arithmetic artifact. Fixed to exactly `120000`.
- **+188.365° position offset in motion tests** — fixed by reading from socket.io instead of SDO 0x6064.
- **0-byte binary / Err 80 crash loop** — fixed by `make deploy` validating non-empty files.

### Race-detection round

Found via systematic `-race` testing in an x86_64 sandbox (84 races down to
0, verified with 10 consecutive clean runs):

- **`resetSystemWorker` had no real stop mechanism** — sending `false` on its
  control channel was silently ignored; the goroutine ran forever once
  started. Fixed with proper `if !msg { return }` handling.
- **Reset sequence's restart order was backwards** — pollers (which
  immediately start sending on shared channels) were started *before* the
  listeners that reassign those channels, creating a real race window on
  every reset. Reordered so listeners come up first.
- **Every `stopXxx()` function was fire-and-forget** — no confirmation the
  target goroutine actually exited before proceeding. Fixed with
  `sync.WaitGroup`s and bounded (2s) waits, degrading to a logged warning
  rather than blocking forever if a goroutine is genuinely stuck.
- **`MTSdoReady = dev.MTSdoReady` self-assignment** in `SetupPDO` — a
  no-op line, removed.

### Coverage-reporting round

- **`-coverpkg` only ever named a curated ~27-package subset** — 12 packages
  with zero tests never factored into the reported percentage at all,
  inflating it. Fixed: every phase now uses `-coverpkg=./...`.
- **`ethercatdevicedatatypes` and `serialtest` had real, passing tests that
  were never actually invoked** by any phase — only affected by coverage
  instrumentation, never executed, because they weren't in any phase's
  package argument list. Fixed by adding them to `UNIT_PACKAGES`.
- **All 18 `commands/*` packages had an identical gap**: `CreateHandler()`,
  the actual plugin-loader entry point, was never called by any test —
  every test constructed `CommandHandler{}` directly instead. Fixed with
  one new test per package.

### Nil-channel hang round

Found via a real hardware hang during cold-boot testing, then found to be
systemic:

- **`currentDriverPosition` in `driver_status_keeper.go`** sent
  unconditionally on `BroadCastDriveStatusChannel` — fixed with a
  non-blocking `select`/`default` send.
- **5 of 6 methods in `ucam_driver_status_notifier.go`** had the identical
  unconditional-send pattern (`DestinationPosition`, `Alarm`, `DriverStatus`,
  `NotifyIOStatus`, `SocketMessage`) — only `CurrentPosition` had it done
  correctly. Fixed all 5.
- **All 5 functions in `channels/broadcast_status_ui.go`** had the same
  pattern (`SendAlarm`, `SendLineNumber`, `NotifyUIProgramCompleted`,
  `StepModeComplete`, `DestinationReached`) — this was the one that
  actually caused the real hardware hang (`DestinationReached()` blocking
  forever on a nil channel during the cold-boot test). Fixed all 5.

### Cold-boot lifecycle round

- **`moveToZero` had no "already at target" fast path** — unlike
  `moveMotorToDegree`, which already had one. Sending a redundant set-point
  identical to the current position gets no bit-12 acknowledgment from the
  drive, causing a real 2-second handshake timeout every time
  zero-reference was called while already at the reference position (a
  completely normal case, e.g. re-homing at startup). Fixed with a minimal,
  targeted fast path — deliberately *not* copying `moveMotorToDegree`'s
  full fast path, since that one is coupled to ECS synchronization logic
  `moveToZero` doesn't participate in.
- **The test itself needed `settings.LoadDriverSettings()`** before calling
  `InitMaster()`, matching `main.go`'s real startup order — without it,
  every drive setting (`JogFeed`, `HomingApos`, etc.) read as its zero
  value regardless of what was actually in `settings.json`, because
  `InitMaster()` alone never loads settings from disk; that's a separate
  step the real application does first.
- **`SaveHomingReference`'s in-memory cache update was silently failing**
  whenever *any other* field in the drive's settings blob had a type
  mismatch (confirmed on the real `settings.json`: `work_offset`, `g55`,
  `g57`, `g58`, `g56` are stored as bare integers, but their struct tags
  require quoted strings). The file write always succeeded and the function
  returned `nil`, but the in-memory `settingsRoot` cache silently kept the
  stale `HomingApos` value until the next full process restart. Fixed by
  updating only the one field that actually changed on the existing cached
  entry, instead of re-parsing the whole blob into a fresh struct.
- **`moveToZero`'s distance calculation reads a cached, asynchronously-updated
  position** (updated ~every 50ms by a background poller), separate from the
  live raw pulse count used for the actual pulse target. Calling zero-reference
  immediately after a jog risks reading a cache that hasn't caught up with
  where the jog actually left the drive. Not a code bug — mitigated in the
  cold-boot test with a 2-second settling pause between jog and zero-reference.
- **A file-mixup regression**: `commands/g90`'s test file was found
  containing tests named `TestG69_*`, causing spurious failures unrelated
  to any real logic bug — a copy/paste slip during file application, not a
  code defect. Caught and corrected.

### Zero-coverage infrastructure round

Closing out the four packages that previously had no tests at all:

- **`hotspot`/`wifi`, `tunnel`** — both called `exec.Command` directly
  against real `sudo`/`ngrok` scripts, with no way to test them without
  actually invoking those scripts. Fixed by making `exec.Command` an
  injectable package variable (the standard Go pattern for this), with
  tests using a `TestHelperProcess` subprocess instead of ever running the
  real script. `tunnel.go` additionally had `/mnt/app/jamun/ngrok.log`
  hardcoded inline — made injectable too, same as the exec calls.
- **`systemupdate`** — five real production paths (`rtcUpdateRoot`,
  `installPath`, `backupRoot`, `otaScriptPath`, `updateLockPath`,
  `rollbackScriptPath`) were `const`, not `var`, blocking any test from
  redirecting them safely. Converted to `var` (same values, no behavior
  change), along with making `http.Get` and `syscall.Statfs` injectable.
  `PerformRollback` and `PerformSystemUpdate` both call `os.Exit(2)` near
  the end (after launching their respective scripts) — deliberately left
  untested past that point, same accepted-gap category as
  `logger.Fatal()`. Everything reachable before that — every guard clause,
  `GetLatestBackup`, `CheckRollbackSuccess`, `Untar` (fully parameter-based,
  needed no changes at all), disk-space checking, staged-update validation,
  version comparison — is covered.
- **The new tests were invisible to the full suite at first** — exactly
  the same class of gap as `ethercatdevicedatatypes`/`serialtest`/
  `licensechecker` earlier: the tests passed perfectly in isolation, but
  `UNIT_PACKAGES` in `test-all-report.sh` was never updated to include
  `logger`, `hotspot`, `tunnel`, `systemupdate`, so the full suite silently
  never ran them, showing 0.0% coverage despite everything working. Fixed
  by adding all four to `UNIT_PACKAGES`.

---

## Why not `go test ./...`

Do not use `go test ./...` as the default validation command.

The `commands/` directory contains Go plugins (`package main`) with their own
`main`-equivalent entry points. Running `./...` from the repo root picks these
up and fails with multiple `main` declaration errors.

The Makefile test targets scope to the packages with stable tests. This is
intentional.

---

## Report generator

Each run generates three report formats:

```
test-reports/<timestamp>/summary.json   # machine-readable
test-reports/<timestamp>/report.html    # interactive HTML
test-reports/<timestamp>/report.md      # Markdown summary
```

`overall_status` in `summary.json` is `PASS` if and only if `failed == 0`.
Phase exit codes from skips do not affect it.

To open the latest merged coverage report:

```bash
go tool cover -html=test-reports/latest/coverage-merged.out
```