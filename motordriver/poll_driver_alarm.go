package motordriver

// poll_driver_alarm.go
//
// Responsibilities:
//  1. Error code lookup  — decodes raw 0x603F codes into human-readable strings
//                          using configs/error_definition.txt (loaded once, lazily).
//  2. Fault history      — 20-entry ring buffer persisted to disk so faults survive
//                          process restarts and Pi reboots.
//  3. Error polling      — polls 0x603F via PDO buffer (preferred) or SDO fallback,
//                          fires DriverError() on rising-edge changes only.

import (
	logger "EtherCAT/logger"
	"EtherCAT/motordriver/statusnotifier"
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ============================================================
// Section 1: Error code lookup
// ============================================================
// WHY: Previously DriverError(87) showed "Driver Error 87" in the UI with no
// context. configs/error_definition.txt already contained the mapping
// (87 → "Hardware Emergency pressed") but was never loaded at runtime.
// These helpers wire that config into every alarm log and UI notification
// so operators see "Error 87: Hardware Emergency pressed" immediately.

const errorDefinitionFile = "configs/error_definition.txt"
const (
	errCodeCleared      = -1
	errCodeUnknownFault = -2
)

var (
	errorDescriptions map[int]string
	errorDescOnce     sync.Once
)

// loadErrorDescriptions reads error_definition.txt once and populates the map.
// Lines beginning with "//" after trimming are treated as comments and skipped.
// Duplicate keys are last-write-wins (the file has a few intentional duplicates).
func loadErrorDescriptions() {
	errorDescriptions = make(map[int]string)
	f, err := os.Open(errorDefinitionFile)
	if err != nil {
		logger.Warn("[ErrorLookup] Could not open", errorDefinitionFile, ":", err,
			"— error codes will display as raw numbers")
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	count := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		code, convErr := strconv.Atoi(strings.TrimSpace(parts[0]))
		if convErr != nil {
			continue
		}
		errorDescriptions[code] = strings.TrimSpace(parts[1])
		count++
	}
	logger.Info("[ErrorLookup] Loaded", count, "error code definitions from", errorDefinitionFile)
}

// normalizeErrorCode strips the M700 vendor prefix from raw 0x603F values.
//
// The Nidec M700 encodes its alarm IDs as 0xFF00|alarmID (e.g. alarm 1 is
// reported as 0xFF01 = 65281, alarm 87 as 0xFF57 = 65367). The
// error_definition.txt file stores the decoded alarm IDs (1, 87, …).
// Without normalisation, LookupErrorDescription(65281) always misses and
// every M700 fault shows "Unknown error code 65281".
//
// Rule: if the high byte is 0xFF the low byte is the real alarm ID.
// Other drives (A6 Minas, Delta ASDA) use plain codes (≤ 255, high byte = 0),
// so they pass through unchanged.
func normalizeErrorCode(code int) int {
	if code>>8 == 0xFF {
		return code & 0xFF
	}
	return code
}

// LookupErrorDescription returns the human-readable description for a CiA-402
// error code (0x603F value). Thread-safe: map is written once before any read.
// Raw M700 codes (0xFF00|n) are normalised to their alarm ID before lookup.
func LookupErrorDescription(code int) string {
	errorDescOnce.Do(loadErrorDescriptions)
	normalized := normalizeErrorCode(code)
	if desc, ok := errorDescriptions[normalized]; ok {
		return desc
	}
	return fmt.Sprintf("Unknown error code %d (not in error_definition.txt)", normalized)
}

// FormatErrorCode returns a display string combining the decoded alarm ID and
// its description. Raw M700 vendor-prefixed codes are normalised first.
// Example output: "Error 87: Hardware Emergency pressed"
func FormatErrorCode(code int) string {
	return fmt.Sprintf("Error %d: %s", normalizeErrorCode(code), LookupErrorDescription(code))
}

// ============================================================
// Section 2: Fault history ring buffer
// ============================================================
// WHY: After PDOFaultReset() the error atomic was cleared to 0, wiping all
// fault evidence before a reboot. This ring buffer retains the last 20 fault
// events with full context (timestamp, error code, decoded description,
// statusword, device name) and persists them to disk so they survive restarts.

const (
	faultHistoryCapacity = 20
	// /var/tmp persists across reboots on most Pi setups.
	// Falls back gracefully if the path is not writable (log warning, keep going).
	faultHistoryFile = "/var/tmp/ethercat_fault_history.json"
)

// FaultEntry is one recorded fault event.
type FaultEntry struct {
	Time        time.Time `json:"time"`
	DeviceName  string    `json:"device_name"`
	ErrorCode   uint16    `json:"error_code"`
	Description string    `json:"description"`
	Statusword  uint16    `json:"statusword"`
}

var (
	faultHistoryMu sync.RWMutex
	faultRing      [faultHistoryCapacity]FaultEntry
	faultRingHead  int // index of the next write slot
	faultRingCount int // number of valid entries (≤ capacity)
)

// RecordFault appends a new fault to the ring and persists to disk.
// Consecutive identical (device + errorCode) entries are deduplicated to
// prevent the ring filling up when the drive sits in fault state across
// multiple 100ms poll ticks.
// Thread-safe: uses faultHistoryMu.
func RecordFault(deviceName string, errorCode uint16, statusword uint16) {
	desc := LookupErrorDescription(int(errorCode))

	faultHistoryMu.Lock()
	// Skip if identical to the most recent entry.
	if faultRingCount > 0 {
		prev := faultRing[(faultRingHead-1+faultHistoryCapacity)%faultHistoryCapacity]
		if prev.DeviceName == deviceName && prev.ErrorCode == errorCode {
			faultHistoryMu.Unlock()
			return
		}
	}
	faultRing[faultRingHead] = FaultEntry{
		Time:        time.Now(),
		DeviceName:  deviceName,
		ErrorCode:   errorCode,
		Description: desc,
		Statusword:  statusword,
	}
	faultRingHead = (faultRingHead + 1) % faultHistoryCapacity
	if faultRingCount < faultHistoryCapacity {
		faultRingCount++
	}
	faultHistoryMu.Unlock()

	logger.Warn("[FaultHistory] Recorded:", FormatErrorCode(int(errorCode)),
		"device:", deviceName, "sw:", fmt.Sprintf("0x%04X", statusword))
	persistFaultHistory()
}

// GetFaultHistory returns all recorded faults in chronological order (oldest first).
// The returned slice is a copy; safe to read without holding any lock.
func GetFaultHistory() []FaultEntry {
	faultHistoryMu.RLock()
	defer faultHistoryMu.RUnlock()
	if faultRingCount == 0 {
		return nil
	}
	out := make([]FaultEntry, faultRingCount)
	start := (faultRingHead - faultRingCount + faultHistoryCapacity) % faultHistoryCapacity
	for i := 0; i < faultRingCount; i++ {
		out[i] = faultRing[(start+i)%faultHistoryCapacity]
	}
	return out
}

// ClearFaultHistory empties the in-memory ring and removes the on-disk file.
func ClearFaultHistory() {
	faultHistoryMu.Lock()
	faultRingHead = 0
	faultRingCount = 0
	faultHistoryMu.Unlock()
	_ = os.Remove(faultHistoryFile)
	logger.Info("[FaultHistory] Cleared")
}

// LoadFaultHistory restores the ring from disk on startup.
// Called at the top of InitMaster. No-op on first boot (file absent).
func LoadFaultHistory() {
	data, err := os.ReadFile(faultHistoryFile)
	if err != nil {
		return // not present on first boot — expected
	}
	var entries []FaultEntry
	if jsonErr := json.Unmarshal(data, &entries); jsonErr != nil {
		logger.Warn("[FaultHistory] Could not parse history file:", jsonErr)
		return
	}
	if len(entries) > faultHistoryCapacity {
		entries = entries[len(entries)-faultHistoryCapacity:]
	}
	faultHistoryMu.Lock()
	for i, e := range entries {
		faultRing[i] = e
	}
	faultRingCount = len(entries)
	faultRingHead = faultRingCount % faultHistoryCapacity
	faultHistoryMu.Unlock()
	logger.Info("[FaultHistory] Loaded", faultRingCount, "historical fault entries from disk")
}

// persistFaultHistory serialises the ring to disk after every RecordFault.
// Failures are logged but never propagated — fault recording must not fail
// just because the filesystem is full.
func persistFaultHistory() {
	entries := GetFaultHistory()
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		logger.Warn("[FaultHistory] Marshal error:", err)
		return
	}
	if writeErr := os.WriteFile(faultHistoryFile, data, 0644); writeErr != nil {
		logger.Warn("[FaultHistory] Could not persist to", faultHistoryFile, ":", writeErr)
	}
}

// ============================================================
// Section 3: Error polling goroutine
// ============================================================

// stopErrPollingChans holds one stop channel per pollDriveErrWorker goroutine.
// Phase 4 fix: single channel meant stopErrorPolling() could only stop ONE
// goroutine — the others kept running and could not be cleanly shut down on
// a reset cycle, leaking goroutines on multi-drive systems.
// Now each device gets its own buffered channel, matching how stopChans works
// in poll_drive_position.go.
var stopErrPollingChans []chan bool

// errPollingRunning must be atomic: written by resetSystemWorker goroutine,
// read by stopErrorPolling from the action-listener goroutine concurrently.
var errPollingRunning atomic.Bool

// errPollingWG lets stopErrorPolling confirm every pollDriveErrWorker
// goroutine has actually returned, not just that the stop signal was sent.
var errPollingWG sync.WaitGroup

// pollDriveError starts one error-polling goroutine per device.
func pollDriveError(avilableDevices []*MasterDevice) error {
	logger.Debug("starting driver error listener")
	if len(avilableDevices) <= 0 {
		return errors.New("no driver found")
	}
	// Create one buffered stop channel per device so stopErrorPolling()
	// can signal every goroutine independently. The old single-channel
	// approach meant only one goroutine received the stop — the others
	// kept running and leaked on each reset cycle.
	stopErrPollingChans = make([]chan bool, 0, len(avilableDevices))
	errPollingRunning.Store(true)
	for _, device := range avilableDevices {
		ch := make(chan bool, 1)
		stopErrPollingChans = append(stopErrPollingChans, ch)
		errPollingWG.Add(1)
		go pollDriveErrWorker(device, ch)
	}
	return nil
}

// notifySetupDriveFault publishes a fault found during pre-activation setup.
// This runs before PDO polling is active, so a hardware emergency that survives
// startup fault reset is visible immediately instead of waiting for the poller.
func notifySetupDriveFault(device *MasterDevice) {
	statusword, _ := readDriveStatuswordSDO(device)
	errCode, _ := readDriveErrorCodeSDO(device)
	publishDriveFault(device, errCode, statusword, "[STARTUP-SETUP-FAULT]")
}

// notifyStartupDriveFaults publishes the current drive fault state once after
// startup listener creation. This makes a pre-existing drive fault visible
// immediately after app start, without waiting for Reset or Zero Ref.
func notifyStartupDriveFaults(availableDevices []*MasterDevice) {
	if len(availableDevices) == 0 {
		return
	}

	go func() {
		time.Sleep(300 * time.Millisecond)

		for _, device := range availableDevices {
			if device == nil {
				continue
			}

			sw := uint16(device.PDOStatus.Load() & 0xFFFF)
			if sw&0x0008 == 0 {
				continue
			}

			errCode := 0
			if device.PdoErrorReady {
				errCode = int(device.PDOErr.Load() & 0xFFFF)
			}

			if errCode != 0 {
				publishDriveFault(device, errCode, sw, "[STARTUP-FAULT]")
			} else {
				publishDriveFault(device, 0, sw, "[STARTUP-FAULT]")
			}
		}
	}()
}

func readDriveStatuswordSDO(device *MasterDevice) (uint16, bool) {
	operation, err := GetEtherCATOperation("readStatusword", device.Device.AddressConfigName)
	if err != nil || len(operation.Steps) == 0 {
		return 0, false
	}
	statusword, sdoErr := SDOUpload2(device.Master, device.Position, operation.Steps[0])
	if sdoErr != nil {
		return 0, false
	}
	return uint16(statusword), true
}

func readDriveErrorCodeSDO(device *MasterDevice) (int, bool) {
	operation, err := GetEtherCATOperation("readError", device.Device.AddressConfigName)
	if err != nil || len(operation.Steps) == 0 {
		return 0, false
	}
	errCode, sdoErr := SDOUpload2(device.Master, device.Position, operation.Steps[0])
	if sdoErr != nil {
		return 0, false
	}
	return errCode, true
}

func publishDriveFault(device *MasterDevice, errCode int, statusword uint16, tag string) {
	if errCode != 0 {
		logger.Error(tag, FormatErrorCode(errCode),
			"sw=", fmt.Sprintf("0x%04X", statusword), "device:", device.Name)
		RecordFault(device.Name, uint16(errCode), statusword)
		statusnotifier.Alarm(FormatErrorCode(errCode))
		statusnotifier.DriverError(errCode)
		return
	}

	alarm := fmt.Sprintf(
		"Drive fault active on %s (sw=0x%04X) — error code unavailable",
		device.Name,
		statusword,
	)
	logger.Error(tag, alarm)
	statusnotifier.Alarm(alarm)
}

// stopErrorPolling signals ALL error-polling goroutines to stop and blocks
// until they have actually exited (up to a bounded timeout).
// Safe to call even when pollDriveError was never started (PDO mode).
func stopErrorPolling() {
	if !errPollingRunning.Load() {
		return // no goroutines running — nothing to stop
	}
	errPollingRunning.Store(false)
	for _, ch := range stopErrPollingChans {
		ch <- true
	}
	waitWithTimeout(&errPollingWG, 2*time.Second, "pollDriveErrWorker")
}

// pollDriveErrWorker polls the drive error code (0x603F) and notifies the
// status subsystem.
//
// PDO path (preferred): when PDO is active, 0x603F is read by the cyclic task
// every 1ms and cached in lastPDOErr. We read from that atomic buffer here —
// no SDO, no mailbox, no risk of blocking.
//
// SDO path: used when PDO is not active (e.g. pre-activation diagnostics).
// initListeners already skips calling pollDriveError() when PDO is active,
// so this worker should only ever reach the SDO branch before activation.
func pollDriveErrWorker(device *MasterDevice, stopCh chan bool) {
	defer errPollingWG.Done()
	logger.Info("polling error of driver: ", device.Name)

	var lastReportedErrCode int
	var usePDO bool
	// Statusword bit 3 is the authoritative CiA-402 fault indication. Do not
	// require 0x603F to be mapped before starting PDO fault monitoring: some
	// drives expose the fault bit several cycles before a usable error code,
	// and some configurations do not map 0x603F at all.
	if IsPDOActive() {
		usePDO = true
		logger.Info("[PDO] pollDriveErrWorker: monitoring statusword fault bit; statusMapped=",
			device.PdoStatusReady, "errorCodeMapped=", device.PdoErrorReady,
			"driver:", device.Name)
	} else {
		operation, _ := GetEtherCATOperation("readError", device.Device.AddressConfigName)
		if len(operation.Steps) <= 0 {
			logger.Warn("pollDriveErrWorker: no readError steps configured for", device.Name)
			return
		}
		step := operation.Steps[0]

		// SDO polling loop (pre-activation path).
		for {
			select {
			default:
				if IsPDOActive() {
					// PDO became active mid-poll — switch to PDO path.
					usePDO = true
					goto pdoLoop
				}
				errCode, err := SDOUpload2(device.Master, device.Position, step)
				if err == nil {
					handleErrCode(device, errCode, uint16(device.PDOStatus.Load()&0xFFFF), &lastReportedErrCode)
				}
				time.Sleep(100 * time.Millisecond)
			case <-stopCh:
				logger.Debug("stopping driver error listener (SDO path)")
				return
			}
		}
	}

pdoLoop:
	if !usePDO {
		return
	}

	// PDO polling loop: read from atomic updated by cyclic task.
	for {
		select {
		default:
			sw := uint16(device.PDOStatus.Load() & 0xFFFF)
			if sw&0x0008 != 0 {
				errCode := 0
				if device.PdoErrorReady {
					errCode = int(device.PDOErr.Load() & 0xFFFF)
				}
				if errCode != 0 {
					handleErrCode(device, errCode, sw, &lastReportedErrCode)
				} else {
					handleFaultWithoutErrorCode(device, sw, &lastReportedErrCode)
				}
			} else if lastReportedErrCode != 0 && lastReportedErrCode != errCodeCleared {
				statusnotifier.AlarmCleared()
				lastReportedErrCode = errCodeCleared
			}
			time.Sleep(100 * time.Millisecond)
		case <-stopCh:
			logger.Debug("stopping driver error listener (PDO path)")
			return
		}
	}
}

// pdoErrLoop is retained from the SDO/PDO build as a helper for PDO-mode
// alarm polling. New code calls it from pollDriveErrWorker with a per-device
// stop channel.
func pdoErrLoop(device *MasterDevice, stopCh chan bool, lastReportedErrCode *int) {
	for {
		select {
		default:
			sw := uint16(device.PDOStatus.Load() & 0xFFFF)
			if sw&0x0008 != 0 {
				errCode := 0
				if device.PdoErrorReady {
					errCode = int(device.PDOErr.Load() & 0xFFFF)
				}
				if errCode != 0 {
					handleErrCode(device, errCode, sw, lastReportedErrCode)
				} else {
					handleFaultWithoutErrorCode(device, sw, lastReportedErrCode)
				}
			} else if *lastReportedErrCode != 0 && *lastReportedErrCode != errCodeCleared {
				statusnotifier.AlarmCleared()
				*lastReportedErrCode = errCodeCleared
			}
			time.Sleep(100 * time.Millisecond)
		case <-stopCh:
			logger.Debug("stopping driver error listener (PDO path)")
			return
		}
	}
}

// handleFaultWithoutErrorCode reports a real CiA-402 fault even when 0x603F
// has not produced a usable code yet. This is especially important at cold
// startup: statusword bit 3 can already be HIGH while the error PDO still
// contains zero. Previously that state produced no UI alarm until Reset ran
// its separate post-reset statusword check.
//
// The sentinel prevents a 100 ms alarm flood. If a nonzero error code appears
// later, handleErrCode replaces this generic alarm with the decoded one.
func handleFaultWithoutErrorCode(device *MasterDevice, statusword uint16, lastReportedErrCode *int) {
	if *lastReportedErrCode == errCodeUnknownFault {
		return
	}

	*lastReportedErrCode = errCodeUnknownFault
	alarm := fmt.Sprintf(
		"Drive fault active on %s (sw=0x%04X) — error code unavailable",
		device.Name,
		statusword,
	)
	logger.Error("[AlarmPoller]", alarm)
	statusnotifier.Alarm(alarm)
}

// handleErrCode is the shared rising-edge filter for SDO and PDO alarm polling.
func handleErrCode(device *MasterDevice, errCode int, statusword uint16, lastReportedErrCode *int) {
	if errCode == 0 {
		if *lastReportedErrCode != 0 && *lastReportedErrCode != errCodeCleared {
			statusnotifier.AlarmCleared()
			*lastReportedErrCode = errCodeCleared
		}
		return
	}
	if errCode == *lastReportedErrCode {
		return
	}
	*lastReportedErrCode = errCode
	logger.Error("[AlarmPoller]", FormatErrorCode(errCode),
		"sw=", fmt.Sprintf("0x%04X", statusword), "device:", device.Name)
	RecordFault(device.Name, uint16(errCode), statusword)
	statusnotifier.Alarm(FormatErrorCode(errCode))
	statusnotifier.DriverError(errCode)
}
