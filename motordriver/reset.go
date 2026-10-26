package motordriver

/*
#include "ethercatinterface.h"
#include <errno.h>
*/
import "C"

import (
	"fmt"
	"time"

	logger "EtherCAT/logger"
)

// ResetDriver clears a CiA-402 fault. PDO path delegates to PDOFaultReset;
// SDO path used pre-activation only.
func ResetDriver(avilableDevices []*MasterDevice) error {
	if IsPDOActive() {
		// Delegate to PDOFaultReset which handles the full fault-reset sequence
		// via the running cyclic task (write 0x80, wait for bit3=0, re-enable).
		logger.Info("[PDO] ResetDriver: delegating to PDOFaultReset")
		PDOFaultReset(avilableDevices)
		return nil
	}

	// SDO path: pre-activation commissioning only.
	for _, device := range avilableDevices {
		operation, err := GetEtherCATOperation("faultReset", device.Device.AddressConfigName)
		if err != nil {
			return err
		}
		logger.Info("[SDO] ResetDriver: fault reset via SDO for drive:", device.Name)
		runSDOOperation(device.Master, device.Position, operation)
	}
	return nil
}

// forceServoCycle recovers a drive that is not faulted but is stuck outside
// Operation Enabled (for example after a quick stop or power-off sequence).
// PDOFaultReset intentionally skips such drives because their fault bit is 0.
func forceServoCycle(devices []*MasterDevice) {
	if !IsPDOActive() {
		return
	}

	const (
		disableTimeout = 200 * time.Millisecond
		enableTimeout  = 200 * time.Millisecond
	)

	for _, d := range devices {
		if d == nil || !d.PdoRxReady {
			continue
		}

		d.EnableJogPDO(false)
		d.EnablePosPDO(false)
		d.desiredTargetVelocity.Store(0)
		_ = d.SetTargetPositionPDO(d.PDOPos.Load())

		// FastPowerOff/ShutdownMasters leaves this asserted. If it is not
		// cleared, the PDO loop keeps sending CW=0x0006 forever and reset can
		// report success while the drive remains Ready To Switch On.
		d.pdoShutdownActive.Store(false)

		sw := uint16(d.PDOStatus.Load() & 0xFFFF)
		if (sw & 0x006F) == 0x0027 {
			continue
		}

		logger.Info("[RESET] cycling servo for", d.Name,
			"sw=", fmt.Sprintf("0x%04X", sw))
		d.pdoForcedServoCycleActive.Store(true)

		disableDeadline := time.Now().Add(disableTimeout)
		for time.Now().Before(disableDeadline) {
			if uint16(d.PDOStatus.Load()&0x006F) == 0x0021 {
				break
			}
			time.Sleep(2 * time.Millisecond)
		}

		d.pdoForcedServoCycleActive.Store(false)
		enableDeadline := time.Now().Add(enableTimeout)
		for time.Now().Before(enableDeadline) {
			if uint16(d.PDOStatus.Load()&0x006F) == 0x0027 {
				break
			}
			time.Sleep(2 * time.Millisecond)
		}

		finalSW := uint16(d.PDOStatus.Load() & 0xFFFF)
		if (finalSW & 0x006F) != 0x0027 {
			logger.Warn("[RESET] servo cycle did not reach Operation Enabled for", d.Name,
				"sw=", fmt.Sprintf("0x%04X", finalSW))
		}
	}
}

// resetMultiTurn dispatches each device to its own Driver.ResetMultiTurn().
//
// WHY PER-DEVICE DISPATCH:
//   The old version called GetMotorDriver().ResetMultiTurn(availableDevices)
//   which used the global driver — wrong for mixed-axis setups. A6 uses async
//   SDO objects (0x4D01/0x4D00); Delta uses blocking CoE writes (P2-08/P2-71);
//   Nidec uses its own YAML-driven sequence. Each must be called on the right device.
func resetMultiTurn(availableDevices []*MasterDevice) error {
	for _, dev := range availableDevices {
		if dev == nil || dev.Driver == nil {
			continue
		}
		if err := dev.Driver.ResetMultiTurn([]*MasterDevice{dev}); err != nil {
			return err
		}
	}
	return nil
}

// triggerMultiTurnResetAsync executes the multiturn reset via async SDO while PDO runs.
// Arms 3 steps (0x4D01:00=0x0031, 0x4D00:01 rising then falling edge) with EBUSY retry.
// pdoMTResetActive disables servo during reset (drive requires servo-off to execute).
func triggerMultiTurnResetAsync(availableDevices []*MasterDevice) error {
	const (
		pollInterval       = 2 * time.Millisecond    // between state polls
		armRetryInitDelay  = 5 * time.Millisecond    // initial backoff on first EBUSY
		armRetryMaxDelay   = 160 * time.Millisecond  // backoff ceiling (doubles each retry)
		armRetryMax        = 10                       // max retries on -EBUSY before giving up
		stepTimeout        = 2000 * time.Millisecond // per-step ACK timeout (was 600ms — increased for trigger step)
		interStepDelay     = 200 * time.Millisecond  // wait between steps — A6 needs time to process func select
		disableTimeout     = 2000 * time.Millisecond // time for drive to reach Switched On
		enableTimeout      = 500 * time.Millisecond  // time for drive to re-reach Op Enabled
	)

	// armStep arms one SDO request, retrying on -EBUSY with exponential backoff.
	//
	// WHY EXPONENTIAL BACKOFF over the previous flat 5ms delay:
	//   A flat retry delay works for a lightly loaded bus, but in high-traffic
	//   EtherCAT environments multiple CoE mailbox transactions can be in-flight
	//   simultaneously. After step 1 ACK the mailbox enters a "releasing" state
	//   that may last several bus cycles — 5ms flat was sometimes not enough,
	//   causing a false "mailbox still busy" abort of the entire reset sequence.
	//
	//   Exponential backoff (5ms → 10ms → 20ms → ... capped at 160ms) lets
	//   the retry adapt to actual contention: low-load cases resolve on the first
	//   retry at 5ms (no extra latency), high-load cases back off gracefully
	//   instead of hammering the mailbox and always timing out at the same wall
	//   clock moment.
	//
	//   The 160ms cap and armRetryMax=10 give a worst-case arming time of
	//   10 × 160ms = 1600ms. This is intentionally separate from — and may
	//   exceed — the 600ms stepTimeout, which applies only to waitForStep
	//   (the ACK-wait phase). The two phases are sequential: arm first, then
	//   wait for drive ACK. Total worst-case per step = 1600ms + 600ms = 2200ms.
	//   For a commissioning-only operation (multiturn reset) this is acceptable.
	//   Under a healthy bus, EBUSY resolves in 1–3 retries at 5–10ms total.
	armStep := func(slavePos int, step int, value uint32, desc string) error {
		backoff := armRetryInitDelay
		for i := 0; i < armRetryMax; i++ {
			rc := int(C.trigger_mt_request_step(C.int(slavePos), C.int(step), C.uint32_t(value)))
			if rc == 0 {
				if i > 0 {
					logger.Info(fmt.Sprintf("[MT-ASYNC] %s: armed after %d retries", desc, i))
				}
				return nil
			}
			if rc == -16 { // EBUSY = 16 on Linux
				logger.Info(fmt.Sprintf("[MT-ASYNC] %s: mailbox busy, retry %d/%d, backoff=%v",
					desc, i+1, armRetryMax, backoff))
				time.Sleep(backoff)
				// Double the delay for next retry, capped at armRetryMaxDelay.
				backoff *= 2
				if backoff > armRetryMaxDelay {
					backoff = armRetryMaxDelay
				}
				continue
			}
			// Any other error (EINVAL = NULL request, etc.) is fatal — stop immediately.
			err := fmt.Errorf("[MT-ASYNC] %s: arm failed rc=%d (not retryable)", desc, rc)
			logger.Error(err.Error())
			return err
		}
		err := fmt.Errorf("[MT-ASYNC] %s: mailbox still busy after %d retries (final backoff=%v)",
			desc, armRetryMax, backoff)
		logger.Error(err.Error())
		return err
	}

	// waitForStep polls until the drive ACKs (SUCCESS) or errors/times-out.
	waitForStep := func(slavePos int, step int, desc string) error {
		deadline := time.Now().Add(stepTimeout)
		for time.Now().Before(deadline) {
			state := int(C.get_mt_request_state(C.int(slavePos), C.int(step)))
			if state == 1 {
				logger.Info("[MT-ASYNC]", desc, "— ACK received")
				return nil
			}
			if state == -1 {
				err := fmt.Errorf("[MT-ASYNC] %s: drive returned SDO ERROR (object may not support CoE mailbox write)", desc)
				logger.Error(err.Error())
				return err
			}
			time.Sleep(pollInterval)
		}
		err := fmt.Errorf("[MT-ASYNC] %s: timeout after %v", desc, stepTimeout)
		logger.Error(err.Error())
		return err
	}

	// CiA-402 state helpers
	// isServoOff: drive has left Operation Enabled and servo power is removed.
	// We accept Ready To Switch On (0x0021) OR Switched On (0x0023) — both have
	// servo de-energized. The Panasonic A6 special function requires SRV-OFF.
	// CW=0x0006 (Shutdown) transitions: Op Enabled → Ready To Switch On (0x0021).
	// isServoOff and isOpEnabled are defined inside the loop (see below)
	// so they close over d and read from the correct device's PDO state.

	for _, d := range availableDevices {
		if d == nil {
			continue
		}

		// Per-device state helpers — close over d so they read the right device.
		isServoOff := func() bool {
			sw := uint16(d.PDOStatus.Load()&0xFFFF) & 0x006F
			return sw == 0x0021 || sw == 0x0023
		}
		isOpEnabled := func() bool {
			return (uint16(d.PDOStatus.Load()&0xFFFF) & 0x006F) == 0x0027
		}

		if !d.MTSdoReady {
			err := fmt.Errorf("[MT-ASYNC] MTSdoReady=false for %s — create_mt_sdo_requests() failed at setup", d.Name)
			logger.Error(err.Error())
			return err
		}

		logger.Info("[MT-ASYNC] Starting multiturn reset for drive:", d.Name,
			"sw=", fmt.Sprintf("0x%04X", uint16(d.PDOStatus.Load()&0xFFFF)))

		// ── Phase 1: Stop motion, disable servo ────────────────────────────
		// The Panasonic A6 accepts 0x4D00/0x4D01 SDO writes in ANY CiA-402
		// state and returns SUCCESS — but only EXECUTES the special function
		// when servo power is removed (Switched On state, CW=0x0007).
		// The cyclic standby branch respects pdoMTResetActive and writes 0x0007.
		d.EnableJogPDO(false)
		d.EnablePosPDO(false)
		d.desiredTargetVelocity.Store(0)
		_ = d.SetTargetPositionPDO(d.PDOPos.Load())
		d.pdoMTResetActive.Store(true)
		logger.Info("[MT-ASYNC] Servo disable requested (CW→0x0007 Switched On). Waiting for servo-off...")

		disableDeadline := time.Now().Add(disableTimeout)
		for !isServoOff() {
			if time.Now().After(disableDeadline) {
				d.pdoMTResetActive.Store(false)
				err := fmt.Errorf("[MT-ASYNC] timeout waiting for servo-off state (Ready To Switch On / Switched On) sw=0x%04X — aborting", uint16(d.PDOStatus.Load()&0xFFFF))
				logger.Error(err.Error())
				return err
			}
			time.Sleep(pollInterval)
		}
		logger.Info("[MT-ASYNC] Servo is OFF sw=", fmt.Sprintf("0x%04X", uint16(d.PDOStatus.Load()&0xFFFF)),
			"— executing special function sequence")

		// ── Phase 2: Execute 3-step SDO sequence ───────────────────────────
		// Step 1: select special function "multi-turn data reset"
		if err := armStep(d.Position, 0, 0x0031, "0x4D01:00=0x0031 (func select)"); err != nil {
			d.pdoMTResetActive.Store(false)
			return err
		}
		if err := waitForStep(d.Position, 0, "0x4D01:00=0x0031 (func select)"); err != nil {
			d.pdoMTResetActive.Store(false)
			return err
		}
		// A6 Minas requires settle time after function code selection before
		// it will accept the trigger write on 0x4D00:01. 5ms was too short —
		// the drive returned SDO ERROR immediately on step 1.
		time.Sleep(interStepDelay)

		// Step 2: rising edge — execute the reset
		if err := armStep(d.Position, 1, 0x00000200, "0x4D00:01=0x200 (trigger)"); err != nil {
			d.pdoMTResetActive.Store(false)
			return err
		}
		if err := waitForStep(d.Position, 1, "0x4D00:01=0x200 (trigger)"); err != nil {
			d.pdoMTResetActive.Store(false)
			return err
		}
		// Same settle requirement between trigger and clear.
		time.Sleep(interStepDelay)

		// Step 3: falling edge — clear trigger
		if err := armStep(d.Position, 1, 0x00000000, "0x4D00:01=0x000 (clear)"); err != nil {
			d.pdoMTResetActive.Store(false)
			return err
		}
		if err := waitForStep(d.Position, 1, "0x4D00:01=0x000 (clear)"); err != nil {
			d.pdoMTResetActive.Store(false)
			return err
		}

		// ── Phase 3: Re-enable servo ────────────────────────────────────────
		d.pdoMTResetActive.Store(false)
		logger.Info("[MT-ASYNC] Servo re-enabling (CW→0x000F). Waiting for Op Enabled...")

		enableDeadline := time.Now().Add(enableTimeout)
		for !isOpEnabled() {
			if time.Now().After(enableDeadline) {
				logger.Warn("[MT-ASYNC] timeout waiting for Op Enabled after reset — CiA-402 will recover on its own")
				break
			}
			time.Sleep(pollInterval)
		}

		logger.Info("[MT-ASYNC] Multiturn reset complete for drive:", d.Name,
			"sw=", fmt.Sprintf("0x%04X", uint16(d.PDOStatus.Load()&0xFFFF)),
			"pos=", d.PDOPos.Load())
	}
	return nil
}

// triggerMultiTurnResetSDO executes the multiturn reset via blocking SDO.
// ONLY valid before ecrt_master_activate() (pre-activation commissioning).
func triggerMultiTurnResetSDO(availableDevices []*MasterDevice) error {
	for _, device := range availableDevices {
		if device == nil {
			continue
		}
		operation, err := GetEtherCATOperation("resetMultiTurn", device.Device.AddressConfigName)
		if err != nil {
			return fmt.Errorf("triggerMultiTurnResetSDO: no config for device %s: %w", device.Name, err)
		}
		logger.Info("[SDO] triggerMultiTurnResetSDO: executing for drive:", device.Name)
		for _, step := range operation.Steps {
			if step.Action == "read" {
				val, _ := SDOUpload2(device.Master, device.Position, step)
				logger.Debug("[SDO] resetMultiTurn read:", step.Name, "val:", val)
			} else {
				if err := SDODownload(device.Master, device.Position, step); err != nil {
					logger.Warn("[SDO] resetMultiTurn write failed:", step.Name, err)
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		logger.Info("[SDO] triggerMultiTurnResetSDO complete for:", device.Name)
	}
	return nil
}