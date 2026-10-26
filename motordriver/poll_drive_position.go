package motordriver

/**
Functions in this file poll the status of the driver: position and safety limits.

Key fixes vs the old version:
 1. masterDevices is now []*MasterDevice, so device.PdoReady is read from the
    SAME struct that SetupPDOPosition wrote — not a stale value copy.
 2. Each goroutine gets its own stop channel so StopDriverPolling() reliably
    stops ALL goroutines, not just one random one.
 3. checkPotNotLimit guard condition fixed: OR → AND.
 4. checkPotNotLimit safety stop uses the triggering device, not masterDevices[0].
**/
import (
	channels "EtherCAT/channels"
	logger "EtherCAT/logger"
	"EtherCAT/motordriver/statusnotifier"
	notifier "EtherCAT/motordriver/statusnotifier"
	"EtherCAT/settings"
	"sync"
	"sync/atomic"
	"time"
)

// stopChansMu guards stopChans against concurrent close/range races during reset.
var stopChansMu sync.Mutex

// stopChans holds one channel per polling goroutine so every goroutine can be
// stopped independently and reliably.
var stopChans []chan struct{}

// pollingWG lets stopDriverPolling confirm every goroutine has actually
// returned, not just that the stop channel was closed. Closing a channel is
// an instant signal; the goroutine noticing it and returning is not — without
// this, code that restarts polling immediately after stopDriverPolling() can
// race with a goroutine that hasn't unwound yet.
var pollingWG sync.WaitGroup

// waitWithTimeout blocks until wg.Wait() returns or timeout elapses,
// whichever comes first. A bare wg.Wait() is dangerous here: if the
// goroutine(s) being waited on can block on something outside this
// function's control (e.g. an unbuffered channel send with no guaranteed
// receiver), an unbounded Wait() turns a pre-existing, previously-harmless
// stuck goroutine into a permanent deadlock for the caller. Timing out and
// logging instead keeps the common case (clean, fast exit) fully
// synchronized while degrading a stuck goroutine to a visible warning
// rather than a hang.
func waitWithTimeout(wg *sync.WaitGroup, timeout time.Duration, label string) {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		logger.Warn("[SHUTDOWN]", label, "did not exit within", timeout, "— continuing anyway; goroutine may still be running")
	}
}

// waitForFlagFalse polls an atomic.Bool (e.g. isStatusListening,
// isActionListening) until it goes false or timeout elapses, whichever
// comes first. Used to confirm a listener goroutine has actually exited
// after its stop function fires — those stop functions only send an exit
// signal and return immediately, they don't wait for the goroutine to
// process it. Bounded so a stuck goroutine degrades to a logged warning
// instead of blocking the caller forever.
func waitForFlagFalse(flag *atomic.Bool, timeout time.Duration, label string) {
	deadline := time.Now().Add(timeout)
	for flag.Load() {
		if time.Now().After(deadline) {
			logger.Warn("[SHUTDOWN]", label, "did not exit within", timeout, "— continuing anyway; goroutine may still be running")
			return
		}
		time.Sleep(1 * time.Millisecond)
	}
	// Small grace period: the flag goes false immediately before the
	// goroutine's return statement, not after — there's a brief window
	// where the goroutine hasn't actually finished unwinding yet even
	// though the flag already reads false. Confirmed necessary in practice
	// (see TestPowerOffAll_PDOActive_StopsAllDevices).
	time.Sleep(50 * time.Millisecond)
}

// pollDrivePosition starts one position-polling goroutine per device.
// devices is []*MasterDevice so goroutines always see the live PdoReady flag.
func pollDrivePosition(devices []*MasterDevice) error {
	logger.Debug("starting driver position listener")
	stopChansMu.Lock()
	stopChans = make([]chan struct{}, 0, len(devices))
	stopChansMu.Unlock()
	for _, device := range devices {
		ch := make(chan struct{})
		stopChansMu.Lock()
		stopChans = append(stopChans, ch)
		stopChansMu.Unlock()
		pollingWG.Add(1)
		go pollDrivePositionProcess(device, ch)
	}
	return nil
}

// stopDriverPolling stops ALL position-polling goroutines and blocks until
// they have actually exited (up to a bounded timeout — see waitWithTimeout).
func stopDriverPolling() {
	stopChansMu.Lock()
	for _, ch := range stopChans {
		close(ch)
	}
	stopChans = nil
	stopChansMu.Unlock()
	waitWithTimeout(&pollingWG, 2*time.Second, "pollDrivePosition")
}

// pollDrivePositionProcess is the per-device polling goroutine.
//
// It receives a *MasterDevice pointer so it always reads the flags and offsets
// that were written by SetupPDOPosition, even if those were written after the
// goroutine was created (which cannot happen with the new init order, but the
// pointer guarantees correctness regardless).
//
// UI THROTTLE: Position updates are sent to the UI at most every 50ms, not
// on every poll tick. At a 1ms PDO cycle the raw position changes 1000x/second;
// emitting a Socket.io event on every change saturates the browser event loop
// on low-power handheld devices and floods the Go event scheduler. 50ms gives
// a smooth 20 FPS display update rate, which is more than sufficient for an
// operator display and cuts UI load by ~98%.
func pollDrivePositionProcess(device *MasterDevice, stop <-chan struct{}) {
	defer pollingWG.Done()
	logger.Info("polling position of driver:", device.Name)

	const uiNotifyInterval = 30 * time.Millisecond
	lastUINotify := time.Time{} // zero value ensures first iteration always notifies

	for {
		select {
		case <-stop:
			logger.Debug("stopping driver position listener for:", device.Name)
			return
		default:
		}

		var rawPulses int32

		// Read position: prefer PDO buffer (updated every 10 ms by cyclic task).
		// Fall back to SDO only when PDO was not successfully configured.
		if device.PdoReady && IsPDOActive() {
			rawPulses = device.PDOPos.Load()
		} else {
			operation, opErr := GetEtherCATOperation("pollstatus", device.Device.AddressConfigName)
			if opErr == nil && len(operation.Steps) > 0 {
				sdoVal, _ := DrivePosition(device.Master, device.Position, operation.Steps[0])
				rawPulses = sdoVal
			}
		}

		driverStatus := getCurrentDriverStatus(device.Name)

		// Convert raw encoder counts to degrees.
		curPos, posWithErrCorrection := currentPosition(rawPulses, device.Device.DriveXRatio, device.Name)

		// Apply pitch error, backlash and work offset for the UI display value.
		pitchError := getPitchError(device.Name, driverStatus.destinationPosition)
		displayPos := (posWithErrCorrection - pitchError) + driverStatus.backlash - driverStatus.workOffset

		// Normalise to [0, 360).
		if displayPos >= 359.999 {
			displayPos = 0
		}

		// Notify UI with the display-corrected position — throttled to 50ms.
		// The PDO buffer updates every 1ms but the browser does not need 1000 fps.
		if time.Since(lastUINotify) >= uiNotifyInterval {
			notifier.NotifyCurrentPosition(device.Name, displayPos)
			lastUINotify = time.Now()
		}

		// Update internal status keeper with the raw degree position.
		currentDriverPosition(device, curPos)

		// Software safety limit check.
		driverSettings := settings.GetDriverSettings(device.Name)
		checkPotNotLimit(curPos, device, driverSettings, driverStatus)

		// 50 ms is plenty when PDO already updates the buffer at 10 ms.
		time.Sleep(50 * time.Millisecond)
	}
}

// checkPotNotLimit checks software POT/NOT limits and triggers an emergency stop
// if the motor has exceeded them.
//
// Fixes vs old version:
//   - Guard condition: was (NOT >= 0 || POT <= 0) which skipped checking whenever
//     either limit was zero. Corrected to (NOT >= 0 && POT <= 0) so both limits
//     must be unset before skipping.
//   - Emergency stop: was masterDevices[0] (always device 0). Now uses the
//     triggering device pointer so multi-drive systems work correctly.
func checkPotNotLimit(
	currentPos float64,
	device *MasterDevice,
	driverSettings settings.DriverSettings,
	driverStatus driverCurrentStatus,
) (exceeded bool) {
	driverStatus = getCurrentDriverStatus(device.Name)

	// Both limits unset (zero) — nothing to check.
	if driverSettings.NOT >= 0 && driverSettings.POT <= 0 {
		return false
	}

	threshold := device.Device.PotNotThreshold
	pot := float64(driverSettings.POT)
	not := 360.0 + float64(driverSettings.NOT) // NOT is stored as a negative angle

	// Limit was already flagged in a previous cycle. Keep the latch active until
	// the axis is physically back inside the safe zone; changing direction alone
	// is not enough, because the next poll would immediately trip the same limit
	// again while the table is still outside the boundary.
	if driverStatus.potNotExceeded {
		if driverStatus.potExceeded && pot > 0 && currentPos < (pot-threshold) {
			driverStatus.potNotExceeded = false
			driverStatus.potExceeded = false
			driverStatus.notExceeded = false
			setCurrentDriverStatus(device.Name, driverStatus)
		} else if driverStatus.notExceeded && driverSettings.NOT != 0 && currentPos > (not+threshold) {
			driverStatus.potNotExceeded = false
			driverStatus.potExceeded = false
			driverStatus.notExceeded = false
			setCurrentDriverStatus(device.Name, driverStatus)
		} else if !isMotionBlockedByPotNotLimit(driverStatus, driverStatus.direction) {
			return false
		} else {
			if driverStatus.potExceeded {
				statusnotifier.Alarm("POT Limit Exceeded")
			} else {
				statusnotifier.Alarm("NOT Limit Exceeded")
			}
			return true
		}
	}

	// POT check (positive / clockwise direction)
	if driverStatus.direction == 1 && pot > 0 {
		if currentPos >= (pot-threshold) && currentPos <= pot+(threshold*10) {
			logger.Error("Software POT limit exceeded at", currentPos, "limit:", pot)
			driverStatus.potNotExceeded = true
			driverStatus.potExceeded = true
			driverStatus.notExceeded = false
			setCurrentDriverStatus(device.Name, driverStatus)
			FastPowerOff(device)
			StopJog(device)
			channels.WriteCommandExecInput("stop_prog_exec", "")
			statusnotifier.Alarm("POT Limit Exceeded")
			notifyDriverStatus("pot_not_exceeded", "POT", device)
			return true
		}
	}

	// NOT check (negative / counter-clockwise direction)
	// BUG FIX: The old guard was (not > 0) where not = 360.0 + driverSettings.NOT.
	// When NOT is unconfigured (= 0 in the UI), not = 360.0, which is always > 0,
	// so the check fired at ~360° — a completely normal wrap-around position.
	// This caused a false emergency stop every time the motor passed 360° while
	// jogging counter-clockwise, even when no NOT limit was configured by the operator.
	//
	// FIX: Guard on driverSettings.NOT != 0 (raw UI value) instead of the
	// computed not > 0, exactly matching the POT side which correctly uses pot > 0
	// (and pot is the raw UI value, not shifted by 360).
	if driverStatus.direction == -1 && driverSettings.NOT != 0 {
		if currentPos <= (not+threshold) && currentPos >= not-(threshold*10) {
			logger.Error("Software NOT limit exceeded at", currentPos, "limit:", not)
			driverStatus.potNotExceeded = true
			driverStatus.potExceeded = false
			driverStatus.notExceeded = true
			setCurrentDriverStatus(device.Name, driverStatus)
			FastPowerOff(device)
			StopJog(device)
			channels.WriteCommandExecInput("stop_prog_exec", "")
			statusnotifier.Alarm("NOT Limit Exceeded")
			notifyDriverStatus("pot_not_exceeded", "NOT", device)
			return true
		}
	}

	return false
}
