# Testing merge — TODO / known gaps

This file tracks test coverage that was intentionally **not** ported from
`test_environment13` into this build, and why — so the decision is visible
and reversible later, rather than silently lost.

Merge policy used throughout: **HAL's current logic/fixes are the source of
truth.** testenv's test files were layered on top; testenv's production-code
changes were only adopted where they were strictly additive/non-conflicting
(documented bugfixes, pure refactors-for-testability). Anywhere the two
sides had substantively different logic for the same behavior, HAL's version
was kept untouched and the corresponding tests were adapted or dropped.

## executors package — resume/stop logic (dropped)

`executors/command_executor.go` and `command_exec_input_listener.go` diverged
significantly from testenv's versions of the same files. Both sides added
real, independent fixes after forking:

**HAL has (kept):**
- Preserves an active fault alarm instead of overwriting it with "No Alarms"
  when `Initialize()` or `RunCodeFile()` runs (prevents hiding a real drive
  fault from the operator).
- Pre-creates `CommandExecInputChannel` in `init()` to close a startup race
  where an early drive fault could silently drop a stop command.

**testenv has (not adopted — would require redesigning HAL's execution
state machine):**
- `runMu` mutex preventing duplicate/concurrent `RunCodeFile` calls.
- A substantially more sophisticated resume system: `resolveStartLine`,
  `replaySetupCommands`, per-program tracking (`last_program.txt`), one-shot
  user-line selection (`userline.json` via `readUserLine`/`clearUserLine`),
  and atomic file writes (`writeFileAtomic`) for crash-safety.
- `blockWaitCompleted` tracking to correctly decide "resume at same line"
  vs "resume at next line" after a stop.
- A 60-second timeout in `waitForProgramFileUpdate` — HAL's version can
  block forever if an expected RS232 file update never arrives.

**What was ported:** only tests for functions verified byte-identical or
behaviorally identical on the paths exercised — `extractCommand`,
`createCommands`, `ParserCommand`, `SetRS232Enabled`/`IsRS232Enabled`,
`isAValidHandler`, `loadYmlConfig`, `notAPlugin`, and `CompileProgram`'s
file-error paths. See `executors/program_reader_test.go`,
`executors/program_reader_integration_test.go`, and
`executors/command_executor_safe_test.go`.

**What was dropped:** `executors_logic_test.go`, `executors_coverage_test.go`
(partially), `command_executor_integration_test.go`,
`resolve_start_line_integration_test.go`, `fake_program_e2e_test.go`, and
the resume-logic portions of `program_reader_unit_test.go`
(`writeFileAtomic`, `resolveStartLine`, `readUserLine`/`clearUserLine`,
`saveLastProgram`/`readLastProgram`, `ClearExecutionResumeState`,
`ResetExecutingProgram`, `UpdateLastLineFromJSON`, `ResumeExecution` —
these last two exist in HAL too, but with different implementations, so
testenv's tests don't apply as-is).

**If you want testenv's resume redesign later:** that's a deliberate product
decision (mutex + persistent multi-file resume state vs HAL's simpler
approach), not a drop-in — it should be reviewed and merged intentionally,
then the dropped test files can be reinstated.

## Packages not yet processed in this pass

`restapi`, `commands/*` (22 G-code command plugins), and the `motordriver`
package itself (cgo, requires the real IgH EtherCAT master library —
untestable in a sandbox without the hardware/library) still need the same
careful diff-and-merge treatment described above before their test suites
can be safely added.
