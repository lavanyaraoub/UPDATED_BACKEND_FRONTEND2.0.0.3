# Test runner and reports

Use one command to run a complete test profile and generate a report.

## Commands

### Full suite — all 5 phases (recommended before deployment)

```bash
MOTION_TEST_RANGE_DEG=3 MOTION_TIMEOUT_S=30 make test-coverage-full
```

Runs unit → integration → hardware-readonly → hardware-ecs → e2e, merges
coverage, and generates a timestamped report under `test-reports/`.

### Safe tests only — no hardware or app needed

```bash
bash scripts/test-all-report.sh safe
```

### Read-only hardware checks — no motion

```bash
bash scripts/test-all-report.sh hardware-readonly
```

### Hardware motion tests only

```bash
MOTION_TEST_RANGE_DEG=3 MOTION_TIMEOUT_S=30 bash scripts/test-all-report.sh hardware-motion
```

### E2E tests only (app must be running on port 5000)

```bash
make test-e2e
```

Or via the script:

```bash
bash scripts/test-all-report.sh e2e
```

---

## Report output

Each run creates a timestamped folder:

```text
test-reports/YYYYMMDD-HHMMSS/
```

Important files:

```text
report.html              Human-readable report with per-test details
report.md                Markdown summary
summary.json             Machine-readable summary (used by CI)
coverage-merged.out      All phases merged into one coverage profile
coverage-unit.out        Phase 1 coverage only
coverage-integration.out Phase 2 coverage only
coverage-hardware-*.out  Phase 3/4 coverage
raw/*.jsonl              Raw go test -json streams
raw/*.console.log        Console output per phase
raw/*.stderr.log         Stderr per phase
raw/phase-status.tsv     Exit code per phase
```

A convenience symlink is also created:

```text
test-reports/latest → test-reports/YYYYMMDD-HHMMSS
```

Open the latest merged coverage report:

```bash
go tool cover -html=test-reports/latest/coverage-merged.out
```

---

## Profiles

| Profile | What it runs | Motor? | App needed? |
|---|---|---|---|
| `safe` | unit + integration | No | No |
| `hardware-readonly` | Hardware smoke/SDO/REST/compile | No | Yes |
| `hardware-motion` | `TestMotion_*` and `TestMotionProgram_*` only | Yes | Yes |
| `hardware-ecs` | Motion tests WITH ECS simulation | Yes | Yes |
| `full` | All of the above in sequence | Yes | Yes |
| `e2e` | REST API lifecycle against live app | No | Yes |

---

## Make targets

```bash
make test-unit           # Phase 1 only
make test-integration    # Phase 2 only
make test-e2e            # Phase 5 only (app must be running)
make test-coverage       # Phase 1 + 2 with coverage
make test-coverage-full  # All 5 phases (recommended)
```

---

## Coverage numbers

| Measured (go tool cover) | Real estimate |
|---|---|
| **46.5%** | **~65%** |

The gap between measured and real is explained by:
- **Cross-process motion** — `ManualJog`, `freeRotate`, `moveToZero` run inside the jamun process during 44 motion tests. Go's coverage tool only instruments the test binary.
- **CGo boundary** — `SDODownload`, `PDOSetup`, `init_master` CGo calls cannot be instrumented by Go's tooling.
- **gosocketio event handlers** — `clientcommunication/socketserver.go` Emit calls require a real WebSocket server.

---

## Why hardware tests are split internally

The runner executes hardware tests in two safe phases:

```text
hardware-readonly: ALLOW_HARDWARE_TESTS=1, ALLOW_MOTION_TESTS unset
hardware-ecs:      ALLOW_HARDWARE_TESTS=1, ALLOW_MOTION_TESTS=1, ECS simulation enabled
```

This avoids running read-only safety-contract tests while motion mode is enabled,
and ensures the motor only moves when explicitly opted in.