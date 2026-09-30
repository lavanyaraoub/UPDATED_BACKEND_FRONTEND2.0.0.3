# Hardware Motion Tests

Motion tests that physically move the Panasonic MDDLN45BE servo drive.

---

## Safety contract

These tests move the motor shaft. Before running:

1. **Shaft clearance** — ensure the motor shaft can rotate ±`MOTION_TEST_RANGE_DEG` degrees from its current position without hitting mechanical hard stops, tooling, or personnel.
2. **Application running** — `sudo systemctl start jamun` must be active. The application enforces hardware POT/NOT limit switches throughout.
3. **No fault** — the drive must show error code 0x0000. Clear any fault via the jamun UI Reset button before running.
4. **Never in unattended CI** — motion tests require `ALLOW_MOTION_TESTS=1` as an explicit opt-in. They must never run in automated pipelines without physical supervision.

---

## Running

### Recommended — via full test suite

```bash
MOTION_TEST_RANGE_DEG=3 MOTION_TIMEOUT_S=30 make test-coverage-full
```

This runs motion tests as Phase 4 of the full pipeline after all non-motion
phases have passed.

### Motion phase only

```bash
MOTION_TEST_RANGE_DEG=3 MOTION_TIMEOUT_S=30 \
bash scripts/test-all-report.sh hardware-ecs
```

### Run a single test

```bash
ALLOW_HARDWARE_TESTS=1 ALLOW_MOTION_TESTS=1 ALLOW_ECS_SIMULATION=1 \
go test -tags=hardware ./hardware/... -v -run TestMotion_AbsolutePositive
```

---

## Tests

| Test | Description |
|---|---|
| `TestMotion_AbsolutePositive` | Move to +delta°, assert arrival ±0.5° |
| `TestMotion_AbsoluteNegative` | Move to -delta° (wrapped to 360-delta°), assert arrival |
| `TestMotion_RelativeRoundTrip` | G91 +delta then -delta, assert return to start |
| `TestMotion_FeedrateChange` | Vary feedrate mid-program, assert arrival |
| `TestMotion_WorkOffset_G54` | Apply G54 work offset, assert offset position |
| `TestMotion_ErrorCodeZeroAfterMove` | Small move, assert 0x603F=0x0000 after |
| `TestMotionProgram_SampleFromSpec` | Execute full sample G-code programme |
| `TestMotionProgram_NegativePositions` | Programme with negative position commands |
| `TestMotionProgram_PositivePositions` | Programme with positive position commands |
| `TestMotionProgram_SequentialSameDirection` | Multiple moves same direction |
| `TestMotionProgram_DirectionReversals` | Programme alternating CW/CCW |
| `TestMotionProgram_LargeMoves` | Near-maximum range moves |
| `TestMotionProgram_FeedrateVariations` | Multiple F-code changes within programme |
| `TestMotionProgram_RelativeMode_MultiStep` | G91 multi-step programme |
| `TestMotionProgram_MixedAbsoluteRelative` | G90/G91 switching within programme |
| `TestMotionProgram_DriveHealthAfterAllMoves` | Assert error code 0x0000 after complete programme |
| `TestMotionProgram_Visible_*` (4) | Visual inspection tests — large visible moves |

---

## Environment variables

| Variable | Default | Description |
|---|---|---|
| `ALLOW_HARDWARE_TESTS` | — | Must be `1` to run any hardware test |
| `ALLOW_MOTION_TESTS` | — | Must be `1` to enable motor movement |
| `ALLOW_ECS_SIMULATION` | — | Must be `1` — jamun simulates the ECS finish signal internally |
| `MOTION_TEST_RANGE_DEG` | `5` | Max degrees to move per test. Use `3` for standard runs |
| `MOTION_TIMEOUT_S` | `20` | Seconds to wait for move completion before failing |
| `MOTION_REST_BASE_URL` | `http://localhost:5000` | Jamun REST API base URL |
| `MOTION_SOCKET_URL` | `http://localhost:9090` | Jamun socket.io URL for position readback |
| `ETHERCAT_CLI` | `ethercat` | Path to the IgH EtherCAT CLI |

---

## Position readback

Position is read from jamun's socket.io `destination_position` event on port 9090
— **not** from SDO 0x6064. This is because SDO 0x6064 returns stale data during
PDO operation (the encoder register is only updated when the PDO cyclic task
ticks, which can lag the actual position). The socket.io event reflects the
PDO buffer value directly and is always current.

Test position tolerance: **±0.5°**

---

## What the tests exercise

- Full application stack: REST API → executor → `DriverActionChannel` → `moveMotorToDegree()` → PDO position mode
- CiA-402 Profile Position mode (mode 0x01, confirmed by 0x6061 = 0x01)
- ECS finish signal handshake (simulated internally via `ALLOW_ECS_SIMULATION=1`)
- Position tolerance gate (0.02° internal jam threshold, 0.5° test assertion)
- Drive health after motion (error code 0x603F = 0x0000 required after each test)
- Work offset application (G54 in settings applied to absolute position)
- Feedrate enforcement (F-code changes validated by motion timing)

---

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| Tests hang indefinitely | `ALLOW_ECS_SIMULATION` not set | Add `ALLOW_ECS_SIMULATION=1` |
| Position assertion fails ±0.5° | Drive not homed or wrong work offset | Run zero-reference; verify G54=0 in settings |
| Drive fault mid-test | POT/NOT limit hit | Check `MOTION_TEST_RANGE_DEG` is within safe zone |
| `ethercat master` not in Operation | jamun not running | `sudo systemctl start jamun` |
| Error code non-zero before test | Latched fault | Clear via jamun UI Reset button |