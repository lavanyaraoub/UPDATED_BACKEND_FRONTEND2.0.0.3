package motordriver

import (
	"fmt"
	"sync/atomic"
	"time"

	channels "EtherCAT/channels"
	"EtherCAT/logger"
	"EtherCAT/motordriver/statusnotifier"
)

/*
resetSystemWorker is the single goroutine that handles all system resets.
It reads from channels.ResetDriverSystem and executes the full reset sequence.

RESET SEQUENCE — order is critical for IgH EtherCAT correctness:

  PHASE 1 — Stop motion and listeners (PDO cyclic still running)
    a. stopECSCheck             — unblock any goroutine waiting in ECS loop
    b. stopDriverPolling        — stop 50ms position-poll goroutines
    c. stopPollIOStat           — stop digital-input poll goroutines
    d. stopErrorPolling         — stop error-code poller (no-op when PDO active)
    e. stopDriverActionListener — signal action listener goroutine to exit
    f. stopDriveStatusListener  — signal status keeper goroutine to exit

  PHASE 2 — Guard: abort reset if PDO is not active (no safe SDO fallback)
    If IsPDOActive() == false →log error, re-start listeners, continue.

  PHASE 3 — Fault reset via PDO (cyclic MUST still be running)
    g. PDOFaultResetdis able jog/pos modes, clear drive fault via CiA-402
                       state machine. Cyclic is alive to process controlwords.
    h. Sleep 500ms  allow state machine to settle to Operation Enabled.

  PHASE 4 Restart listeners
    i. pollDrivePosition / pollIOStat / initDriverActionListener /
       startDriverStatusListener

  PDO IS MANDATORY THERE IS NO SDO FALLBACK AT RUNTIME:
    IgH EtherCAT has no ecrt_master_deactivate(). After ecrt_master_activate()
    the master owns the bus. SDO mailbox responses require ecrt_master_receive()
    to be running in the cyclic task. Stopping the cyclic before SDO calls
    causes those calls to block forever. If PDO failed to start during InitMaster,
    the process must be restarted no safe SDO recovery path exists at runtime.
*/

var systemResetInProgress atomic.Bool

type resetHardwareLimitState struct {
	pot       bool
	not       bool
	available bool
}

func listenSystemReset() {
	// A reset request must never block the socket/GPIO/action goroutine.
	channels.ResetDriverSystem = make(chan bool, 1)
	go resetSystemWorker()
}

func performSysReset(checkMotorRunning bool) {
	if checkMotorRunning {
		for _, dev := range masterDevices {
			stat := getCurrentDriverStatus(dev.Name)
			if stat.isMotorRunning {
				logger.Info("Unable to do system reset, motor", dev.Name, "is running")
				statusnotifier.Alarm("Unable to do system reset, motor " + dev.Name + " is running")
				statusnotifier.SocketMessage("reset_done", "reset completed")
				return
			}
		}
	}
	if systemResetInProgress.Load() {
		logger.Info("System reset already in progress, dropping duplicate request")
		return
	}
	select {
	case channels.ResetDriverSystem <- true:
	default:
		logger.Info("System reset already queued, dropping duplicate request")
	}
}

func resetSystemWorker() {
	for {
		msg := <-channels.ResetDriverSystem
		if !msg {
			// Stop the worker goroutine. Nothing in production code sends
			// false on this channel today — InitMaster starts this worker
			// once and it runs for the life of the process — so this path
			// only matters for tests that spin up a throwaway instance and
			// need a clean way to shut it down afterward.
			logger.Debug("stopping reset system worker")
			return
		}

		systemResetInProgress.Store(true)
		stopRequested := runSystemReset()
		systemResetInProgress.Store(false)
		if stopRequested {
			logger.Debug("stopping reset system worker")
			return
		}
	}
}

// runSystemReset performs one reset. Its return value reports whether a false
// worker-stop message was received while duplicate reset requests were drained.
func runSystemReset() (stopRequested bool) {
	logger.Info("resetting driver system....")

	// Capture hardware limits before stopping listeners or cycling the servo.
	// The A6 input word can briefly look inactive during CiA-402 recovery even
	// while the physical NOT/POT switch remains held.
	preResetLimits := make(map[*MasterDevice]resetHardwareLimitState, len(masterDevices))
	for _, dev := range masterDevices {
		if dev == nil {
			continue
		}
		pot, not, available := readHardwareLimitState(dev)
		preResetLimits[dev] = resetHardwareLimitState{pot: pot, not: not, available: available}
		logger.Info(fmt.Sprintf("[RESET-LIMIT] pre-reset: %s di=0x%08X available=%v POT=%v NOT=%v",
			dev.Name, uint32(dev.PDODI.Load()), available, pot, not))
	}

	// Stop every producer before rebuilding the listener channels.
	stopECSCheck()
	stopDriverPolling()
	stopDriverActionListener()
	waitForFlagFalse(&isActionListening, 2*time.Second, "listenDriverAction")
	stopDriveStatusListener()
	waitForFlagFalse(&isStatusListening, 2*time.Second, "listenDriverStatus")
	stopErrorPolling()
	stopPollIOStat()
	doneDriverAction()

	// "reset" alone does not set StopExecution, so a program can retain runMu
	// after reset_done. Stop it first, then clear its resume state.
	channels.WriteCommandExecInput("stop_prog_exec", "")
	channels.WriteCommandExecInput("reset", "")
	channels.NotifyCmdComplete()

	if !IsPDOActive() {
		logger.Error("[RESET] FATAL: PDO cyclic is not active — restart required")
		statusnotifier.Alarm("Reset failed: EtherCAT PDO not active — restart required")
		statusnotifier.SocketMessage("reset_done", "reset failed")
		restartResetListeners()
		return false
	}

	// Clear state left behind by emergency/quick-stop before recovering the
	// CiA-402 state machine. Otherwise the next move can remain cancelled or
	// the PDO loop can keep requesting Profile Velocity mode.
	for _, dev := range masterDevices {
		if dev == nil {
			continue
		}
		dev.posMoveAborted.Store(false)
		dev.desiredOpMode.Store(0)
	}

	// A plain fault reset only touches drives with statusword bit 3 set. The
	// servo cycle also recovers non-faulted drives stuck after jog/quick-stop.
	forceServoCycle(masterDevices)
	resetOK := PDOFaultReset(masterDevices)

	// Direct channel writers can still queue duplicates; discard true requests.
	// Preserve a false test/shutdown message rather than accidentally swallowing it.
	for {
		select {
		case request := <-channels.ResetDriverSystem:
			if !request {
				stopRequested = true
			}
		default:
			goto drained
		}
	}

drained:
	time.Sleep(100 * time.Millisecond)
	restartResetListeners()

	// Clear latched POT/NOT and transient motion state only after the status
	// listener is running again; notifyDriverStatus is otherwise a no-op.
	for _, dev := range masterDevices {
		if dev != nil {
			notifyDriverStatus("reset", "", dev)
		}
	}
	pollDriveError(masterDevices)

	allOperational := true
	postResetAlarm := ""
	for _, dev := range masterDevices {
		if dev == nil {
			continue
		}
		sw := uint16(dev.PDOStatus.Load() & 0xFFFF)
		logger.Info(fmt.Sprintf("[RESET] post-reset check: %s sw=0x%04X pds=0x%02X",
			dev.Name, sw, sw&0x006F))
		if sw&0x0008 != 0 {
			allOperational = false
			errorCode := int(dev.PDOErr.Load() & 0xFFFF)
			if errorCode != 0 {
				postResetAlarm = FormatErrorCode(errorCode)
				statusnotifier.DriverError(errorCode)
			} else {
				postResetAlarm = fmt.Sprintf("Drive fault persists after reset (sw=0x%04X)", sw)
			}
		} else if sw&0x006F != 0x0027 {
			allOperational = false
			postResetAlarm = fmt.Sprintf("Drive not operational after reset (sw=0x%04X)", sw)
		} else {
			pot, not, available := readHardwareLimitState(dev)
			if before, ok := preResetLimits[dev]; ok && before.available {
				available = true
				pot = pot || before.pot
				not = not || before.not
			}
			logger.Info(fmt.Sprintf("[RESET-LIMIT] post-reset: %s di=0x%08X available=%v POT=%v NOT=%v",
				dev.Name, uint32(dev.PDODI.Load()), available, pot, not))

			if !available || (!pot && !not) {
				continue
			}

			allOperational = false
			switch {
			case pot && not:
				notifyDriverStatus("pot_not_exceeded", "POT", dev)
				postResetAlarm = "POT and NOT Limit Exceeded"
			case pot:
				notifyDriverStatus("pot_not_exceeded", "POT", dev)
				postResetAlarm = "POT Limit Exceeded"
			case not:
				notifyDriverStatus("pot_not_exceeded", "NOT", dev)
				postResetAlarm = "NOT Limit Exceeded"
			}
		}
	}

	logger.Info("resetting driver completed....")
	if !resetOK || !allOperational {
		if allOperational {
			postResetAlarm = "Drive fault could not be cleared — check alarm code and try again"
		}
		// reset_done may clear the UI's local alarm; reassert the still-active
		// hardware/fault alarm afterwards so it remains authoritative.
		statusnotifier.SocketMessage("reset_done", "reset failed")
		statusnotifier.Alarm(postResetAlarm)
	} else {
		statusnotifier.SocketMessage("reset_done", "reset completed")
		statusnotifier.Alarm("No Alarms")
	}
	return stopRequested
}

// readHardwareLimitState mirrors the live PDO input mapping used by each
// driver's I/O listener.
func readHardwareLimitState(device *MasterDevice) (pot, not, available bool) {
	if device == nil || !device.Device.StopWhenHWPOTNOT || !device.PdoDIReady {
		return false, false, false
	}
	di := uint32(device.PDODI.Load())
	switch device.Driver.(type) {
	case A6Minas, *A6Minas:
		return (di & (1 << 1)) == 0, (di & (1 << 2)) == 0, true
	default:
		// Delta, Nidec and Veichi use active-high 0x60FD:
		// bit 1=POT and bit 0=NOT.
		return (di & (1 << 1)) != 0, (di & (1 << 0)) != 0, true
	}
}

func restartResetListeners() {
	startDriverStatusListener()
	initDriverActionListener()
	pollDrivePosition(masterDevices)
	pollIOStat(masterDevices)
}

// StopSystem stops all listeners and powers off drives cleanly.
func StopSystem() {
	if !HasDriverConnected() {
		return
	}
	stopECSCheck()
	stopDriverPolling()
	stopPollIOStat()
	stopDriverActionListener()
	stopDriveStatusListener()
	stopErrorPolling()
	channels.WriteCommandExecInput("reset", "")
	channels.NotifyCmdComplete()
	// ShutdownMasters implements the 3-phase PDO shutdown:
	// arm pdoShutdownActive → poll PDS safe → StopPDOCyclic + ecrt_release_master.
	// This is the only correct way to stop without triggering Err88.2.
	ShutdownMasters()
}