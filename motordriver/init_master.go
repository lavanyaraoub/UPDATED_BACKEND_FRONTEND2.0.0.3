package motordriver

/*
#include <ecrt.h>
#include "ethercatinterface.h"
*/
import "C"

import (
	parser "EtherCAT/configparser"
	ethercatDevice "EtherCAT/ethercatdevicedatatypes"
	"EtherCAT/logger"
	"EtherCAT/motordriver/statusnotifier"
	settings "EtherCAT/settings"
	"fmt"
	"strings"
	"time"
)

var driverConnectionStatus = "SUCCESS"

// masterDevices is a slice of POINTERS so that PDO flags set by SetupPDOPosition
// (e.g. PdoReady, PdoJogReady, OffControlWord …) are visible to every goroutine
// that holds a reference to the same device.  The old []MasterDevice (value slice)
// meant that goroutines launched with a copy never saw the flags written by
// SetupPDOPosition, so device.PdoReady was always false inside pollDrivePositionProcess.
var masterDevices []*MasterDevice

var ethercatAddressMapping map[string]ethercatDevice.Ethercat

// pdoLayoutMap stores the parsed PDO layout for each drive config file.
// Key is AddressConfigName (e.g. "delta_asda2e"), same key as ethercatAddressMapping.
// Populated in InitMaster() from the pdo: section of each drive YAML.
// Used by SetupPDOPosition() (Phase 3) to drive the generic C registration engine.
var pdoLayoutMap map[string]*PDOLayout

// ============================================================
// Phase 5 — Bus Scanning
//
// ScanBus queries the IgH master for every slave physically
// present on the EtherCAT wire. Safe to call before activation.
//
// ValidateBusConfig compares the scanned bus against what
// device-configuration.yml expects. Returns a joined error
// string listing every mismatch, or "" if everything matches.
//
// WHY THIS EXISTS:
//   Before Phase 5, a misconfigured vendor-id or product-code
//   in device-configuration.yml caused ecrt_master_slave_config()
//   to silently return nil — the PDO registration then produced
//   all-zero offsets, the drive never reached Operation Enabled,
//   and the only clue was a cryptic "ecrt_domain_data returned nil"
//   message deep in the startup log.
//
//   Phase 5 catches the mismatch before any SDO writes happen and
//   prints a clear message:
//     "Axis A: expected Delta 0x000001DD/0x10305070 at position 0,
//      found Panasonic 0x0000066F/0x535300A1"
// ============================================================

// SlaveOnBus holds the identity of one physically detected slave.
type SlaveOnBus struct {
	Position    int
	Alias       int
	VendorID    uint32
	ProductCode uint32
	Name        string
}

// ScanBus returns a slice of SlaveOnBus describing every slave
// the IgH master can see. Returns an error only on a C call failure;
// an empty slice means no slaves were detected (link down or all off).
func ScanBus(master *C.ec_master_t) ([]SlaveOnBus, error) {
	const maxSlaves = 32
	buf := make([]C.BusSlaveInfo, maxSlaves)

	count := int(C.scan_bus(master, &buf[0], C.int(maxSlaves)))
	if count < 0 {
		return nil, fmt.Errorf("ScanBus: scan_bus returned error %d", count)
	}

	result := make([]SlaveOnBus, count)
	for i := 0; i < count; i++ {
		result[i] = SlaveOnBus{
			Position:    int(buf[i].position),
			Alias:       int(buf[i].alias),
			VendorID:    uint32(buf[i].vendor_id),
			ProductCode: uint32(buf[i].product_code),
			Name:        C.GoString(&buf[i].name[0]),
		}
	}
	return result, nil
}

// ValidateBusConfig compares scanned slaves against configured devices.
//
// Matching rule: configured device at index N is expected at ring
// position N (0-based). vendor-id and product-code must match exactly.
//
// Reports:
//   - Configured devices with no matching slave on the bus
//   - Slaves at the expected position but with wrong vendor/product
//   - Extra slaves on the bus beyond what is configured (warning only)
//
// Returns "" if everything matches, or a newline-joined error string.
func ValidateBusConfig(scanned []SlaveOnBus, configured []ethercatDevice.Device) string {
	var errs []string

	for idx, dev := range configured {
		expectedPos := dev.ID

		if expectedPos >= len(scanned) {
			errs = append(errs, fmt.Sprintf(
				"Axis %s (pos %d): NOT FOUND on bus vendor=0x%08X product=0x%08X",
				dev.Name, expectedPos, uint32(dev.VendorID), uint32(dev.ProductCode)))
			continue
		}

		found := scanned[expectedPos]

		if found.VendorID != uint32(dev.VendorID) || found.ProductCode != uint32(dev.ProductCode) {
			errs = append(errs, fmt.Sprintf(
				"Axis %s (pos %d): MISMATCH config=0x%08X/0x%08X bus=0x%08X/0x%08X name=%q",
				dev.Name, expectedPos,
				uint32(dev.VendorID), uint32(dev.ProductCode),
				found.VendorID, found.ProductCode, found.Name))
		} else {
			logger.Info(fmt.Sprintf(
				"[BUS-SCAN] Axis %s (pos %d): OK vendor=0x%08X product=0x%08X name=%q",
				dev.Name, idx, uint32(dev.VendorID), uint32(dev.ProductCode), found.Name))
		}
	}

	for i := len(configured); i < len(scanned); i++ {
		s := scanned[i]
		logger.Warn(fmt.Sprintf(
			"[BUS-SCAN] Extra slave pos=%d vendor=0x%08X product=0x%08X name=%q",
			s.Position, s.VendorID, s.ProductCode, s.Name))
	}

	return strings.Join(errs, "; ")
}

// InitMaster initialises all EtherCAT masters and brings up the drives.
//
// Sequence (order matters for IgH EtherCAT master correctness):
//  1. Parse config
//  2. SetMotorDriver  — HAL: MUST be first so SetupPDOPosition calls the right driver
//  3. RequestMaster   — kernel module claims the bus
//  4. configureDriver — SDO parameter writes (mode, gains, limits …)
//  5. reverseDir / nonReverseDir — SDO polarity write
//  6. PowerOn — SDO enable
//  7. SetupPDOPosition — registers domain + PDO entry offsets (still pre-activation)
//  8. setupDrivers — SDO clamp/declamp decision  ← MUST be before activation
//  9. StartPDOCyclic — calls ecrt_master_activate(); NO MORE SDO after this point
//
// 10. Start listener goroutines
func InitMaster() error {
	// 1. Reload persisted fault history from disk immediately
	LoadFaultHistory()

	ethercatAddressMapping = make(map[string]ethercatDevice.Ethercat)
	pdoLayoutMap = make(map[string]*PDOLayout)

	// 2. Read the initial device-configuration.yml (with "auto" and empty strings)
	devices, err := parser.ParseDeviceConfig()
	if err != nil {
		return err
	}

	if len(devices.Device) == 0 {
		return fmt.Errorf("InitMaster: no devices configured")
	}

	// =================================================================
	// 3. AUTO-DISCOVERY PHASE (Moved UP!)
	// Request master and scan the bus BEFORE parsing individual YAMLs
	// =================================================================
	master0, reqErr := RequestMaster(devices.Device[0])
	if reqErr != nil {
		logger.Error("RequestMaster error:", reqErr)
		return reqErr
	}
	if master0 == nil {
		return fmt.Errorf("InitMaster: RequestMaster returned nil master")
	}

	scanned, scanErr := ScanBus(master0)
	if scanErr != nil {
		logger.Warn("[BUS-SCAN] Could not scan bus:", scanErr, "— proceeding anyway")
	} else {
		for i, dev := range devices.Device {
			if dev.DriveType == "auto" || dev.VendorID == 0 {
				if dev.ID >= len(scanned) {
					return fmt.Errorf("InitMaster: configured Axis %s at position %d but no physical drive found there", dev.Name, dev.ID)
				}

				found := scanned[dev.ID]
				logger.Info(fmt.Sprintf("[AUTO-DISCOVER] Axis %s (pos %d): Found hardware Vendor=0x%08X Product=0x%08X", dev.Name, dev.ID, found.VendorID, found.ProductCode))

				// Look up the profile based on the physical hardware
				profile, err := IdentifyDrive(found.VendorID, found.ProductCode)
				if err != nil {
					return fmt.Errorf("InitMaster: Auto-Discovery failed for Axis %s: %v", dev.Name, err)
				}

				// Inject the discovered parameters into memory dynamically!
				devices.Device[i].VendorID = int(found.VendorID)
				devices.Device[i].ProductCode = int(found.ProductCode)
				devices.Device[i].DriveType = profile.DriveType
				devices.Device[i].AddressConfigName = profile.AddressConfigName
				devices.Device[i].AddressConfigFile = profile.AddressConfigFile
				devices.Device[i].Alias = int(found.Alias)

				// Inject motion parameters from the drive YAML (motion: section).
				// RPMConst and DriveXRatio are drive-type facts — they live in the
				// drive YAML and are loaded by IdentifyDrive() via ParseMotionConfig().
				// This means device-configuration.yml no longer needs rpm-const or
				// drive-x-ratio. Swapping a physical drive auto-loads the correct values.
				if profile.Motion.RPMConst > 0 && profile.Motion.DriveXRatio > 0 {
					devices.Device[i].RPMConst = profile.Motion.RPMConst
					devices.Device[i].DriveXRatio = profile.Motion.DriveXRatio
					logger.Info(fmt.Sprintf(
						"[AUTO-DISCOVER] Axis %s motion params injected — rpm_const=%d drive_x_ratio=%d",
						dev.Name, profile.Motion.RPMConst, profile.Motion.DriveXRatio))
				} else {
					// IdentifyDrive returned an error loading motion config.
					// Log clearly — the operator needs to add a motion: section
					// to the drive YAML before motion will work correctly.
					logger.Warn(fmt.Sprintf(
						"[AUTO-DISCOVER] Axis %s: motion: section missing or incomplete in %s — "+
							"rpm_const and drive_x_ratio will be 0. Add motion: section to the drive YAML.",
						dev.Name, profile.AddressConfigFile))
				}

				logger.Info(fmt.Sprintf("[AUTO-DISCOVER] Successfully mapped Axis %s to driver type: %s", dev.Name, profile.DriveType))
			}
		}

		// Now run the normal validation to ensure everything matches
		if mismatches := ValidateBusConfig(scanned, devices.Device); mismatches != "" {
			return fmt.Errorf("InitMaster: bus mismatch: %s", mismatches)
		}
	}

	// =================================================================
	// 4. PARSE INDIVIDUAL DRIVE YAMLS (Moved DOWN!)
	// Now dev.AddressConfigFile is safely populated with the discovered path.
	// =================================================================
	for _, dev := range devices.Device {
		address, parseErr := parser.ParseEthercatAddressConfig(dev.AddressConfigFile)
		if parseErr != nil {
			return parseErr
		}
		ethercatAddressMapping[dev.AddressConfigName] = address

		pdoLayout, layoutErr := ParsePDOLayout(dev.AddressConfigFile)
		if layoutErr != nil {
			logger.Warn("[PDO-LAYOUT] Could not parse pdo: section for",
				dev.AddressConfigName, ":", layoutErr,
				"— will use legacy hardcoded C PDO path")
		} else {
			if unknowns := ValidatePDOLayout(pdoLayout); len(unknowns) > 0 {
				for _, u := range unknowns {
					logger.Warn("[PDO-LAYOUT] Unknown entry name in",
						dev.AddressConfigName, ":", u)
				}
			}
			pdoLayoutMap[dev.AddressConfigName] = pdoLayout
			logger.Info("[PDO-LAYOUT] Stored layout for", dev.AddressConfigName,
				"— RxPDO:", len(pdoLayout.RxEntries()), "entries",
				"TxPDO:", len(pdoLayout.TxEntries()), "entries")
		}
	}

	// =================================================================
	// 5. CONFIGURE DRIVES AND START PDO
	// =================================================================
	//
	// FIX (Bug 4): Use dev.ID directly as Position instead of a separate
	// incrementing counter.
	//
	// The old code used a local `position` variable starting at 0 and
	// incremented it once per loop iteration. This assumed that devices
	// in device-configuration.yml are listed in strict ring-position order
	// with no gaps (0, 1, 2, ...). If the YAML ever has a gap — for example,
	// two drives at ring positions 0 and 2 with nothing at 1 — the second
	// drive would be registered at position 1 (wrong). IgH would call
	// ecrt_master_slave_config(master, 0, 1, ...) for a slave that does not
	// exist at that position, silently return nil, and the PDO setup for that
	// drive would fail with all-zero offsets and no useful error message.
	//
	// Using dev.ID directly ensures the C layer always targets the exact ring
	// position the operator configured in the YAML, regardless of listing order
	// or gaps between positions.
	for _, i := range devices.Device {
		SetMotorDriver(i.DriveType)
		masterDevice := &MasterDevice{
			Master:   master0,
			Position: i.ID, // FIX: use ring position from YAML, not a loop counter
			Name:     i.Name,
			Device:   i,
			Driver:   GetDriverForType(i.DriveType),
		}
		masterDevices = append(masterDevices, masterDevice)

		configErr := configureDriver(masterDevice)
		if configErr != nil {
			driverConnectionStatus = "ERROR"
			statusnotifier.DriverStatus(i.Name, "0")
			return configErr
		}

		drvSettings := settings.GetDriverSettings(masterDevice.Name)
		if drvSettings.MotorDirection == 1 {
			nonReverseDir(masterDevice)
		} else {
			reverseDir(masterDevice)
		}

		statusnotifier.DriverStatus(i.Name, "1")
		time.Sleep(500 * time.Millisecond)
		PowerOn(masterDevice)
		time.Sleep(500 * time.Millisecond)

		if pdoErr := SetupPDOPosition(masterDevice); pdoErr != nil {
			logger.Warn("PDO setup failed for", i.Name, "— will fall back to SDO polling:", pdoErr)
		} else {
			logger.Info("PDO setup successful for", i.Name,
				"PdoJogReady:", masterDevice.PdoJogReady,
				"PdoPosReady:", masterDevice.PdoPosReady)
		}
	}

	setupDrivers(masterDevices)

	// Start PDO cyclic only when ALL configured devices have PDO set up.
	// Previously checked only masterDevices[0] — on a 2-axis system where
	// Axis B's PDO setup failed, the cyclic would start anyway and Axis B
	// would receive all-zero RxPDO writes every tick, causing faults.
	allPdoReady := len(masterDevices) > 0
	for _, dev := range masterDevices {
		if !dev.PdoReady {
			allPdoReady = false
			logger.Warn("[INIT] PDO not ready for device:", dev.Name, "— skipping cyclic start")
			break
		}
	}

	pdoOK := false
	if allPdoReady {
		if startErr := StartPDOCyclic(masterDevices); startErr != nil {
			logger.Warn("PDO cyclic start failed — falling back to SDO polling:", startErr)
		} else {
			pdoOK = true
			logger.Info("[PDO] Cyclic task running. SDO access is now DISABLED.")
		}
	}

	// InitAposCorrection must be called AFTER StartPDOCyclic so that
	// dev.PDOPos.Load() returns the first real encoder value from the domain
	// (not zero). It detects whether the encoder sign flipped since the last
	// zero-reference and stores a per-device correction offset so
	// currentPosition() shows the correct angle after a power cycle.
	for _, dev := range masterDevices {
		if dev.PdoReady {
			InitAposCorrection(dev.Name)
		}
	}

	initListeners(masterDevices, pdoOK)
	notifyStartupDriveFaults(masterDevices)

	time.Sleep(500 * time.Millisecond)
	driverConnectionStatus = "SUCCESS"
	return nil
}

// ForceReleaseMaster provides a fail-safe way to kill the hardware connection
// if the cyclic task or PDS shutdown sequence hangs.
func ForceReleaseMaster() {
	if len(masterDevices) > 0 {
		master := masterDevices[0].Master
		if master != nil {
			logger.Warn("[FORCE] Emergency deactivation of EtherCAT master...")
			C.ecrt_master_deactivate(master)
			C.ecrt_release_master(master)
		}
	}
}

// waitForFaultClearSDO polls 0x6041 (statusword) via SDO until the fault bit
// (bit 3) clears, or the deadline expires.
//
// Must only be called before ecrt_master_activate (SDO window is open).
// Returns true if the fault cleared within the timeout, false on timeout.
//
// WHY THIS IS NEEDED:
//
//	The faultReset YAML sequence sends CW=0x0080 to clear a latched CiA-402
//	fault. The drive needs time to process this and exit the Fault PDS state.
//	Calling FastPowerOff (CW=0x0006) before fault bit clears sends the Shutdown
//	command into a transitional PDS state, which can prevent the drive from
//	reaching "Ready To Switch On" cleanly before ecrt_master_activate is called.
func waitForFaultClearSDO(dev *MasterDevice, timeout time.Duration) bool {
	operation, err := GetEtherCATOperation("readStatusword", dev.Device.AddressConfigName)
	if err != nil {
		// Config doesn't have readStatusword yet — fall back to a fixed sleep
		// long enough to cover the worst-case A6 Minas fault-exit time (~600ms).
		logger.Warn("[SETUP] waitForFaultClearSDO: no readStatusword operation configured for",
			dev.Device.AddressConfigName, "— using 600ms fallback sleep")
		time.Sleep(600 * time.Millisecond)
		return true // non-fatal: proceed, we just can't confirm
	}
	step := operation.Steps[0]
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		sw, sdoErr := SDOUpload2(dev.Master, dev.Position, step)
		if sdoErr == nil && (uint16(sw)&0x0008) == 0 {
			logger.Info(fmt.Sprintf("[SETUP] Fault bit cleared — sw=0x%04X device:%s",
				uint16(sw), dev.Name))
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	sw, _ := SDOUpload2(dev.Master, dev.Position, step)
	logger.Warn(fmt.Sprintf("[SETUP] waitForFaultClearSDO: timeout after %v — "+
		"fault bit still set sw=0x%04X device:%s", timeout, uint16(sw), dev.Name))
	return false
}

// waitForReadyToSwitchOnSDO polls 0x6041 (statusword) via SDO until the drive
// reaches CiA-402 PDS "Ready To Switch On" (sw & 0x6F == 0x21) or
// "Switch On Disabled" (sw & 0x6F == 0x40), or the deadline expires.
//
// Must only be called before ecrt_master_activate (SDO window is open).
// Returns true if the drive confirmed a safe state within the timeout.
//
// WHY THIS IS NEEDED:
//
//	FastPowerOff sends CW=0x0006 (Shutdown command) via SDO, but the old code
//	only slept a fixed 500ms before calling ecrt_master_activate. If the A6
//	Minas hasn't finished the Shutdown PDS transition when the ESM walks
//	PREOP→SAFEOP→OP, the drive raises Error 80 ("ESM unauthorized request").
//	Actively polling 0x6041 until PDS=0x21 eliminates this race entirely.
func waitForReadyToSwitchOnSDO(dev *MasterDevice, timeout time.Duration) bool {
	operation, err := GetEtherCATOperation("readStatusword", dev.Device.AddressConfigName)
	if err != nil {
		// Config doesn't have readStatusword yet — fall back to the old fixed sleep.
		logger.Warn("[SETUP] waitForReadyToSwitchOnSDO: no readStatusword operation configured for",
			dev.Device.AddressConfigName, "— using 500ms fallback sleep")
		time.Sleep(500 * time.Millisecond)
		return true // non-fatal: proceed without confirmation
	}
	step := operation.Steps[0]
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		sw, sdoErr := SDOUpload2(dev.Master, dev.Position, step)
		if sdoErr == nil {
			state := uint16(sw) & 0x006F
			// 0x0021 = Ready To Switch On  (Shutdown command accepted — safe for ESM)
			// 0x0040 = Switch On Disabled   (also safe for ESM transitions)
			if state == 0x0021 || state == 0x0040 {
				logger.Info(fmt.Sprintf("[SETUP] Drive confirmed safe PDS state before activation "+
					"— sw=0x%04X device:%s", uint16(sw), dev.Name))
				return true
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	sw, _ := SDOUpload2(dev.Master, dev.Position, step)
	logger.Warn(fmt.Sprintf("[SETUP] waitForReadyToSwitchOnSDO: timeout after %v — "+
		"drive did not confirm safe state sw=0x%04X device:%s", timeout, uint16(sw), dev.Name))
	return false
}

// setupDrivers ensures the drive CiA-402 PDS state is safe for ESM transitions.
// Must be called BEFORE ecrt_master_activate (i.e. before StartPDOCyclic).
//
// ROOT CAUSE OF ERROR 80 — Panasonic MINAS A6 "ESM unauthorized request error
// protection":
//
//	Error 80 fires when ecrt_master_activate walks ESM PREOP→SAFEOP→OP while
//	the drive's CiA-402 PDS is not at a state that permits the OP transition.
//	Two scenarios trigger it:
//
//	Scenario A — Stale fault from previous session:
//	  A bad shutdown left Error 80 latched in 0x603F. The faultReset SDO
//	  (CW=0x80) clears the CiA-402 fault bit, but if FastPowerOff (CW=0x0006)
//	  is sent before the drive has finished processing the fault reset, the
//	  Shutdown command arrives in a transitional PDS state → ESM activation
//	  triggers Error 80 again.
//	  Fix: waitForFaultClearSDO — poll 0x6041 until bit3=0 before issuing
//	  FastPowerOff.
//
//	Scenario B — FastPowerOff not yet confirmed before activation:
//	  FastPowerOff writes CW=0x0006 (Shutdown), but the old code only slept a
//	  fixed 500ms and assumed the drive confirmed "Ready To Switch On". If the
//	  A6 hasn't finished the Shutdown transition when ecrt_master_activate is
//	  called, the ESM OP transition finds PDS at "Switched On" or worse →
//	  Error 80.
//	  Fix: waitForReadyToSwitchOnSDO — poll 0x6041 until sw & 0x6F == 0x21.
func setupDrivers(devices []*MasterDevice) {
	for _, dev := range devices {
		// ── Step 1: Fault reset — clear any stale drive fault ─────────────────
		//
		// FIX (Scenario A): after the faultReset SDO sequence, actively wait for
		// the fault bit (statusword bit 3) to clear before proceeding to
		// FastPowerOff. The old fixed 200ms sleep was not enough when Error 80
		// was latched from a previous bad shutdown — the A6 Minas can take up to
		// ~600ms to fully exit fault state after receiving CW=0x80.
		if operation, err := GetEtherCATOperation("faultReset", dev.Device.AddressConfigName); err == nil {
			logger.Info("[SETUP] Issuing pre-activation fault reset to clear any stale drive fault:", dev.Name)
			for _, step := range operation.Steps {
				if step.Action == "read" {
					SDOUpload2(dev.Master, dev.Position, step)
				} else {
					SDODownload(dev.Master, dev.Position, step)
				}
			}
			// Actively confirm the fault bit cleared (up to 1 second).
			// The faultReset YAML already contains a 150ms built-in delay inside
			// its steps; this poll runs on top of that.
			if !waitForFaultClearSDO(dev, 1*time.Second) {
				logger.Warn("[SETUP] Fault did not clear within 1s after faultReset SDO —",
					"drive may need power cycle:", dev.Name)
				notifySetupDriveFault(dev)
			}
		} else {
			logger.Warn("[SETUP] No faultReset operation configured — skipping pre-activation fault clear:", err)
		}

		// ── Step 2: Shutdown — leave PDS at "Ready To Switch On" ──────────────
		//
		// FIX (Scenario B): after FastPowerOff (CW=0x0006) poll 0x6041 via SDO
		// until the drive confirms PDS = "Ready To Switch On" (sw & 0x6F == 0x21).
		// The old blind time.Sleep(500ms) was the direct cause of intermittent
		// Error 80 at startup — the A6 occasionally needs more than 500ms to
		// confirm the Shutdown command, and ecrt_master_activate was called too
		// early. Polling with a 2s timeout eliminates this race entirely.
		FastPowerOff(dev)
		if !waitForReadyToSwitchOnSDO(dev, 2*time.Second) {
			// Log the failure clearly. Do not abort — the pdoStopCh handler in
			// the cyclic task also sends CW=0x0006 and will keep trying, but
			// Error 80 may still occur if the drive is in a stuck state.
			logger.Error("[SETUP] Drive did not confirm Ready To Switch On before activation —",
				"Error 80 risk. Check drive hardware and previous fault history:", dev.Name)
		}
	}
}

// initListeners starts all background goroutines.
// pdoOK tells it whether the PDO cyclic task is running.
func initListeners(devices []*MasterDevice, pdoOK bool) {
	if len(devices) == 0 {
		return
	}

	initDriverActionListener()
	initDriverStatusKeeperListener()
	listenSystemReset()

	// pollDrivePosition is the unified position poller.
	// Internally it checks device.PdoReady to decide whether to read from the
	// PDO memory buffer or fall back to SDO — and it always notifies the UI.
	pollDrivePosition(devices)
	// Only poll errors via SDO when PDO is not running.
	pollDriveError(masterDevices)
	pollIOStat(masterDevices)
	// ── BPF: start Modbus poller after all listeners are ready ──
	StartBPFPoller()
}

// ShutdownMasters safely stops all drives and the PDO cyclic task.
//
// Three-phase shutdown — prevents Error 80 ("ESM unauthorized request error
// protection" raised when ecrt_release_master walks ESM OP→SAFEOP while the
// drive PDS is still at Operation Enabled):
//
//	Phase 1 — Arm pdoShutdownActive on every device.
//	  The cyclic standby branch writes CW=0x0006 (Shutdown) instead of
//	  CW=0x000F on every 1ms tick, walking PDS down automatically.
//
//	Phase 2 — Poll statusword until PDS is safe (sw&0x6F == 0x0021 or 0x0040).
//	  Deadline extended to 3 seconds. If the drive reaches a safe state or is
//	  already faulted (bit3=1) we proceed to Phase 3 immediately.
//
//	Phase 2b — Hard block if drive is still at Operation Enabled after Phase 2.
//	  ROOT CAUSE FIX: the old code logged a Warn and called StopPDOCyclic()
//	  immediately when Phase 2 timed out. That is what caused Error 80 to latch
//	  on every stop/exit — ecrt_release_master fired with PDS=Op Enabled.
//	  Now we log a hard Error and let StopPDOCyclic's internal pdoStopCh handler
//	  (which sends CW=0x0006 in a tight 1ms C loop for up to 2s) finish the job
//	  before ecrt_release_master is called.
//
//	Phase 3 — StopPDOCyclic.
//	  By the time we reach here the drive has either reached a safe PDS state,
//	  or the pdoStopCh handler has made its best effort. The IgH watchdog
//	  OP→SAFEOP now fires onto a drive that is at "Ready To Switch On", so no
//	  Error 80 is raised.
//
// NOTE: PDOFaultReset is NOT called here. It would walk the drive BACK to
// Operation Enabled which is exactly the wrong state to be in before shutdown.
func ShutdownMasters() {
	if !IsPDOActive() {
		return
	}

	logger.Info("[SHUTDOWN] Starting 3-phase graceful shutdown to prevent Error 80")

	// Phase 1: Halt motion + arm shutdown flag on all devices.
	for _, dev := range masterDevices {
		dev.EnableJogPDO(false)
		dev.EnablePosPDO(false)
		dev.desiredTargetVelocity.Store(0)
		dev.pdoShutdownActive.Store(true) // Forces cyclic loop to send CW=0x0006
	}
	logger.Info("[SHUTDOWN] pdoShutdownActive=true — cyclic now sends CW=0x0006 every tick")

	// Phase 2: Wait for ALL drives to reach a safe idle state.
	// Extended from 2s to 3s — the A6 Minas occasionally needs more than 2s to
	// walk from Operation Enabled to Ready To Switch On when under load.
	//
	// FIX: Previously only checked masterDevices[0]. On a 2-axis system, Axis B
	// could still be at Operation Enabled when Phase 3 fired, causing Error 80.
	// Now polls ALL devices and only proceeds once every drive is in a safe state.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		allSafe := true
		for _, dev := range masterDevices {
			sw := uint16(dev.PDOStatus.Load() & 0xFFFF)
			state := sw & 0x006F
			// 0x0021 = Ready To Switch On  (target — safe for ESM transitions)
			// 0x0040 = Switch On Disabled  (also safe)
			// bit3   = Drive already faulted (ESM will drop regardless)
			if !(state == 0x0021 || state == 0x0040 || (sw&0x0008) != 0) {
				allSafe = false
				break
			}
		}
		if allSafe {
			logger.Info("[SHUTDOWN] All drives at safe PDS state — proceeding to StopPDOCyclic")
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Phase 2b: ROOT CAUSE FIX for Error 80.
	//
	// Check if ANY drive is STILL at Operation Enabled after Phase 2.
	// If so, log a hard Error — this is an abnormal condition.
	// The pdoStopCh handler inside the cyclic goroutine will keep sending
	// CW=0x0006 in a tight 1ms C loop for up to 2 more seconds after receiving
	// the stop signal, giving each drive another window to reach Ready To Switch On.
	//
	// FIX: Previously only checked masterDevices[0] — now checks all devices.
	for _, dev := range masterDevices {
		finalSW := uint16(dev.PDOStatus.Load() & 0xFFFF)
		if finalSW&0x006F == 0x0027 {
			logger.Error(fmt.Sprintf(
				"[SHUTDOWN] Drive %s still at Operation Enabled (sw=0x%04X) after 3s. "+
					"pdoStopCh handler will attempt a final 2s CW=0x0006 burst before "+
					"releasing master. If Error 80 occurs, inspect hardware preventing servo-off.",
				dev.Name, finalSW))
		}
	}

	// Phase 3: Stop cyclic + release master (ecrt_release_master called inside).
	// StopPDOCyclic signals pdoStopCh. The cyclic goroutine will:
	//   1. Send CW=0x0006 every 1ms for up to 2 seconds (extended from 500ms fix).
	//   2. Only exit once PDS reaches Ready To Switch On or deadline expires.
	//   3. Close pdoCyclicDone, then ecrt_release_master is called below.
	StopPDOCyclic()
}

// PowerOnMasters powers on all available masters.
func PowerOnMasters() {
	for _, device := range masterDevices {
		PowerOn(device)
	}
}

// GetEtherCATOperation returns the operation configured in ethercat-device-addressing.yml.
func GetEtherCATOperation(operation string, deviceAddressConfigName string) (ethercatDevice.Operation, error) {
	addressMapping := getEtherCATAddress(deviceAddressConfigName)
	return addressMapping.GetOperation(operation)
}

// HasDriverConnected returns true when all drivers connected successfully.
func HasDriverConnected() bool {
	return driverConnectionStatus == "SUCCESS"
}

func getMasterDevices() []*MasterDevice {
	return masterDevices
}

func getEtherCATAddress(deviceAddressConfigName string) ethercatDevice.Ethercat {
	return ethercatAddressMapping[deviceAddressConfigName]
}
