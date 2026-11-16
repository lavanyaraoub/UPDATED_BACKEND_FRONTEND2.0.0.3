#!/usr/bin/env python3
"""Generate HTML/Markdown/JSON reports from one or more `go test -json` streams.

The HTML report is a descriptive document: every test has a plain-English
explanation of *what* it checks and *how* it checks it, plus live pass/fail
status and timing injected from the actual go test JSON output.
"""
from __future__ import annotations

import argparse
import datetime as dt
import html
import json
import os
from pathlib import Path
from typing import Any, Dict, Iterable, List, Tuple

TERMINAL_TEST_ACTIONS = {"pass", "fail", "skip"}
TERMINAL_PACKAGE_ACTIONS = {"pass", "fail"}

# ── Human-readable descriptions for every test ───────────────────────────────
# Format: "package/TestName": ("What it checks", "How it checks it")
# "package" is the path segment after EtherCAT/ e.g. "motordriver", "commands/g01"
# A missing entry falls back to a plain rendering of the test name.
TEST_DESCRIPTIONS: Dict[str, Tuple[str, str]] = {

    # ── motordriver: CiA-402 state machine ───────────────────────────────────
    "motordriver/TestCia402NextControlword": (
        "Verifies the complete CiA-402 drive state machine truth table.",
        "Runs every defined branch (fault, startup sequence, high-bit masking, faultReset ignored during normal run, unknown state fallback) against expected controlword and opEnabled values from the Panasonic A6 manual §8.2.",
    ),
    "motordriver/TestCia402NextControlword_AlwaysWritesOpEnabled": (
        "Guarantees the opEnabled output pointer is always written, never left stale.",
        "Pre-sets opEnabled to the opposite of expected, then checks the function overwrites it correctly for a representative set of statuswords.",
    ),
    "motordriver/TestCia402NextControlword_AllInputsReturnValidCW": (
        "Exhaustive safety net: every possible 16-bit statusword must return a legal controlword and never panic.",
        "Loops all 65 536 uint16 values × 2 faultReset states, checks result is in {0x0000, 0x0006, 0x0007, 0x000F, 0x0080}. Takes ~5 ms.",
    ),

    # ── motordriver: PDO watchdog ─────────────────────────────────────────────
    "motordriver/TestPDOTickAge_ReturnsZeroBeforeFirstTick": (
        "Before the PDO loop starts, age must be 0, not garbage from an uninitialised atomic.",
        "Resets package atomics, calls PDOTickAge(), asserts == 0.",
    ),
    "motordriver/TestPDOTickAge_ReturnsRecentAgeAfterTick": (
        "After a simulated tick at time.Now(), age must be under 5 ms.",
        "Stores time.Now().UnixNano() into lastPDOTickNanos, calls PDOTickAge(), asserts 0 ≤ age < 5 ms.",
    ),
    "motordriver/TestPDOTickAge_DetectsStaleness": (
        "A tick recorded 100 ms ago must produce an age of ~100 ms.",
        "Stores time.Now().Add(-100ms).UnixNano(), asserts 90 ms ≤ age ≤ 110 ms.",
    ),
    "motordriver/TestPDOHealthy_QuietWhenPDONotActive": (
        "'Not active' is not 'unhealthy' — before the loop starts, PDOHealthy() must return (true, 0).",
        "pdoActive=false, asserts healthy==true and age==0.",
    ),
    "motordriver/TestPDOHealthy_HealthyWhenTicksFresh": (
        "With PDO active and a fresh tick, health must be true.",
        "Sets pdoActive=true and lastPDOTickNanos=now, asserts healthy==true.",
    ),
    "motordriver/TestPDOHealthy_UnhealthyWhenTicksStale": (
        "Ticks older than the 50 ms threshold must flip health to false.",
        "Sets lastPDOTickNanos to 100 ms ago, asserts healthy==false.",
    ),
    "motordriver/TestPDOHealthy_BoundaryAt50ms": (
        "Confirms the health threshold is exactly < 50 ms (exclusive).",
        "Tests 40 ms (healthy) and 60 ms (unhealthy) to verify the boundary direction.",
    ),

    # ── motordriver: PDO atomic masking ──────────────────────────────────────
    "motordriver/TestGetLastPDOStatusword_MasksHighBits": (
        "The statusword accessor must return only the low 16 bits of the 32-bit atomic storage.",
        "Stores 0xDEAD0637, asserts GetLastPDOStatusword() == 0x0637.",
    ),
    "motordriver/TestGetLastPDOErrorCode_MasksHighBits": (
        "The error code accessor must return only the low 16 bits.",
        "Stores 0x12341A82, asserts GetLastPDOErrorCode() == 0x1A82.",
    ),

    # ── motordriver: drive direction & jog ───────────────────────────────────
    "motordriver/TestReverseDir_PDOActiveReturnsNil": (
        "reverseDir must return nil (no error) when the PDO cyclic task is active.",
        "Activates PDO, calls reverseDir(stubDevice), asserts nil error.",
    ),
    "motordriver/TestNonReverseDir_PDOActiveReturnsNil": (
        "nonReverseDir must return nil when PDO is active.",
        "Activates PDO, calls nonReverseDir(stubDevice), asserts nil error.",
    ),
    "motordriver/TestStopJog_PDONotActiveReturnsError": (
        "StopJog must return an error containing 'PDO not active' when EtherCAT is not ready.",
        "Leaves pdoActive=false, calls StopJog(stubDevice), asserts non-nil error with expected message.",
    ),
    "motordriver/TestIsPDOActive_DefaultIsFalse": (
        "Package-level PDO active flag must default to false before any initialisation.",
        "Resets atomics, asserts IsPDOActive() == false.",
    ),
    "motordriver/TestIsPDOActive_TrueAfterActivation": (
        "After PDO is activated the flag must flip to true.",
        "Calls activatePDO, asserts IsPDOActive() == true.",
    ),

    # ── motordriver: angle-to-encoder conversion ──────────────────────────────
    "motordriver/TestGetPulsesFromDegree_BasicMultiplication": (
        "Converts degrees to encoder pulse counts using the drive gear ratio. Covers positive, negative, fractional, and different ratios.",
        "Table of (ratio, degree) → expected int64 pulse count; e.g. ratio=20000, 90° → 1 800 000.",
    ),
    "motordriver/TestGetPulsesFromDegree_IsLinear": (
        "Result must be linear: doubling the degree doubles the pulse count.",
        "For degrees 1–180 step 7, checks getPulsesFromDegree(2×deg) == 2×getPulsesFromDegree(deg).",
    ),
    "motordriver/TestGetPulsesFromDegree_AntisymmetricSign": (
        "Positive and negative degrees must produce equal-magnitude opposite-sign pulse counts.",
        "For degrees 0–360 step 10, checks result(deg) == −result(−deg).",
    ),
    "motordriver/TestCurrentPosition_ZeroEncoderAtZeroDegrees": (
        "Encoder count 0 must report position 0°.",
        "currentPosition(0, 20000, 'A') → 0.0.",
    ),
    "motordriver/TestCurrentPosition_QuarterTurn": (
        "1 800 000 pulses at ratio 20000 must report 90°.",
        "currentPosition(1_800_000, 20000, 'A') → ~90.",
    ),
    "motordriver/TestCurrentPosition_HalfTurn": (
        "3 600 000 pulses must report 180°.",
        "currentPosition(3_600_000, 20000, 'A') → ~180.",
    ),
    "motordriver/TestCurrentPosition_FullTurnWrapsToZero": (
        "A full revolution (7 200 000 pulses) must wrap to 0°, not 360°.",
        "currentPosition(7_200_000, 20000, 'A') → ~0.",
    ),
    "motordriver/TestCurrentPosition_NegativeEncoderNormalisedToPositive": (
        "Negative encoder counts (reverse direction) must normalise to the equivalent positive angle.",
        "−1 800 000 pulses → 270° (not −90°).",
    ),
    "motordriver/TestCurrentPosition_AlwaysInRange": (
        "For any encoder count, the returned position must be in [0°, 360°). Range invariant.",
        "Sweeps −4 to +4 full turns in 500 000-pulse steps, asserts 0 ≤ pos < 360.",
    ),
    "motordriver/TestCurrentPosition_AposCorrectionApplied": (
        "A calibration offset stored in aposCorrection shifts the reported position.",
        "Sets correction = 200 000 pulses (10°), calls currentPosition(1_800_000 ...) → ~80 (= 90 − 10).",
    ),
    "motordriver/TestGetAbsolutePosition_DelegatesCorrectly": (
        "getAbsolutePosition must use shortest-path logic: 350°→10° moves +20°, not −340°.",
        "Table of (current, target) → (expectedToMove, expectedDest), includes wrap-around cases.",
    ),
    "motordriver/TestGetRelativePosition_UsesCurrentWhenPrevFarAway": (
        "If prev differs from current by > 1°, treat current as the starting point (first-move guard).",
        "prev=0, current=90, target=+10 → move=10, dest=100.",
    ),
    "motordriver/TestGetRelativePosition_UsesPrevWhenClose": (
        "If prev is within 1° of current, use prev as the starting point.",
        "prev=90.5, current=90, target=+10 → dest=100.5.",
    ),
    "motordriver/TestGetRelativePosition_MinusOneSentinelFallsBackToCurrent": (
        "The sentinel value −1 (meaning 'no previous') always triggers the first-move guard.",
        "prev=−1, current=45, target=+15 → move=15, dest=60.",
    ),
    "motordriver/TestGetRelativePosition_WrapAroundStaysInRange": (
        "Relative moves that cross 360° must wrap to [0°, 360°).",
        "current=prev=350, target=+20 → dest=10.",
    ),
    "motordriver/TestGetPitchError_ZeroTargetReturnsZero": (
        "A target of 0° is outside all 36 pitch-error buckets (index would be −1); must return 0.",
        "getPitchError('A', 0) → 0.",
    ),
    "motordriver/TestGetPitchError_FirstInterval": (
        "10° maps to pitch-error index 0 (first bucket).",
        "Sets PitchError[0]=0.005, getPitchError('A', 10) → 0.005.",
    ),
    "motordriver/TestGetPitchError_LastInterval": (
        "360° maps to pitch-error index 35 (last bucket).",
        "Sets PitchError[35]=0.009, getPitchError('A', 360) → 0.009.",
    ),
    "motordriver/TestGetPitchError_BeyondLastIntervalReturnsZero": (
        "A target beyond 360° (index > 35) must return 0, not panic or read out of bounds.",
        "getPitchError('A', 370) → 0.",
    ),
    "motordriver/TestGetPitchError_NegativeTargetUsesAbsoluteValue": (
        "Negative targets use their absolute value for the bucket lookup.",
        "getPitchError('A', −90) == getPitchError('A', 90).",
    ),
    "motordriver/TestGetPitchError_AllIntervalsReachable": (
        "Every one of the 36 pitch-error buckets can be reached by the correct target angle.",
        "Loads distinct values into all 36 slots, calls getPitchError for target 10°, 20°, …, 360°, asserts each returns its slot value.",
    ),

    # ── motordriver: power & reset helpers ────────────────────────────────────
    "motordriver/TestPowerOn_PDOActiveReturnsNil": (
        "PowerOn must return nil when PDO is active (CiA-402 state machine handles enable, SDO not needed).",
        "Activates PDO, calls PowerOn(stubDevice), asserts nil.",
    ),
    "motordriver/TestPowerOff_PDOActiveReturnsNil": (
        "PowerOff must return nil and stop motion via PDO atomics.",
        "Activates PDO, calls PowerOff(stubDevice), asserts nil.",
    ),
    "motordriver/TestFastPowerOn_PDOActiveReturnsNil": (
        "FastPowerOn clears the shutdown flag via PDO. Must return nil when PDO is active.",
        "Activates PDO, calls FastPowerOn(stubDevice), asserts nil.",
    ),
    "motordriver/TestFastPowerOff_PDOActiveReturnsNil": (
        "FastPowerOff triggers a PDO-cycle shutdown. Must return nil when PDO is active.",
        "Activates PDO, calls FastPowerOff(stubDevice), asserts nil.",
    ),
    "motordriver/TestEmergency_PDOActiveEmptyMasterDevicesReturnsNil": (
        "Emergency stop with an empty device list and PDO active must return nil (nothing to stop).",
        "Activates PDO, calls emergency(stubDevice) with empty master devices, asserts nil.",
    ),
    "motordriver/TestResetDriver_PDOActiveReturnsNil": (
        "ResetDriver delegates to PDOFaultReset when PDO is active. Must return nil.",
        "Activates PDO, calls ResetDriver([], ...), asserts nil.",
    ),
    "motordriver/TestResetDriver_PDOActiveNonEmptySliceReturnsNil": (
        "ResetDriver with a real device slice and active PDO must return nil.",
        "Activates PDO, calls ResetDriver([stubDevice], ...), asserts nil.",
    ),
    "motordriver/TestResetMultiTurn_PDOActiveIsNoOp": (
        "ResetMultiTurn is a no-op when PDO is active (multi-turn reset happens at startup only).",
        "Activates PDO, calls ResetMultiTurn, asserts no error.",
    ),
    "motordriver/TestPowerOffAll_EmptySliceReturnsNil": (
        "PowerOffAll with an empty slice must return nil immediately.",
        "Calls PowerOffAll([]), asserts nil.",
    ),
    "motordriver/TestPdoStopMotion_EmptyMasterDevicesReturnsNil": (
        "PdoStopMotion with no devices configured must return nil.",
        "Calls PdoStopMotion([], ...), asserts nil.",
    ),
    "motordriver/TestSendECSFinSignal_ZeroFinishSignalReturnsNil": (
        "When fin_signal is 0 (disabled), SendECSFinSignal must return nil immediately.",
        "Calls SendECSFinSignal with finSignal=0, asserts nil.",
    ),
    "motordriver/TestPollIOStat_WithMockDriverDoesNotPanic": (
        "PollIOStat must not panic when called with a mock driver.",
        "Calls PollIOStat with a stub, defers recover(), fails test if panic occurs.",
    ),
    "motordriver/TestStopPollIOStat_WithMockDriverDoesNotPanic": (
        "StopPollIOStat must not panic when called with a mock driver.",
        "Calls StopPollIOStat with a stub, defers recover().",
    ),
    "motordriver/TestStopECSCheck_DoesNotPanic": (
        "StopECSCheck must not panic regardless of state.",
        "Calls StopECSCheck(), defers recover().",
    ),

    # ── commands/moveRotary ────────────────────────────────────────────────────
    "commands/moveRotary/TestCheckPotNotLimit_AbsoluteMove_WithinLimits": (
        "A move to 90° with ±180° limits must succeed and set DestinationPosition=90.",
        "checkPotNotLimit(ctx, 90, 'A') → nil; ctx.DriveSettings['A'].DestinationPosition == 90.",
    ),
    "commands/moveRotary/TestCheckPotNotLimit_AbsoluteMove_ExactlyAtPOTLimit_Blocked": (
        "Moving exactly to the POT limit (180°) must be rejected — the limit is exclusive.",
        "checkPotNotLimit(ctx, 180, 'A') → non-nil error 'POT Limit exceeded'.",
    ),
    "commands/moveRotary/TestCheckPotNotLimit_AbsoluteMove_BelowPOTLimit_Allowed": (
        "179° is strictly inside ±180° and must be allowed.",
        "checkPotNotLimit(ctx, 179, 'A') → nil.",
    ),
    "commands/moveRotary/TestCheckPotNotLimit_DisabledLimits_AlwaysAllowed": (
        "When both POT and NOT limits are 0 (infinite-rotation mode), any position is allowed.",
        "Sets both limits to 0, calls checkPotNotLimit(ctx, 99999, 'A') → nil.",
    ),
    "commands/moveRotary/TestCheckPotNotLimit_RelativeMode_AccumulatesDestination": (
        "In REL mode each move adds to the previous destination and the result must be in [0, 360).",
        "DestinationPosition=50, REL +30 → new destination in valid range.",
    ),
    "commands/moveRotary/TestCheckPotNotLimit_OnlyTargetedDriveIsUpdated": (
        "Moving drive A must not alter drive B's DestinationPosition.",
        "After checkPotNotLimit(ctx, 90, 'A'), ctx.DriveSettings['B'].DestinationPosition unchanged.",
    ),
    "commands/moveRotary/TestHandle_TrialMode_DoesNotPanic_AndAdvancesLine": (
        "The command handler must advance NextCmdLineToExec even in trial mode (no motion).",
        "currentLine=2, Handle('A90') → NextCmdLineToExec==3.",
    ),
    "commands/moveRotary/TestHandle_TrialMode_POTLimitSetsErr": (
        "A command breaching the POT limit must set ctx.Err during trial validation.",
        "Handle('A180') with ±180° limit → ctx.Err != nil.",
    ),
    "commands/moveRotary/TestHandle_TrialMode_NegativePosition": (
        "Trial mode with a negative position must return 0 results and still advance the line.",
        "Handle('A-90') → len(results)==0, NextCmdLineToExec==1.",
    ),
    "commands/moveRotary/TestHandle_TrialMode_DriveB": (
        "Drive B with a target within its ±360° limit must succeed and advance the line.",
        "Handle('B45') → ctx.Err==nil, NextCmdLineToExec advances.",
    ),
    "commands/moveRotary/TestHandle_TrialMode_WorkOffsetApplied": (
        "A work offset shifts the commanded position: A90 + 10° offset = 100°, within ±180°.",
        "Sets G53 offset=10°, Handle('A90') → ctx.Err==nil.",
    ),
    "commands/moveRotary/TestHandle_TrialMode_WorkOffsetCanCauseLimitBreach": (
        "An offset can push an otherwise-valid command over the limit.",
        "Sets G53 offset=50°, Handle('A150') → 200° > 180° POT limit → ctx.Err!=nil.",
    ),
    "commands/moveRotary/TestMoveRotarySafetyIntegration_AbsoluteMoveUpdatesOnlySelectedDrive": (
        "ABS move to 90° on drive A sets A.DestinationPosition=90 and leaves B unchanged.",
        "checkPotNotLimit(ctx, 90, 'A'); A.Dest==90, B.Dest==0.",
    ),
    "commands/moveRotary/TestMoveRotarySafetyIntegration_RelativeMoveUsesExistingDestination": (
        "REL mode accumulates: starting at 40°, +15° → destination 55°.",
        "Sets A.DestinationPosition=40, REL +15 → A.DestinationPosition==55.",
    ),
    "commands/moveRotary/TestMoveRotarySafetyIntegration_BlocksPOTLimit": (
        "Moving exactly to the POT limit must return error 'POT Limit exceeded'.",
        "checkPotNotLimit(ctx, 100, 'A') with POTLimit=100 → 'POT Limit exceeded'.",
    ),
    "commands/moveRotary/TestMoveRotarySafetyIntegration_NOTLimitCurrentBehavior": (
        "PINNED KNOWN DEFECT: NOT-limit check is not implemented. A REL move breaching NOTLimit is not blocked; instead it wraps. This test locks in the broken behaviour so a future fix is visible.",
        "REL −20° from −90° with NOTLimit=−100 → no error returned, destination wraps to 250°.",
    ),
    "commands/moveRotary/TestMoveRotarySafetyIntegration_DisabledLimitsDoNotBlock": (
        "When limits are 0, any position must be allowed.",
        "Sets POT=NOT=0, checkPotNotLimit(ctx, 100000, 'A') → nil.",
    ),

    # ── commands/divide360 ────────────────────────────────────────────────────
    "commands/divide360/TestGetDividedDegreeAndTimes_ValidDivisor": (
        "G16 P<n> divides 360° by n. Tests P4→90°, P10→36°, P6→60°, P360→1°, P-4→-90°.",
        "getDividedDegreeAndTimes('G16 P4', ctx) → degree=90.0, divisor=4.",
    ),
    "commands/divide360/TestGetDividedDegreeAndTimes_NoParams_ReturnsErr": (
        "'G16' with no P parameter must return an error — it is invalid syntax.",
        "getDividedDegreeAndTimes('G16', ctx) → non-nil error.",
    ),
    "commands/divide360/TestGetDividedDegreeAndTimes_NonNumericParam_ReturnsErr": (
        "'G16 Pabc' with a non-numeric value must return an error.",
        "getDividedDegreeAndTimes('G16 Pabc', ctx) → non-nil error.",
    ),
    "commands/divide360/TestG16_RequiresRelativeMode": (
        "G16 only makes sense in REL mode; ABS mode must set ctx.Err.",
        "ctx.RunMode='ABS', Handle('G16 P4') → ctx.Err != nil.",
    ),
    "commands/divide360/TestG16_TrialModeAdvancesLine": (
        "Trial mode must advance NextCmdLineToExec.",
        "currentLine=3, Handle('G16 P4') → NextCmdLineToExec==4.",
    ),
    "commands/divide360/TestG16_TrialModeNoResults": (
        "Trial mode must return 0 execution results (no motion output).",
        "Handle('G16 P4') → len(results)==0.",
    ),
    "commands/divide360/TestG16_CommandName": (
        "CommandName() must return 'g16'.",
        "CommandHandler{}.CommandName() == 'g16'.",
    ),

    # ── commands/g01 ─────────────────────────────────────────────────────────
    "commands/g01/TestG01_NoParams_SetsErr": (
        "G01 without a feed rate must set ctx.Err with message 'Feed rate not specified'.",
        "Handle('G01') → ctx.Err contains 'Feed rate not specified'.",
    ),
    "commands/g01/TestG01_WithFeedParam_ReturnsChildResult": (
        "G01 spawns sub-commands as child results rather than executing them directly.",
        "Handle('G01 F100') → result with ShouldExecute=true, Cmd='F100'.",
    ),
    "commands/g01/TestG01_MultipleParams_AllSpawnedAsChildren": (
        "'G01 F20 A90' must spawn both F20 and A90 as separate child results.",
        "Handle('G01 F20 A90') → both 'F20' and 'A90' appear in results with ShouldExecute=true.",
    ),
    "commands/g01/TestG01_AdvancesLine": (
        "NextCmdLineToExec must increment by 1.",
        "currentLine=1, Handle('G01 F20') → NextCmdLineToExec==2.",
    ),
    "commands/g01/TestG01_CommandName": (
        "CommandName() must return 'g01'.",
        "CommandHandler{}.CommandName() == 'g01'.",
    ),

    # ── commands/g90 / g91 ───────────────────────────────────────────────────
    "commands/g90/TestG90_SetsAbsoluteMode": (
        "G90 must set ctx.RunMode to 'ABS'.",
        "Handle('G90') → ctx.RunMode == 'ABS'.",
    ),
    "commands/g90/TestG90_AdvancesLine": ("Advances NextCmdLineToExec by 1.", "currentLine=N, Handle('G90') → NextCmdLineToExec==N+1."),
    "commands/g90/TestG90_TrialModeReturnsNoResults": ("Trial mode returns 0 results.", "Handle('G90') → len(results)==0."),
    "commands/g90/TestG90_CommandName": ("CommandName() == 'g90'.", "CommandHandler{}.CommandName() == 'g90'."),
    "commands/g91/TestG91_SetsRelativeMode": (
        "G91 must set ctx.RunMode to 'REL'.",
        "Handle('G91') → ctx.RunMode == 'REL'.",
    ),
    "commands/g91/TestG91_AdvancesLine": ("Advances NextCmdLineToExec by 1.", "currentLine=N, Handle('G91') → NextCmdLineToExec==N+1."),
    "commands/g91/TestG91_TrialModeReturnsNoResults": ("Trial mode returns 0 results.", "Handle('G91') → len(results)==0."),
    "commands/g91/TestG91_CommandName": ("CommandName() == 'g91'.", "CommandHandler{}.CommandName() == 'g91'."),

    # ── commands/g68 / g69 ───────────────────────────────────────────────────
    "commands/g68/TestG68_DoesNotSetErr": ("G68 (coordinate rotation enable) must not set ctx.Err.", "Handle('G68') → ctx.Err==nil."),
    "commands/g68/TestG68_AdvancesLine": ("Advances NextCmdLineToExec.", "currentLine=N → N+1."),
    "commands/g68/TestG68_TrialModeReturnsNoResults": ("Trial mode returns 0 results.", "len(results)==0."),
    "commands/g68/TestG68_CommandName": ("CommandName() == 'g68'.", "CommandHandler{}.CommandName() == 'g68'."),
    "commands/g69/TestG69_DoesNotSetErr": ("G69 (coordinate rotation cancel) must not set ctx.Err.", "Handle('G69') → ctx.Err==nil."),
    "commands/g69/TestG69_AdvancesLine": ("Advances NextCmdLineToExec.", "currentLine=N → N+1."),
    "commands/g69/TestG69_TrialModeReturnsNoResults": ("Trial mode returns 0 results.", "len(results)==0."),
    "commands/g69/TestG69_CommandName": ("CommandName() == 'g69'.", "CommandHandler{}.CommandName() == 'g69'."),

    # ── commands/m30 / m99 ────────────────────────────────────────────────────
    "commands/m30/TestM30_EndsExecution": ("M30 must signal end-of-program to the executor.", "Handle('M30') sets ctx.NextCmdLineToExec to the end sentinel."),
    "commands/m30/TestM30_ClearsErr": ("M30 must clear any existing ctx.Err before ending.", "Sets ctx.Err, Handle('M30') → ctx.Err==nil."),
    "commands/m30/TestM30_TrialModeReturnsNoResults": ("Trial mode returns 0 results.", "len(results)==0."),
    "commands/m30/TestM30_CommandName": ("CommandName() == 'm30'.", "CommandHandler{}.CommandName() == 'm30'."),
    "commands/m99/TestM99_TrialModeEndsExecution": ("M99 ends execution in trial mode.", "Handle('M99') → ctx signals end."),
    "commands/m99/TestM99_TrialModeReturnsNoResults": ("Trial mode returns 0 results.", "len(results)==0."),
    "commands/m99/TestM99_CommandName": ("CommandName() == 'm99'.", "CommandHandler{}.CommandName() == 'm99'."),

    # ── commands/loopStart / loopEnd ─────────────────────────────────────────
    "commands/loopStart/TestLoopStart_SetsLoopCountMinusOne": (
        "loopStart stores the loop count minus one into ctx so loopEnd can decrement to zero.",
        "Handle('LOOP3') → ctx.LoopCount == 2.",
    ),
    "commands/loopStart/TestLoopStart_RecordsWhereLoopStarted": (
        "loopStart records the current line number so loopEnd can jump back.",
        "Handle at line 5 → ctx.LoopStartLine == 5.",
    ),
    "commands/loopStart/TestLoopStart_InvalidValueSetsErr": (
        "A non-numeric loop count must set ctx.Err.",
        "Handle('LOOPabc') → ctx.Err != nil.",
    ),
    "commands/loopStart/TestLoopStart_AdvancesLine": ("Advances NextCmdLineToExec.", "currentLine=N → N+1."),
    "commands/loopStart/TestLoopStart_TrialModeReturnsNoResults": ("Trial mode returns 0 results.", "len(results)==0."),
    "commands/loopStart/TestLoopStart_CommandName": ("CommandName() == 'loopStart'.", "CommandHandler{}.CommandName() == 'loopStart'."),
    "commands/loopEnd/TestLoopEnd_DecrementsAndJumpsBackWhenCountRemaining": (
        "When count > 0, loopEnd decrements the count and sets NextCmdLineToExec back to LoopStartLine.",
        "ctx.LoopCount=2, Handle → LoopCount==1, NextCmdLineToExec==LoopStartLine.",
    ),
    "commands/loopEnd/TestLoopEnd_AdvancesLineWhenCountExhausted": (
        "When count reaches 0, loopEnd advances the line normally (exits the loop).",
        "ctx.LoopCount=0, Handle → NextCmdLineToExec advances past loopEnd.",
    ),
    "commands/loopEnd/TestLoopEnd_TrialModeJustAdvances": ("Trial mode just advances the line.", "currentLine=N → N+1."),
    "commands/loopEnd/TestLoopEnd_CommandName": ("CommandName() == 'loopEnd'.", "CommandHandler{}.CommandName() == 'loopEnd'."),

    # ── commands/invalidCommand ───────────────────────────────────────────────
    "commands/invalidCommand/TestInvalidCommand_SetsErrWithCommandName": (
        "An unknown command must set ctx.Err containing the command name.",
        "Handle('BADCMD') → ctx.Err message contains 'BADCMD'.",
    ),
    "commands/invalidCommand/TestInvalidCommand_AdvancesLine": ("Advances NextCmdLineToExec so executor does not loop.", "currentLine=N → N+1."),
    "commands/invalidCommand/TestInvalidCommand_CommandName": ("CommandName() == 'invalidCommand'.", "CommandHandler{}.CommandName() == 'invalidCommand'."),

    # ── commands/workoffset ───────────────────────────────────────────────────
    "commands/workoffset/TestWorkoffset_SetsCurrentWorkOffset": (
        "Activating a work offset must set ctx.CurrentWorkOffSet to the correct code.",
        "Handle('G54') → ctx.CurrentWorkOffSet == 'G54'.",
    ),
    "commands/workoffset/TestWorkoffset_AllOffsetCodes": (
        "All six work offset codes (G53–G58) must be accepted and applied.",
        "Iterates G53, G54, G55, G56, G57, G58; each Handle() sets the correct code.",
    ),
    "commands/workoffset/TestWorkoffset_WithInlineAxisCommand_ReturnsChildResult": (
        "A workoffset command paired with an axis move spawns the axis as a child result.",
        "Handle('G54 A90') → child result with Cmd='A90' and ShouldExecute=true.",
    ),
    "commands/workoffset/TestWorkoffset_AdvancesLine": ("Advances NextCmdLineToExec.", "currentLine=N → N+1."),
    "commands/workoffset/TestWorkoffset_CommandName": ("CommandName() == 'workoffset'.", "CommandHandler{}.CommandName() == 'workoffset'."),

    # ── configparser ──────────────────────────────────────────────────────────
    "configparser/TestParseExecutionConfigFromReader_ValidYAML": (
        "A realistic 3-command YAML config parses into correct Command structs.",
        "Checks Cmd, Func, ConsiderInBlockExecution, DriveID for G01, A**, and pipe-separated workoffset.",
    ),
    "configparser/TestParseExecutionConfigFromReader_DriveIDDefaultsZero": (
        "Commands without a driveId field must default to DriveID=0.",
        "G01 parsed → DriveID==0.",
    ),
    "configparser/TestParseExecutionConfigFromReader_BCommandDriveID": (
        "B** with driveId:1 must parse to DriveID=1 (multi-drive support readiness).",
        "Parse YAML with B** driveId:1 → DriveID==1.",
    ),
    "configparser/TestParseExecutionConfigFromReader_EmptyInput": (
        "Empty YAML must produce 0 commands without error.",
        "ParseExecutionConfigFromReader('') → len(commands)==0, err==nil.",
    ),
    "configparser/TestParseExecutionConfigFromReader_EmptyCommandArray": (
        "An explicit 'command: []' must produce 0 commands without error.",
        "Parse YAML with command:[] → len==0.",
    ),
    "configparser/TestParseExecutionConfigFromReader_MalformedYAML": (
        "Invalid YAML syntax must return an error, not a silently empty config.",
        "Parse 'this is { not valid yaml [' → non-nil error.",
    ),
    "configparser/TestParseExecutionConfigFromReader_GetCommandWorksOnParsedConfig": (
        "After parsing, GetCommand must correctly resolve exact matches, wildcards, and pipe-separated multi-commands.",
        "GetCommand('G01')→g01, GetCommand('A90')→moveRotaryDegree (A** wildcard), GetCommand('G55')→workoffset.",
    ),
    "configparser/TestParseDeviceConfigFromReader_ValidYAML": (
        "A realistic device-config YAML with drive parameters parses correctly.",
        "Checks DriveXRatio, POTLimit, NOTLimit, PitchError, WorkOffsets for at least one device.",
    ),
    "configparser/TestParseDeviceConfigFromReader_EmptyInput": ("Empty YAML → 0 devices, nil error.", "ParseDeviceConfigFromReader('') → empty, nil."),
    "configparser/TestParseDeviceConfigFromReader_EmptyDevicesArray": ("'devices: []' → 0 devices, nil error.", "Parse empty array."),
    "configparser/TestParseDeviceConfigFromReader_MalformedYAML": ("Invalid YAML → non-nil error.", "Parse bad YAML."),
    "configparser/TestParseDeviceConfigFromReader_MissingOptionalFields": ("Optional fields absent → sensible zero defaults.", "Parse minimal device entry."),
    "configparser/TestParseDeviceConfigFromReader_MultipleDevices": ("Two device entries both parse correctly.", "Parse two devices, check names and ratios."),
    "configparser/TestParseDeviceConfigFromReader_NoDevicesArray": ("YAML without a devices key → 0 devices, nil.", "Parse YAML missing devices key."),
    "configparser/TestParseDeviceConfigFromReader_RPMConstFloatValueBehavior": ("RPMConst stored as Float64Str parses correctly.", "Check Float64Str round-trip."),
    "configparser/TestParseDeviceConfigFromReader_TypeMismatch": ("Type mismatch in YAML returns error.", "Parse YAML with integer where string expected."),
    "configparser/TestParseEthercatAddressConfigFromReader_ValidYAML": ("EtherCAT address config parses correctly.", "Checks operation name, steps, index/subindex fields."),
    "configparser/TestParseEthercatAddressConfigFromReader_EmptyInput": ("Empty YAML → 0 operations, nil.", "Parse empty."),
    "configparser/TestParseEthercatAddressConfigFromReader_EmptyOperationsArray": ("'operations: []' → 0 operations.", "Parse empty array."),
    "configparser/TestParseEthercatAddressConfigFromReader_MalformedYAML": ("Invalid YAML → error.", "Parse bad YAML."),
    "configparser/TestParseEthercatAddressConfigFromReader_GetOperationByName": ("GetOperation returns correct entry by name.", "Lookup existing name → match."),
    "configparser/TestParseEthercatAddressConfigFromReader_GetOperationNotFound": ("GetOperation returns empty for unknown name.", "Lookup missing name → zero value."),
    "configparser/TestParseEthercatAddressConfigFromReader_OperationWithNoSteps": ("Operation with empty steps list parses without error.", "Parse operation with steps:[]."),
    "configparser/TestParseEthercatAddressConfigFromReader_StepGetValueWorks": ("Step.GetValue returns the correct typed value.", "Parse step with known value, call GetValue()."),
    "configparser/TestParseEthercatAddressConfigFromReader_ActionField": ("Action field on a step parses correctly.", "Check step.Action == expected string."),
    "configparser/TestConfigToExecutionIntegration_CommandDeviceAndEthercatConfigsAgree": (
        "Loading both YAML files produces a consistent ExecutionContext: all drives present, all handlers registered.",
        "Loads execution.yml and device-configuration.yml, checks drive names, command map, and limits agree.",
    ),
    "configparser/TestConfigToExecutionIntegration_InvalidCommandFallbackIsConfigured": (
        "The 'invalidCommand' fallback must be registered so unknown G-codes are handled gracefully.",
        "After loading config, checks funcMap contains 'invalidCommand' handler.",
    ),
    "configparser/TestParseDeviceConfig_HappyPathViaOpenAndDelegate": ("ParseDeviceConfig reads the real config file without error.", "Opens configs/device-configuration.yml, asserts nil error."),
    "configparser/TestParseDeviceConfig_MissingFileReturnsError": ("Missing config file returns error.", "Points to nonexistent path, asserts non-nil error."),
    "configparser/TestParseEthercatAddressConfig_HappyPathViaOpenAndDelegate": ("ParseEthercatAddressConfig reads the real file.", "Opens real YAML, asserts nil error."),
    "configparser/TestParseEthercatAddressConfig_MissingFileReturnsError": ("Missing file returns error.", "Non-existent path → error."),
    "configparser/TestParseExececutionConfigYML_HappyPathViaOpenAndDelegate": ("ParseExececutionConfigYML reads real execution.yml.", "Opens real file, asserts nil error."),
    "configparser/TestParseExececutionConfigYML_MissingFileReturnsError": ("Missing execution.yml returns error.", "Non-existent path → error."),
    "configparser/TestGetErrorString_KnownCodeResolvesAfterManualInjection": ("Known error code resolves to human-readable string after injection.", "Injects code→string map, calls GetErrorString(code) → expected string."),
    "configparser/TestGetErrorString_ParsesRealFileFormatCorrectly": ("Error definition file format parses correctly.", "Loads sample file, verifies code-to-string mapping."),
    "configparser/TestGetErrorString_UnknownCodeReturnsUnknownMessage": ("Unknown error code returns 'unknown' message.", "GetErrorString(0xFFFF) → contains 'unknown'."),

    # ── executors ─────────────────────────────────────────────────────────────
    "executors/TestExtractCommand_SemicolonAtEnd": ("'G90;' extracts to 'G90'.", "extractCommand('G90;') → 'G90', nil."),
    "executors/TestExtractCommand_SemicolonWithInlineComment": ("'A90; comment' extracts to 'A90', discarding the comment.", "extractCommand('A90; move to 90') → 'A90'."),
    "executors/TestExtractCommand_NoSemicolonReturnsSyntaxError": ("Line without semicolon is a syntax error.", "extractCommand('G90') → '', error('Syntax error')."),
    "executors/TestExtractCommand_EmptyBeforeSemicolon": ("Bare ';' produces empty command string (caller skips it).", "extractCommand(';') → '', nil."),
    "executors/TestExtractCommand_MultipleSemicolonsUsesFirst": ("Only the first semicolon is the terminator.", "extractCommand('A90; a; b') → 'A90'."),
    "executors/TestExtractCommand_NegativeValue": ("Negative values parse correctly.", "extractCommand('A-90;') → 'A-90'."),
    "executors/TestExtractCommand_FloatValue": ("Float values parse correctly.", "extractCommand('A90.5;') → 'A90.5'."),
    "executors/TestExtractCommand_LeadingWhitespacePreserved": ("extractCommand does not trim — whitespace before command is kept (caller trims).", "extractCommand('  G90;') → '  G90'."),
    "executors/TestExtractCommand_MixedCasePreserved": ("extractCommand does not uppercase — caller does that.", "extractCommand('g90;') → 'g90'."),
    "executors/TestExtractCommand_NeverPanics": (
        "The function must never panic on any ASCII printable input including edge cases.",
        "Runs 11 adversarial inputs through a recover() deferred panic check.",
    ),
    "executors/TestParserCommandIntegration_StripsCommentsAndKeepsSourceLineNumbers": (
        "ParserCommand strips comment lines (#) and preserves source line numbers on remaining commands.",
        "Writes file with comments, asserts resulting Command structs have correct line numbers.",
    ),
    "executors/TestParserCommandIntegration_NormalizesCaseButPreservesLineNumber": ("ParserCommand uppercases commands.", "Writes 'g90;', asserts Cmd=='G90'."),
    "executors/TestParserCommandIntegration_RejectsLineWithoutSemicolon": ("A line without semicolon causes ParserCommand to return error.", "File with 'G90' (no semicolon) → error."),
    "executors/TestParserCommandIntegration_EmptyAndCommentOnlyPrograms": ("Empty file and comment-only file both parse to 0 commands.", "Three sub-cases: empty, comments-only, whitespace+comments."),
    "executors/TestCompileProgram_ValidProgramReturnsNil": ("A syntactically valid program passes trial validation.", "Writes valid .nc, CompileProgram → nil."),
    "executors/TestCompileProgram_SyntaxErrorReturnsError": ("A syntax error in the program returns a non-nil error.", "Writes line without semicolon → non-nil."),
    "executors/TestCompileProgram_MissingFileReturnsError": ("Missing file returns error.", "CompileProgram('/nonexistent') → error."),
    "executors/TestExecuteCommandsIntegration_RealisticAbsoluteRelativeMotionFlow": (
        "A realistic G90→A90→G91→A10→M30 sequence dispatches to correct handlers in order.",
        "Recording handlers log each call; asserts call order matches program order.",
    ),
    "executors/TestExecuteCommandsIntegration_StopsOnHandlerError": ("Executor stops on ctx.Err set by a handler.", "BAD command handler sets Err → subsequent commands not executed."),
    "executors/TestExecuteCommandsIntegration_UnknownCommandFailsFast": ("Unknown command (not in funcMap) causes fast failure.", "Command with no handler → executor returns error."),
    "executors/TestExecuteCommandsIntegration_HonoursExplicitJumpTarget": ("Explicit jump target (R-command) skips to correct line.", "R2 in program → executor jumps to line 2."),
    "executors/TestExecuteCommandsIntegration_JumpTargetOutsideProgramEndsCleanly": ("Jump beyond end of program terminates cleanly.", "R99 beyond last line → clean exit, no panic."),
    "executors/TestExecuteCommandsIntegration_SequentialNonMotionFlow": ("G90→G91→M30 runs all three in sequence.", "Recording handler logs; asserts ['G90','G91','M30']."),
    "executors/TestExecuteCommands_StopFlagExitsImmediately": ("Setting the stop flag causes the executor to exit immediately.", "Sets stopFlag=true before run → executor exits without executing commands."),
    "executors/TestExecuteCommandIntegration_ExecutesInlineParamHandlers": ("Inline parameter handlers (child commands) are dispatched correctly.", "PARENT command spawns child; asserts child handler called."),
    "executors/TestIsAValidHandler_ValidHandlerReturnsNil": ("A properly implemented handler passes validation.", "ValidHandler{} → isAValidHandler → nil."),
    "executors/TestIsAValidHandler_NilHandlerReturnsError": ("nil handler fails validation.", "nil → error."),
    "executors/TestIsAValidHandler_InvalidCommandNameReturnsError": ("Handler returning empty CommandName() fails validation.", "EmptyNameHandler{} → error."),
    "executors/TestResolveStartLine_FreshStartReturnsZero": ("Fresh start (no saved state) → line 0.", "resolveStartLine with no files → 0."),
    "executors/TestResolveStartLine_ResumeSameProgram": ("Resume same program → returns last saved line.", "SaveLastLine(3), resolveStartLine(program.nc) → 3."),
    "executors/TestResolveStartLine_ProgramChangedReturnsZero": ("Program changed → start from 0.", "SaveLastProgram('old'), resolve('new') → 0."),
    "executors/TestResolveStartLine_UserLineHasPriority": ("User-selected line takes priority over auto-resume.", "WriteUserLine(5), resolve → 4 (0-indexed)."),
    "executors/TestResolveStartLine_RS232AlwaysReturnsZero": ("RS-232 mode always starts from line 0.", "SetRS232Enabled(true), resolve → 0."),
    "executors/TestResolveStartLineIntegration_Rs232AlwaysStartsFresh": ("RS-232 integration: resolve returns 0 regardless of saved state.", "Save line 5, enable RS232, resolve → 0."),
    "executors/TestReplaySetupCommandsIntegration_ReplaysOnlySafeSetupCommands": (
        "Resume replay re-runs G90/G91/workoffset setup commands but not motion commands.",
        "Program with setup + motion; resume at line 4 → setup replayed, motion skipped.",
    ),
    "executors/TestResumeExecution_NonZeroResumesFromThatLine": ("ResumeExecution(n) starts executing from line n.", "ResumeExecution(1) → handlers for lines 1,2 called, line 0 skipped."),
    "executors/TestResumeExecution_ZeroNextLineIsNoOp": ("ResumeExecution(0) is a no-op.", "ResumeExecution(0) → nothing executed."),
    "executors/TestResumeExecution_AfterResumeNextLineWhenStoppedIsPreserved": ("After resume, the stopped-at line is preserved.", "Resume, stop mid-run → NextLineWhenStopped == stopped line."),
    "executors/TestReadLastLine_ValidContent": ("ReadLastLine returns the saved integer.", "Write '7', ReadLastLine → 7."),
    "executors/TestReadLastLine_MissingFileReturnsZero": ("Missing file → 0.", "No file → 0."),
    "executors/TestReadLastLine_EmptyFileReturnsZero": ("Empty file → 0.", "Empty file → 0."),
    "executors/TestReadLastProgram_ValidContent": ("ReadLastProgram returns the saved path.", "Write 'prog.nc', ReadLastProgram → 'prog.nc'."),
    "executors/TestReadLastProgram_MissingFileReturnsEmpty": ("Missing file → ''.", "No file → ''."),
    "executors/TestSaveLastLine_WritesReadableValue": ("SaveLastLine writes a file that ReadLastLine can read back.", "Save 5, ReadLastLine → 5."),
    "executors/TestSaveLastProgram_WritesReadableValue": ("SaveLastProgram round-trips correctly.", "Save 'p.nc', ReadLastProgram → 'p.nc'."),
    "executors/TestClearLastLine_ResetsLineAndProgram": ("ClearLastLine resets both last-line and last-program files.", "ClearLastLine() → ReadLastLine==0, ReadLastProgram==''."),
    "executors/TestReadUserLine_ValidJSON": ("ReadUserLine reads a JSON file {line:N} correctly.", "Write {line:5}, readUserLine → 5."),
    "executors/TestReadUserLine_MissingFileReturnsZero": ("Missing file → 0.", "No file → 0."),
    "executors/TestReadUserLine_BadJSONReturnsZero": ("Bad JSON → 0.", "Write 'not json', readUserLine → 0."),
    "executors/TestReadUserLine_ZeroValueReturnsZero": ("{line:0} → 0.", "Write {line:0} → 0."),
    "executors/TestClearUserLine_WritesZeroValue": ("ClearUserLine writes {line:0}.", "ClearUserLine(), readUserLine → 0."),
    "executors/TestUpdateLastLineFromJSON_ValidUserLine": ("UpdateLastLineFromJSON writes the user line.", "JSON {line:6} → SaveLastLine(5) called (0-indexed)."),
    "executors/TestUpdateLastLineFromJSON_NoUserLineIsNoOp": ("No valid user line → no-op.", "JSON {line:0} → no SaveLastLine call."),
    "executors/TestWriteFileAtomic_CreatesFileWithCorrectContent": ("writeFileAtomic creates a file with correct content.", "writeFileAtomic(path, 'hello') → file contains 'hello'."),
    "executors/TestWriteFileAtomic_OverwritesExistingFile": ("writeFileAtomic overwrites existing file atomically.", "Existing file, write new content → file has new content."),
    "executors/TestSetDriveSettings_MapsSettingsIntoExecContext": ("SetDriveSettings maps device config into ExecutionContext.DriveSettings.", "Calls SetDriveSettings, checks DriveSettings map populated."),
    "executors/TestLoadYmlConfig_CachesAfterFirstCall": ("LoadYmlConfig is cached — second call does not re-read disk.", "Call twice; second call returns same pointer."),
    "executors/TestLoadYmlConfig_ReturnsErrorWhenNoConfigFile": ("Missing execution.yml → error.", "No config file → non-nil error."),
    "executors/TestIsRS232Enabled_ReflectsSetRS232Enabled": ("IsRS232Enabled reflects SetRS232Enabled.", "SetRS232Enabled(true) → IsRS232Enabled()==true."),
    "executors/TestResetExecutingProgram_ClearsContextAndFiles": ("ResetExecutingProgram clears context and persistence files.", "Reset → last-line==0, last-program=='', ctx reset."),

    # ── datatypes ─────────────────────────────────────────────────────────────
    "datatypes/TestGetCommand_ExactMatch": ("GetCommand finds an exact match.", "Execution with 'G01' → returns G01 Command."),
    "datatypes/TestGetCommand_WildcardA": ("A90 matches the A** wildcard entry.", "GetCommand('A90') → Command{Func:'moveRotaryDegree'}."),
    "datatypes/TestGetCommand_WildcardB": ("B-10 matches the B** wildcard entry.", "GetCommand('B-10') → Command{Func:'moveRotaryDegree'}."),
    "datatypes/TestGetCommand_MultiCommandPipe": ("G55 matches G53|G54|G55|...|G58 pipe entry.", "GetCommand('G55') → workoffset Command."),
    "datatypes/TestGetCommand_SemicolonStripped": ("Semicolons are stripped before matching.", "GetCommand('G90;') → G90 Command."),
    "datatypes/TestGetCommand_CaseInsensitive": ("Matching is case-insensitive.", "GetCommand('g90') == GetCommand('G90')."),
    "datatypes/TestGetCommand_UnknownCommandFallsBackToInvalid": ("Unknown command falls back to invalidCommand entry.", "GetCommand('XYZ') → Command{Func:'invalidCommand'}."),
    "datatypes/TestExtractNumeric": ("ExtractNumeric strips the letter prefix and returns the number string.", "ExtractNumeric('A90')→'90', 'A-180.5'→'-180.5', 'F100'→'100'."),
    "datatypes/TestExtractNumericAsFloat": ("ExtractNumericAsFloat parses correctly.", "ExtractNumericAsFloat('A90.5')→90.5, 'B-10'→-10."),
    "datatypes/TestExtractNumericAsInt": ("ExtractNumericAsInt parses integer part.", "ExtractNumericAsInt('A90')→90."),
    "datatypes/TestExtractString": ("ExtractString returns the letter prefix.", "ExtractString('A90')→'A', 'G01'→'G'."),
    "datatypes/TestGetCommandFirstChar": ("GetCommandFirstChar returns the first character.", "'A90'→'A', 'G90;'→'G'."),
    "datatypes/TestGetValue": ("GetValue returns the full value substring after the prefix.", "'A90'→'90', 'F100;'→'100'."),
    "datatypes/TestGetValueAsInt_ValidInteger": ("GetValueAsInt parses valid integers.", "'A90'→90, 'R5'→5."),
    "datatypes/TestGetValueAsInt_FloatValueErrors": ("GetValueAsInt returns error for float values.", "'A90.5'→error."),
    "datatypes/TestMoveNextLine_AdvancesCounterWhenNotWaiting": ("MoveNextLine increments NextCmdLineToExec when not waiting.", "ctx not waiting → counter+1."),
    "datatypes/TestMoveNextLine_DoesNotAdvanceWhenWaitingForECS": ("MoveNextLine does not increment when waiting for ECS signal.", "ctx waiting → counter unchanged."),
    "datatypes/TestMoveToStart_ResetsToZero": ("MoveToStart sets NextCmdLineToExec to 0.", "ctx.NextCmdLineToExec=5, MoveToStart() → 0."),
    "datatypes/TestEndExecution_SetsNegativeOne": ("EndExecution sets NextCmdLineToExec to -1 (sentinel).", "EndExecution() → -1."),
    "datatypes/TestReset_ClearsAllTransientState": ("Reset clears RunMode, Err, position state, loop counters.", "Sets various fields, Reset() → all zeroed."),
    "datatypes/TestPrepareExecutingFile_SameFileLeavesStateIntact": ("Preparing the same file again leaves state intact.", "Prepare 'a.nc', Prepare 'a.nc' again → state unchanged."),
    "datatypes/TestPrepareExecutingFile_DifferentFileResetsAndUpdatesName": ("Different file resets state and updates filename.", "Prepare 'a.nc', Prepare 'b.nc' → state reset, Name=='b.nc'."),
    "datatypes/TestPrepareExecutingFile_AlwaysClearsStopFlag": ("PrepareExecutingFile always clears the stop flag.", "StopFlag=true, Prepare any file → StopFlag=false."),

    # ── helper ────────────────────────────────────────────────────────────────
    "helper/TestRoundFloat_BasicCases": (
        "RoundFloat rounds correctly at precisions 0, 1, 3 including half-values away from zero.",
        "Table of 15 cases; e.g. 3.5→4, -3.5→-4, 2.5→3 (not banker's 2).",
    ),
    "helper/TestRoundFloat_RotationDomain": (
        "Every tenth of a degree from -360° to 360° has ≤1 decimal place and must be unchanged by rounding to 3dp.",
        "7 201 inputs, asserts RoundFloat(x, 3) == x within 1e-9.",
    ),
    "helper/TestRoundFloatTo3_DelegatesCorrectly": (
        "RoundFloatTo3 must agree exactly with RoundFloat(x, 3) on all inputs.",
        "10 representative values; asserts RoundFloatTo3(x) == RoundFloat(x, 3).",
    ),
    "helper/TestRoundFloatTo3_ProductionLogValues": (
        "Values from real production motion logs round correctly.",
        "5 values including pitch-error offsets and a backlash artifact (89.996000000000001 → 89.996).",
    ),
    "helper/TestRound_HalfAwayFromZero": (
        "Internal round() helper uses away-from-zero, not banker's rounding.",
        "0.5→1, 2.5→3 (not 2), -0.5→-1, -2.5→-3 (not -2).",
    ),
    "helper/TestShortestPath_BasicCases": (
        "shortestPath returns the signed angular distance via the shorter arc.",
        "12 cases including wrap-forward (350°→10° = +20°), wrap-backward, tie-goes-negative.",
    ),
    "helper/TestShortestPath_AbsoluteResultNeverExceeds180": (
        "The absolute value of shortestPath must never exceed 180° for any input pair.",
        "Exhaustive sweep of all (current, target) in [0°, 360°), step 1°.",
    ),
    "helper/TestShortestPath_AntisymmetricExceptAtTie": ("shortestPath(a→b) == −shortestPath(b→a) except at the 180° tie.", "Sweep and check antisymmetry."),
    "helper/TestShortestPath_AppliedRotationLandsAtTarget": ("current + shortestPath(current, target) == target (mod 360).", "Sweep and verify."),
    "helper/TestGetAbsolutePosition_ShortestPath_DelegatesAndRounds": ("getAbsolutePosition with shortestPath=true delegates and rounds to 3dp.", "6-case table including wrap cases."),
    "helper/TestGetAbsolutePosition_LongPath_BasicCase": ("getAbsolutePosition with shortestPath=false takes the long arc.", "0°→90° long path = +270°, dest=90."),
    "helper/TestGetAbsolutePosition_DestinationNormalization": ("Destination is normalised to [0°, 360°) for any target.", "15 sub-cases including negatives and beyond 360°."),
    "helper/TestGetAbsolutePosition_DestinationAlwaysInRange_WorkingRange": ("Range invariant holds for working inputs.", "Sweep and assert 0 ≤ dest < 360."),
    "helper/TestGetRelativePosition_DocstringExample": ("Verifies the function matches its own docstring example.", "Check documented (current, target, prev) → (toMove, dest)."),
    "helper/TestGetRelativePosition_MinusOneSentinelMeansFirstMove": ("Sentinel −1 always triggers first-move guard.", "4 sub-cases: from 0, 90, 100, 350."),
    "helper/TestGetRelativePosition_DestinationInRangeForWorkingInputs": ("Destination always in [0°, 360°).", "Sweep working inputs."),
    "helper/TestGetRelativePosition_IsDeterministic": ("Same inputs always produce same output.", "Call twice, assert equal."),
    "helper/TestGetRelativePosition_DiffBranchDiff1LessThanDiff2": ("Branch where diff1 < diff2 is exercised.", "Choose inputs that force this branch."),
    "helper/TestGetRelativePosition_NegativePrevDestination": ("Negative prev destination handled correctly.", "prev=−10, current=350, target=+20."),
    "helper/TestIntToBinary_BasicCases": ("IntToBinary converts integers to 32-char binary string.", "0→'000…0', 1→'000…1', 255→'000…11111111'."),
    "helper/TestIntToBinary_AlwaysProduces32CharsForNonNegative": ("Result is always exactly 32 characters.", "Sweep 0–65535, assert len==32."),
    "helper/TestIntToBinary_BitPositionIndexing": ("Bit positions are correct (index 0 = MSB or LSB per convention).", "Check known bit patterns."),
    "helper/TestIntToBinary_NegativeInputDocumentedBehavior": ("Behaviour for negative input is documented.", "Negative input → documented result (pins current behaviour)."),
    "helper/TestReverse": ("Reverse reverses a string correctly.", "5 sub-cases: empty, single, two chars, palindrome, mixed."),
    "helper/TestReverse_IdempotentWhenDoubled": ("Reverse(Reverse(s)) == s for all inputs.", "Sweep representative strings."),
    "helper/TestReadLastLine_ReturnsLastLinePlusOne": ("ReadLastLine returns stored value + 1 (resume from next line).", "Write '3', ReadLastLine → 4."),
    "helper/TestReadLastLine_EmptyFile_ReturnsZero": ("Empty file → 0.", "Write '', ReadLastLine → 0."),
    "helper/TestReadLastLine_FileNotExist_ReturnsZero": ("Missing file → 0.", "No file → 0."),
    "helper/TestReadLastLine_CorruptContent_ReturnsErr": ("Non-integer content returns error.", "Write 'abc', ReadLastLine → error."),
    "helper/TestReadLastLine_WhitespaceAndNewlines": ("Whitespace and newlines are trimmed before parsing.", "Write '5\\n', ReadLastLine → 5."),
    "helper/TestWriteLastLine_ThenReadBack": ("WriteLastLine round-trips through ReadLastLine.", "Write 7, ReadLastLine → 7+1=8 (note: +1 convention)."),
    "helper/TestWriteLastLine_OverwritesPreviousValue": ("Second write overwrites first.", "Write 3, write 9, ReadLastLine → 10."),
    "helper/TestReadSerialNumber_ReturnsNoError": ("ReadSerialNumber returns nil error on this platform.", "Call ReadSerialNumber() → nil error."),
    "helper/TestReadSerialNumber_OnPiReturnsNonEmpty": ("On a Raspberry Pi the serial number is non-empty.", "Call ReadSerialNumber() → non-empty string."),
    "helper/TestEncryptDecrypt_RoundTrip": ("Encrypt then Decrypt returns the original plaintext.", "Encrypt('hello', key), Decrypt(ciphertext, key) → 'hello'."),
    "helper/TestEncryptDecrypt_EmptyString": ("Empty string encrypts and decrypts correctly.", "Encrypt('', key) → Decrypt → ''."),
    "helper/TestEncryptDecrypt_LongString": ("Long string round-trips correctly.", "Encrypt 1000-char string, decrypt → original."),
    "helper/TestEncryptDecrypt_DifferentKeys_ProduceDifferentCiphertext": ("Different keys produce different ciphertext.", "Encrypt same plaintext with two keys → different ciphertext."),
    "helper/TestEncrypt_DifferentNonceEachCall": ("Each Encrypt call uses a fresh random nonce → different ciphertext.", "Encrypt same plaintext twice → different output."),
    "helper/TestEncrypt_ProducesNonReadableCiphertext": ("Ciphertext is not the plaintext.", "Encrypted != original."),
    "helper/TestDecrypt_PanicsOnTooShortCiphertext": ("Decrypt panics if ciphertext is shorter than nonce size.", "Pass 1-byte ciphertext → recover() catches panic."),
    "helper/TestCopyFile_ContentMatches": ("CopyFile produces identical file content.", "Copy, read both, assert equal."),
    "helper/TestCopyFile_MissingSource_ReturnsErr": ("Missing source → error.", "CopyFile('/nonexistent', dst) → error."),
    "helper/TestCopyFile_PreservesPermissions": ("Copied file has same permissions.", "Copy, stat both, assert mode equal."),
    "helper/TestCopyDir_CopiesFilesRecursively": ("CopyDir copies all files recursively.", "Source with nested files → destination has all files."),
    "helper/TestCopyDir_MissingSource_ReturnsErr": ("Missing source directory → error.", "CopyDir('/nonexistent', dst) → error."),
    "helper/TestCopyDir_SourceNotDirectory_ReturnsErr": ("Source is a file not a directory → error.", "CopyDir(file, dst) → error."),

    # ── restapi ────────────────────────────────────────────────────────────────
    "restapi/TestGetProgramFiles_OptionsReturns200": ("OPTIONS preflight on /program-files returns 200.", "HTTP OPTIONS → 200."),
    "restapi/TestGetProgramFiles_WrongMethodReturns404": ("Wrong HTTP method on /program-files returns 404.", "HTTP DELETE → 404."),
    "restapi/TestGetProgramFiles_WrongPathReturns404": ("Wrong path returns 404.", "GET /nonexistent → 404."),
    "restapi/TestProgramFilesRESTIntegration_ListReadRenameAndDelete": (
        "Full CRUD cycle: list files, read content, rename, delete via REST.",
        "Uses httptest.NewRecorder to drive the handler; verifies status codes and JSON bodies.",
    ),
    "restapi/TestProgramFilesRESTIntegration_CORSPreflightDoesNotTouchHandlers": ("OPTIONS preflight does not invoke underlying handlers.", "OPTIONS → 200, no handler side-effects."),
    "restapi/TestProgramFilesRESTIntegration_RequiredParameterFailures": ("Missing required query params return error responses.", "5 sub-cases: delete missing name, get missing name, get missing file, rename missing old/new."),
    "restapi/TestSaveProgramRESTIntegration_RejectsMalformedProgramAndDeletesFile": (
        "Saving a program with syntax errors rejects it and removes the uploaded file.",
        "POST malformed .nc → error response, file deleted from disk.",
    ),
    "restapi/TestSaveProgramRESTIntegration_SaveValidCommentOnlyProgram": ("A comment-only program saves successfully.", "POST comment-only .nc → 200 success."),
    "restapi/TestManipulateSettings_GetReturnsJSONResponse": ("GET /settings returns JSON.", "HTTP GET → JSON body."),
    "restapi/TestManipulateSettings_OptionsReturns200": ("OPTIONS /settings → 200.", "HTTP OPTIONS → 200."),
    "restapi/TestManipulateSettings_PostWithValidJSONReturnsResponse": ("POST valid settings JSON → response.", "POST JSON → 200 with response body."),
    "restapi/TestGetSupport_GetReturnsJSON": ("GET /support returns JSON.", "HTTP GET → JSON."),
    "restapi/TestGetSupport_OptionsReturns200": ("OPTIONS /support → 200.", "HTTP OPTIONS → 200."),
    "restapi/TestReadFaq_GetReturnsJSONWithStatus": ("GET /faq returns JSON with status field.", "HTTP GET → JSON with 'status' key."),
    "restapi/TestReadFaq_OptionsReturns200": ("OPTIONS /faq → 200.", "HTTP OPTIONS → 200."),
    "restapi/TestReadFaq_PostIsIgnored": ("POST /faq is ignored (read-only endpoint).", "HTTP POST → ignored, no error."),
    "restapi/TestReadPwd_GetReturnsJSONWithStatus": ("GET /pwd returns JSON.", "HTTP GET → JSON."),
    "restapi/TestReadPwd_OptionsReturns200": ("OPTIONS /pwd → 200.", "HTTP OPTIONS → 200."),
    "restapi/TestReadPwd_PostIsIgnored": ("POST /pwd ignored.", "HTTP POST → no error."),
    "restapi/TestParseRS232EnabledPayload_EnabledKey": ("Payload with 'enabled' key parsed correctly.", "{'enabled':true} → true."),
    "restapi/TestParseRS232EnabledPayload_DataKey": ("Payload with 'data' key parsed correctly.", "{'data':1} → true."),
    "restapi/TestParseRS232EnabledPayload_EmptyBodyReturnsFalse": ("Empty body → false.", "'' → false."),
    "restapi/TestParseRS232EnabledPayload_InvalidJSONReturnsFalse": ("Invalid JSON → false.", "'not json' → false."),
    "restapi/TestParseRS232EnabledPayload_NoKnownKeyReturnsFalse": ("JSON without known key → false.", "{'x':1} → false."),
    "restapi/TestParseRS232EnabledValue_Bool": ("Bool value parsed correctly.", "true→true, false→false."),
    "restapi/TestParseRS232EnabledValue_Float64": ("Float64 value: 1.0→true, 0.0→false.", "parseRS232EnabledValue(1.0)→true."),
    "restapi/TestParseRS232EnabledValue_StringTruthy": ("String '1'/'true'→true.", "'1'→true."),
    "restapi/TestParseRS232EnabledValue_StringFalsy": ("String '0'/'false'→false.", "'0'→false."),
    "restapi/TestParseRS232EnabledValue_UnknownStringReturnsFalse": ("Unknown string → false.", "'maybe'→false."),
    "restapi/TestParseRS232EnabledValue_UnknownTypeReturnsFalse": ("Unknown Go type → false.", "struct{}{}→false."),
    "restapi/TestRS232State_OptionsReturns200": ("OPTIONS /rs232-state → 200.", "HTTP OPTIONS → 200."),
    "restapi/TestRS232State_PostWithValidPayload_ReturnsSuccess": ("POST valid RS232 payload → success.", "POST {'enabled':true} → 200."),
    "restapi/TestRS232State_PostWithMissingEnabledKey_ReturnsError": ("POST without enabled key → error.", "POST {} → error body."),
    "restapi/TestRS232State_MethodNotAllowedReturns200WithErrorBody": ("Unsupported method returns 200 with error body (API convention).", "DELETE → 200 with error JSON."),
    "restapi/TestSetupCorsResponse_SetsAllHeaders": ("setupCorsResponse sets all required CORS headers.", "Call setupCorsResponse, assert Access-Control-Allow-* headers present."),
    "restapi/TestNewApi_ReturnsNonZeroValue": ("NewApi returns a non-nil API instance.", "NewApi() != nil."),
    "restapi/TestGetCodeFilePath_ReturnsNonEmptyString": ("GetCodeFilePath returns a non-empty path string.", "GetCodeFilePath() != ''."),
    "restapi/TestBoolToInt": ("boolToInt converts true→1, false→0.", "boolToInt(true)==1, boolToInt(false)==0."),
}


def parse_time(value: str | None) -> dt.datetime | None:
    if not value:
        return None
    try:
        if value.endswith("Z"):
            value = value[:-1] + "+00:00"
        return dt.datetime.fromisoformat(value)
    except ValueError:
        return None


def read_jsonl(path: Path, phase: str) -> Iterable[Dict[str, Any]]:
    if not path.exists():
        return
    with path.open("r", encoding="utf-8", errors="replace") as handle:
        for line_no, line in enumerate(handle, 1):
            line = line.strip()
            if not line:
                continue
            try:
                event = json.loads(line)
            except json.JSONDecodeError:
                yield {"Phase": phase, "Action": "output", "Package": "",
                       "Output": f"[non-json line {line_no}] {line}\n"}
                continue
            event.setdefault("Phase", phase)
            yield event


def load_phase_status(path: Path) -> Dict[str, int]:
    statuses: Dict[str, int] = {}
    if not path.exists():
        return statuses
    with path.open("r", encoding="utf-8", errors="replace") as handle:
        for line in handle:
            parts = line.rstrip("\n").split("\t")
            if len(parts) != 2:
                continue
            try:
                statuses[parts[0]] = int(parts[1])
            except ValueError:
                statuses[parts[0]] = 1
    return statuses


def tail_lines(lines: List[str], max_lines: int = 40) -> str:
    if len(lines) <= max_lines:
        return "".join(lines)
    return "... output truncated ...\n" + "".join(lines[-max_lines:])


def collect(paths: List[Tuple[str, Path]], status_file: Path) -> Dict[str, Any]:
    test_map: Dict[Tuple[str, str, str], Dict[str, Any]] = {}
    package_map: Dict[Tuple[str, str], Dict[str, Any]] = {}
    output_by_test: Dict[Tuple[str, str, str], List[str]] = {}
    output_by_package: Dict[Tuple[str, str], List[str]] = {}
    first_time: dt.datetime | None = None
    last_time: dt.datetime | None = None
    event_count = 0

    for phase, path in paths:
        for event in read_jsonl(path, phase):
            event_count += 1
            phase_name = str(event.get("Phase", phase))
            pkg = str(event.get("Package", ""))
            test = str(event.get("Test", ""))
            action = str(event.get("Action", ""))
            elapsed = event.get("Elapsed")
            output = event.get("Output")
            event_time = parse_time(event.get("Time"))
            if event_time:
                first_time = event_time if first_time is None else min(first_time, event_time)
                last_time = event_time if last_time is None else max(last_time, event_time)

            if output:
                if test:
                    output_by_test.setdefault((phase_name, pkg, test), []).append(str(output))
                else:
                    output_by_package.setdefault((phase_name, pkg), []).append(str(output))

            if test and action in TERMINAL_TEST_ACTIONS:
                key = (phase_name, pkg, test)
                rec = test_map.setdefault(key, {"phase": phase_name, "package": pkg, "test": test,
                                                 "action": action, "elapsed": None, "output": ""})
                rec["action"] = action
                rec["elapsed"] = elapsed
            elif pkg and not test and action in TERMINAL_PACKAGE_ACTIONS:
                key = (phase_name, pkg)
                rec = package_map.setdefault(key, {"phase": phase_name, "package": pkg,
                                                    "action": action, "elapsed": None, "output": ""})
                rec["action"] = action
                rec["elapsed"] = elapsed

    for key, rec in test_map.items():
        rec["output"] = tail_lines(output_by_test.get(key, []))
    for key, rec in package_map.items():
        rec["output"] = tail_lines(output_by_package.get(key, []))

    tests = sorted(test_map.values(), key=lambda r: (r["phase"], r["package"], r["test"]))
    packages = sorted(package_map.values(), key=lambda r: (r["phase"], r["package"]))
    phase_status = load_phase_status(status_file)

    summary = {
        "generated_at": dt.datetime.now(dt.timezone.utc).isoformat(timespec="seconds"),
        "started_at": first_time.isoformat() if first_time else None,
        "finished_at": last_time.isoformat() if last_time else None,
        "event_count": event_count,
        "total_tests": len(tests),
        "passed": sum(1 for t in tests if t["action"] == "pass"),
        "failed": sum(1 for t in tests if t["action"] == "fail"),
        "skipped": sum(1 for t in tests if t["action"] == "skip"),
        "packages_passed": sum(1 for p in packages if p["action"] == "pass"),
        "packages_failed": sum(1 for p in packages if p["action"] == "fail"),
        "phase_status": phase_status,
        # overall_status is FAIL only when there are genuine test or package
        # failures — not when a phase exits non-zero purely due to skipped tests.
        # A phase can exit 1 when all its tests are skipped (e.g. hardware-ecs
        # with no motion tests opted in) without any actual FAIL events.
        "overall_status": "PASS" if (
            not any(t["action"] == "fail" for t in tests)
            and not any(p["action"] == "fail" for p in packages)
        ) else "FAIL",
    }
    return {"summary": summary, "tests": tests, "packages": packages}


def pct(part: int, total: int) -> str:
    if total == 0:
        return "0.0%"
    return f"{part * 100.0 / total:.1f}%"


def write_json(report: Dict[str, Any], path: Path) -> None:
    path.write_text(json.dumps(report, indent=2, sort_keys=True), encoding="utf-8")


def write_markdown(report: Dict[str, Any], path: Path) -> None:
    s = report["summary"]
    failed_tests = [t for t in report["tests"] if t["action"] == "fail"]
    skipped_tests = [t for t in report["tests"] if t["action"] == "skip"]
    slow_tests = sorted(
        [t for t in report["tests"] if isinstance(t.get("elapsed"), (int, float))],
        key=lambda t: float(t.get("elapsed") or 0), reverse=True)[:20]

    lines: List[str] = []
    lines.append(f"# Go Test Report — {s['overall_status']}\n\n")
    lines.append(f"Generated: `{s['generated_at']}`\n\n")
    lines.append("## Summary\n\n")
    lines.append("| Metric | Value |\n|---|---:|\n")
    for key in ["total_tests", "passed", "failed", "skipped", "packages_passed", "packages_failed", "event_count"]:
        lines.append(f"| {key.replace('_', ' ').title()} | {s[key]} |\n")
    lines.append(f"| Pass Rate | {pct(s['passed'], s['total_tests'])} |\n\n")
    lines.append("## Phase exit status\n\n")
    lines.append("| Phase | Exit Code |\n|---|---:|\n")
    for phase, code in sorted(s["phase_status"].items()):
        lines.append(f"| {phase} | {code} |\n")
    lines.append("\n")
    if failed_tests:
        lines.append("## Failed tests\n\n")
        for t in failed_tests:
            lines.append(f"### {t['test']}\n\n")
            lines.append(f"Package: `{t['package']}`  \nPhase: `{t['phase']}`  \nElapsed: `{t.get('elapsed')}`\n\n")
            if t.get("output"):
                lines.append("```text\n" + t["output"] + "\n```\n\n")
    else:
        lines.append("## Failed tests\n\nNone.\n\n")
    lines.append("## Slowest tests\n\n")
    lines.append("| Phase | Package | Test | Seconds | Result |\n|---|---|---|---:|---|\n")
    for t in slow_tests:
        lines.append(f"| {t['phase']} | `{t['package']}` | `{t['test']}` | {float(t.get('elapsed') or 0):.2f} | {t['action']} |\n")
    lines.append("\n")
    if skipped_tests:
        lines.append("## Skipped tests\n\n")
        lines.append("| Phase | Package | Test |\n|---|---|---|\n")
        for t in skipped_tests:
            lines.append(f"| {t['phase']} | `{t['package']}` | `{t['test']}` |\n")
        lines.append("\n")
    path.write_text("".join(lines), encoding="utf-8")


def _desc_key(pkg: str, test_name: str) -> str:
    """Build lookup key from package path and test function name."""
    short_pkg = pkg.replace("EtherCAT/", "")
    return f"{short_pkg}/{test_name}"


def _get_desc(pkg: str, test_name: str) -> Tuple[str, str]:
    key = _desc_key(pkg, test_name)
    if key in TEST_DESCRIPTIONS:
        return TEST_DESCRIPTIONS[key]
    # Auto-generate from test name: split on _ and capitalise
    readable = test_name.replace("Test", "", 1).replace("_", " ").strip()
    return readable, ""


def write_html(report: Dict[str, Any], path: Path) -> None:
    s = report["summary"]
    tests = report["tests"]
    packages = report["packages"]
    failed_tests = [t for t in tests if t["action"] == "fail"]

    from collections import defaultdict
    # Group top-level tests by phase → package
    by_phase_pkg: Dict[str, Dict[str, List[Dict[str, Any]]]] = defaultdict(lambda: defaultdict(list))
    for t in tests:
        if "/" not in t["test"]:
            by_phase_pkg[t["phase"]][t["package"]].append(t)

    def esc(v: Any) -> str:
        return html.escape("" if v is None else str(v))

    overall_pass = s["overall_status"] == "PASS"

    # ── Duration ─────────────────────────────────────────────────────────────
    duration_str = ""
    started, finished = s.get("started_at"), s.get("finished_at")
    if started and finished:
        try:
            def _p(v: str) -> dt.datetime:
                v = v.replace("Z", "+00:00")
                if v.endswith("+00:00"):
                    v = v[:-6]
                    return dt.datetime.fromisoformat(v).replace(tzinfo=dt.timezone.utc)
                return dt.datetime.fromisoformat(v)
            secs = (_p(finished) - _p(started)).total_seconds()
            duration_str = f"{int(secs//60)}m {int(secs%60)}s" if secs >= 60 else f"{secs:.1f}s"
        except Exception:
            pass

    parts: List[str] = []
    parts.append(f"""<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>EtherCAT Test Report — {esc(s['overall_status'])}</title>
<style>
@import url('https://fonts.googleapis.com/css2?family=IBM+Plex+Mono:wght@400;500&family=IBM+Plex+Sans:wght@300;400;500;600&display=swap');
:root{{
  --bg:#0f1117;--surface:#181c24;--border:#262c38;
  --text:#d4dae8;--muted:#5c6478;
  --pass:#36d6b5;--fail:#e05c5c;--skip:#f0a500;--warn:#f0a500;
  --accent:#4f9cf9;--tag:#1e2533;
  --mono:'IBM Plex Mono',monospace;--sans:'IBM Plex Sans',sans-serif;
}}
*{{box-sizing:border-box;margin:0;padding:0}}
body{{background:var(--bg);color:var(--text);font-family:var(--sans);font-size:14px;line-height:1.6;padding:40px 28px 80px}}

/* header */
.hdr{{border-left:3px solid var(--accent);padding-left:20px;margin-bottom:36px}}
.hdr h1{{font-size:26px;font-weight:600;color:#fff;letter-spacing:-.4px}}
.hdr .sub{{color:var(--muted);font-family:var(--mono);font-size:12px;margin-top:4px}}
.pill{{display:inline-block;padding:3px 12px;border-radius:4px;font-family:var(--mono);font-size:13px;font-weight:600;margin-left:10px;vertical-align:middle}}
.pill.pass{{background:rgba(54,214,181,.15);color:var(--pass)}}
.pill.fail{{background:rgba(224,92,92,.15);color:var(--fail)}}

/* stat grid */
.grid{{display:grid;grid-template-columns:repeat(auto-fit,minmax(140px,1fr));gap:12px;margin-bottom:44px}}
.stat{{background:var(--surface);border:1px solid var(--border);border-radius:8px;padding:18px 20px}}
.stat .num{{font-size:32px;font-weight:600;font-family:var(--mono);color:#fff;line-height:1}}
.stat .lbl{{font-size:11px;text-transform:uppercase;letter-spacing:.08em;color:var(--muted);margin-top:5px}}
.stat.c-pass .num{{color:var(--pass)}} .stat.c-fail .num{{color:var(--fail)}} .stat.c-acc .num{{color:var(--accent)}}

/* badges */
.badge{{display:inline-block;padding:2px 9px;border-radius:4px;font-family:var(--mono);font-size:11px;font-weight:500;border:1px solid}}
.badge.pass{{background:rgba(54,214,181,.1);color:var(--pass);border-color:rgba(54,214,181,.25)}}
.badge.fail{{background:rgba(224,92,92,.1);color:var(--fail);border-color:rgba(224,92,92,.25)}}
.badge.skip{{background:rgba(240,165,0,.1);color:var(--skip);border-color:rgba(240,165,0,.25)}}
.badge.unit{{background:rgba(79,156,249,.1);color:var(--accent);border-color:rgba(79,156,249,.25)}}
.badge.integration{{background:rgba(54,214,181,.1);color:var(--pass);border-color:rgba(54,214,181,.25)}}
.badge.hardware,.badge.hardware-motion,.badge.hardware-readonly{{background:rgba(240,165,0,.1);color:var(--warn);border-color:rgba(240,165,0,.25)}}

/* section */
.sec{{margin-bottom:48px}}
.sec-hdr{{display:flex;align-items:baseline;gap:10px;padding-bottom:10px;border-bottom:1px solid var(--border);margin-bottom:20px}}
.sec-hdr h2{{font-size:18px;font-weight:600;color:#fff}}
.sec-hdr .cnt{{font-family:var(--mono);font-size:12px;color:var(--muted)}}

/* phase table */
.tbl{{width:100%;border-collapse:collapse}}
.tbl th{{background:#1e2330;color:var(--muted);font-size:11px;text-transform:uppercase;letter-spacing:.06em;padding:8px 14px;text-align:left;border-bottom:1px solid var(--border)}}
.tbl td{{padding:10px 14px;border-bottom:1px solid var(--border);vertical-align:top}}
.tbl tr:last-child td{{border-bottom:none}}
.tbl code{{font-family:var(--mono);font-size:12px;color:var(--muted)}}

/* package groups */
.pkg{{background:var(--surface);border:1px solid var(--border);border-radius:8px;margin-bottom:14px;overflow:hidden}}
details>summary{{list-style:none;cursor:pointer}}
details>summary::-webkit-details-marker{{display:none}}
.pkg-hdr{{display:flex;align-items:center;gap:10px;padding:13px 18px;border-bottom:1px solid var(--border)}}
.pkg-hdr:hover{{background:rgba(79,156,249,.04)}}
.pkg-hdr h3{{font-size:13px;font-weight:500;color:#fff;flex:1;font-family:var(--mono)}}
.arrow{{font-size:10px;color:var(--muted)}}
details[open] .arrow{{transform:rotate(90deg);display:inline-block}}

/* test items */
.test-item{{display:grid;grid-template-columns:20px 1fr;gap:10px;align-items:start;padding:12px 18px;border-bottom:1px solid rgba(38,44,56,.6)}}
.test-item:last-child{{border-bottom:none}}
.dot{{width:7px;height:7px;border-radius:50%;margin-top:6px;flex-shrink:0}}
.dot.pass{{background:var(--pass)}} .dot.fail{{background:var(--fail)}} .dot.skip{{background:var(--skip)}} .dot.warn{{background:var(--warn)}}
.test-name{{font-family:var(--mono);font-size:12px;color:var(--accent);margin-bottom:4px}}
.test-name.fail{{color:var(--fail)}}
.what{{font-size:13px;color:var(--text);line-height:1.5}}
.how{{font-size:12px;color:var(--muted);margin-top:3px;line-height:1.4}}
.elapsed-tag{{font-family:var(--mono);font-size:11px;color:var(--muted);white-space:nowrap;padding:1px 6px;border:1px solid var(--border);border-radius:3px;display:inline-block;margin-top:4px}}

/* warn box */
.warn-box{{background:rgba(240,165,0,.07);border:1px solid rgba(240,165,0,.25);border-radius:6px;padding:12px 16px;margin:8px 0;font-size:13px;color:var(--warn)}}

/* failure pre */
.fail-pre{{background:rgba(0,0,0,.3);border:1px solid var(--border);border-radius:6px;padding:12px;font-size:12px;font-family:var(--mono);overflow:auto;color:var(--text);white-space:pre-wrap;max-height:300px;margin-top:8px}}
</style>
</head>
<body>
""")

    # ── Header ───────────────────────────────────────────────────────────────
    status_cls = "pass" if overall_pass else "fail"
    dur = f" · {esc(duration_str)}" if duration_str else ""
    parts.append(f"""<div class="hdr">
  <h1>EtherCAT Motor Controller — Test Report
    <span class="pill {status_cls}">{esc(s['overall_status'])}</span>
  </h1>
  <div class="sub">{esc(s['generated_at'])}{dur} · {esc(s['total_tests'])} tests · {esc(len(packages))} packages</div>
</div>
""")

    # ── Stats ────────────────────────────────────────────────────────────────
    fail_cls = "c-fail" if s["failed"] > 0 else ""
    parts.append(f"""<div class="grid">
  <div class="stat c-acc"><div class="num">{esc(s['total_tests'])}</div><div class="lbl">Total tests</div></div>
  <div class="stat c-pass"><div class="num">{esc(s['passed'])}</div><div class="lbl">Passed</div></div>
  <div class="stat {fail_cls}"><div class="num">{esc(s['failed'])}</div><div class="lbl">Failed</div></div>
  <div class="stat"><div class="num">{esc(s['skipped'])}</div><div class="lbl">Skipped</div></div>
  <div class="stat c-pass"><div class="num">{pct(s['passed'],s['total_tests'])}</div><div class="lbl">Pass rate</div></div>
  <div class="stat"><div class="num">{esc(s['packages_passed'])}</div><div class="lbl">Packages passed</div></div>
</div>
""")

    # ── Phase status ─────────────────────────────────────────────────────────
    parts.append("""<div class="sec">
<div class="sec-hdr"><h2>Phase exit status</h2></div>
<table class="tbl"><tr><th>Phase</th><th>Result</th><th>Exit code</th></tr>
""")
    for phase, code in sorted(s["phase_status"].items()):
        ph_cls = "integration" if phase == "integration" else ("unit" if phase == "unit" else "hardware")
        b_cls = "pass" if code == 0 else "fail"
        parts.append(f"<tr><td><span class='badge {ph_cls}'>{esc(phase)}</span></td>"
                     f"<td><span class='badge {b_cls}'>{'PASS' if code==0 else 'FAIL'}</span></td>"
                     f"<td><code>{esc(code)}</code></td></tr>\n")
    parts.append("</table></div>\n")

    # ── Failures ─────────────────────────────────────────────────────────────
    parts.append(f"""<div class="sec">
<div class="sec-hdr"><h2>Failed tests</h2><span class="cnt">{len(failed_tests)}</span></div>
""")
    if not failed_tests:
        parts.append("<p style='color:var(--pass);font-family:var(--mono);font-size:13px'>✓ All tests passed</p>\n")
    else:
        for t in failed_tests:
            what, how = _get_desc(t["package"], t["test"])
            e = f"{float(t.get('elapsed') or 0):.2f}s" if t.get("elapsed") is not None else ""
            parts.append(f"""<div style="background:rgba(224,92,92,.06);border:1px solid rgba(224,92,92,.2);border-radius:8px;padding:16px;margin-bottom:12px">
  <div style="font-family:var(--mono);font-size:13px;color:var(--fail);margin-bottom:6px">{esc(t['test'])}</div>
  <div style="font-size:12px;color:var(--muted);margin-bottom:8px">{esc(t['phase'])} · <code>{esc(t['package'])}</code> · {e}</div>
  <div style="font-size:13px;margin-bottom:4px">{esc(what)}</div>
""")
            if t.get("output"):
                parts.append(f'  <pre class="fail-pre">{esc(t["output"])}</pre>\n')
            parts.append("</div>\n")
    parts.append("</div>\n")

    # ── Tests by package ─────────────────────────────────────────────────────
    parts.append(f"""<div class="sec">
<div class="sec-hdr"><h2>What each test checks</h2><span class="cnt">{s['total_tests']} tests grouped by package</span></div>
""")
    phase_order = {"unit": 0, "integration": 1}
    for phase in sorted(by_phase_pkg.keys(), key=lambda p: (phase_order.get(p, 99), p)):
        pkg_map = by_phase_pkg[phase]
        if not pkg_map:
            continue
        ph_cls = "integration" if phase == "integration" else ("unit" if phase == "unit" else "hardware")
        parts.append(f"<div style='display:flex;align-items:center;gap:8px;margin:20px 0 10px'>"
                     f"<span class='badge {ph_cls}'>{esc(phase)}</span>"
                     f"<span style='color:var(--muted);font-size:12px;font-family:var(--mono)'>"
                     f"{sum(len(v) for v in pkg_map.values())} tests</span></div>\n")

        for pkg in sorted(pkg_map.keys()):
            pkg_tests = pkg_map[pkg]
            n_pass = sum(1 for t in pkg_tests if t["action"] == "pass")
            n_fail = sum(1 for t in pkg_tests if t["action"] == "fail")
            pkg_elapsed = next((p.get("elapsed") for p in packages
                                if p["package"] == pkg and p["phase"] == phase), None)
            e_str = f"{float(pkg_elapsed):.3f}s" if isinstance(pkg_elapsed, (int, float)) else ""
            b_cls = "pass" if n_fail == 0 else "fail"
            short = pkg.replace("EtherCAT/", "")

            parts.append(f"""<details class="pkg">
<summary>
<div class="pkg-hdr">
  <span class="arrow">▶</span>
  <h3>{esc(short)}</h3>
  <span class="badge {b_cls}">{n_pass}✓{"  "+str(n_fail)+"✗" if n_fail else ""}</span>
  <span style="font-family:var(--mono);font-size:11px;color:var(--muted)">{esc(e_str)}</span>
</div>
</summary>
""")
            for t in sorted(pkg_tests, key=lambda x: x["test"]):
                what, how = _get_desc(t["package"], t["test"])
                dot_cls = t["action"]
                name_cls = "fail" if t["action"] == "fail" else ""
                t_e = t.get("elapsed")
                t_e_str = f"{float(t_e):.3f}s" if isinstance(t_e, (int, float)) and t_e > 0 else ""

                # Known defect gets a special warning dot
                is_defect = "NOTLimit" in t["test"] or "BROKEN" in what or "DEFECT" in what or "PINNED" in what
                if is_defect:
                    dot_cls = "warn"

                parts.append(f"""<div class="test-item">
  <div class="dot {dot_cls}"></div>
  <div>
    <div class="test-name {name_cls}">{esc(t['test'])}</div>
    <div class="what">{esc(what)}</div>
""")
                if how:
                    parts.append(f'    <div class="how">{esc(how)}</div>\n')
                if t_e_str:
                    parts.append(f'    <span class="elapsed-tag">{esc(t_e_str)}</span>\n')
                if is_defect:
                    parts.append('    <div class="warn-box" style="margin-top:6px"><strong>⚠ Known defect pinned</strong> — this test locks in broken behaviour intentionally. See test comment for the TODO fix.</div>\n')
                if t["action"] == "fail" and t.get("output"):
                    parts.append(f'    <pre class="fail-pre">{esc(t["output"])}</pre>\n')
                parts.append("  </div>\n</div>\n")

            parts.append("</details>\n")

    parts.append("</div>\n")

    # ── Footer ───────────────────────────────────────────────────────────────
    parts.append(f"""<div style="border-top:1px solid var(--border);padding-top:20px;margin-top:40px;
                       color:var(--muted);font-family:var(--mono);font-size:11px">
  EtherCAT test report · generated {esc(s['generated_at'])}
</div>
</body></html>
""")
    path.write_text("".join(parts), encoding="utf-8")


def main() -> int:
    parser = argparse.ArgumentParser(description="Generate test reports from go test -json output")
    parser.add_argument("--input-dir", required=True)
    parser.add_argument("--output-dir", required=True)
    parser.add_argument("--status-file", default="phase-status.tsv")
    args = parser.parse_args()

    input_dir = Path(args.input_dir)
    output_dir = Path(args.output_dir)
    status_file = Path(args.status_file)
    if not status_file.is_absolute():
        status_file = input_dir / status_file

    paths = [(p.stem, p) for p in sorted(input_dir.glob("*.jsonl"))]
    report = collect(paths, status_file)
    output_dir.mkdir(parents=True, exist_ok=True)
    write_json(report, output_dir / "summary.json")
    write_markdown(report, output_dir / "report.md")
    write_html(report, output_dir / "report.html")

    print(f"Report status: {report['summary']['overall_status']}")
    print(f"HTML report: {output_dir / 'report.html'}")
    print(f"Markdown report: {output_dir / 'report.md'}")
    return 0 if report["summary"]["overall_status"] == "PASS" else 1


if __name__ == "__main__":
    raise SystemExit(main())