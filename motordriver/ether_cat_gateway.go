package motordriver

/*
#cgo CFLAGS:  -I/home/pi/gosrc/src/EtherCAT -I/opt/etherlab/include
#cgo LDFLAGS: /home/pi/gosrc/src/EtherCAT/libethercatinterface.so -L/opt/etherlab/lib -lethercat -ldl -lpthread
#include <stdlib.h>
#include <string.h>
#include "ethercatinterface.h"
*/
import "C"
import (
	ethercatDevice "EtherCAT/ethercatdevicedatatypes"
	"EtherCAT/logger"
	"EtherCAT/motordriver/statusnotifier"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

// MasterDevice holds the EtherCAT master handle and all PDO-related state
// for a single slave drive (Panasonic MINAS A6).
type MasterDevice struct {
	Master *C.ec_master_t

	// PDO infrastructure
	Domain      *C.ec_domain_t
	SlaveConfig *C.ec_slave_config_t // set by setupPDOPositionGeneric; used for drive-specific SDO request creation

	FaultResetMu sync.Mutex
	SDOExclusive atomic.Bool

	// ── Phase 4: per-device PDO state ──────────────────────────────────────
	//
	// Previously these lived as package-level globals (lastPDOPos, lastPDOStatus,
	// lastPDOErr, lastPDODI) — meaning only one drive could be tracked at a time.
	// Moving them into MasterDevice lets the 1ms cyclic task maintain independent
	// state for every slave on the bus.
	//
	// The global getter functions (GetLastPDOPosition etc.) are kept as wrappers
	// that read from masterDevices[0] for backward compatibility with existing
	// callers. Per-device access uses these fields directly.
	PDOPos    atomic.Int32  // 0x6064 Position actual value  — written by cyclic task
	PDOStatus atomic.Uint32 // 0x6041 Statusword             — written by cyclic task

	// AposCorrection compensates a boot-time encoder sign flip detected by
	// InitAposCorrection(). Per-device field replaces the old package-level
	// aposCorrection global which was shared across all drives — wrong on
	// multi-drive setups where the second drive's correction overwrote the first.
	AposCorrection atomic.Int32
	PDOErr         atomic.Uint32 // 0x603F Error code             — written by cyclic task
	PDODI          atomic.Uint32 // DI register (0x60FD or 0x4F25) — written by cyclic task
	// PDOOpModeDisplay caches the drive's active op-mode (0x6061) read from TxPDO each cycle.
	// Drives write their currently executing mode here. Reading this tells you whether
	// the drive has actually switched to the mode you commanded (0x6060 RxPDO).
	// Populated only when PdoOpModeDisplayReady=true (YAML has op_mode_display entry).
	// Primary use: confirm Delta ASDA-A2-E is in PV mode (3) during jog, not CSP mode (8).
	PDOOpModeDisplay atomic.Int32 // 0x6061 Op-mode display — written by cyclic task
	PDODebugLog      atomic.Int64 // last debug log timestamp (moved from package-level)

	// Driver is the IMotorDriver implementation for this specific slave.
	// Set by InitMaster() from the drive-type in device-configuration.yml.
	// The cyclic task uses d.Driver.JogControlword() / FaultResetControlword()
	// instead of GetMotorDriver() so each axis uses its own HAL implementation.
	Driver   IMotorDriver
	DomainPD *C.uint8_t

	// ---- TxPDO offsets (drive → master, read each cycle) ----
	OffPos           C.uint // 0x6064 Position actual value   (S32)
	OffStatus        C.uint // 0x6041 Statusword              (U16)
	OffErrorCode     C.uint // 0x603F Error code              (U16)
	OffDigitalInputs C.uint // 0x4F25 Input signal register (vendor, replaces 0x60FD) (U32)
	OffOpModeDisplay C.uint // 0x6061 Op-mode display (S8, TxPDO) — read-back of active mode

	// ---- RxPDO offsets (master → drive, written each cycle) ----
	// All come from PDO 0x1601 (the only RxPDO in the ESI that has 0x60FF).
	OffControlWord C.uint // 0x6040 Controlword             (U16)
	OffOpMode      C.uint // 0x6060 Modes of operation      (S8)
	OffTargetPos   C.uint // 0x607A Target position         (S32)
	OffTargetVel   C.uint // 0x60FF Target velocity         (S32)
	OffDigOutMask  C.uint // 0x60FE:01 Digital output mask  (U32) — physical output enable
	OffDigOutVal   C.uint // 0x60FE:02 Digital output value (U32) — physical output bits
	// --- vendor multiturn reset via async SDO requests ---
	// Uses ec_sdo_request_t objects (registered in SetupPDOPosition).
	// IgH services the mailbox inside ecrt_master_receive() — safe during Op.
	// No PDO domain entries needed. No blocking. No deadlock.
	MTSdoReady bool // true when create_mt_sdo_requests() succeeded
	PdoMTReady bool // always false; multiturn reset is SDO-only
	// pdoMTResetActive: when true, cyclic standby branch writes CW=0x0007
	// (servo disabled) instead of 0x000F. The Panasonic A6 only executes the
	// special function (0x4D00/0x4D01) when servo power is removed.
	// Set/cleared by triggerMultiTurnResetAsync() in reset.go.
	pdoMTResetActive    atomic.Bool
	pdoFaultResetActive atomic.Bool
	// pdoForcedServoCycleActive makes the PDO loop hold CW=0x0006 even when
	// no fault bit is set. System reset uses it to recover a non-faulted drive
	// that is no longer accepting motion commands.
	pdoForcedServoCycleActive atomic.Bool
	// pdoShutdownActive: when true the standby branch writes CW=0x0006 (Shutdown)
	// instead of CW=0x000F. Set by ShutdownMasters before StopPDOCyclic so the
	// running 1ms ticker walks PDS Op Enabled→Ready To Switch On. Only after that
	// is confirmed does StopPDOCyclic fire — preventing Err88.2.
	pdoShutdownActive atomic.Bool
	PdoVelSdoReady    bool           // true when Profile Velocity SDO request registered
	PdoVelSdoReq      unsafe.Pointer // Pointer to the ec_sdo_request_t for 0x6081

	// ---- PDO readiness flags ----
	PdoReady              bool // TxPDO 0x6064 registered
	PdoStatusReady        bool // TxPDO 0x6041 registered
	PdoErrorReady         bool // TxPDO 0x603F registered
	PdoDIReady            bool // TxPDO 0x4F25 registered (input signal register, vendor-specific)
	PdoOpModeDisplayReady bool // TxPDO 0x6061 registered — confirms actual active op-mode
	PdoDigOutReady        bool // RxPDO 0x60FE:01/02 offsets resolved (physical digital outputs)
	PdoRxReady            bool // ALL RxPDO entries registered (0x1600 fully mapped)
	PdoJogReady           bool // RxPDO ready for jog (same as PdoRxReady)
	PdoPosReady           bool // RxPDO ready for position (same as PdoRxReady)

	// Motion enable flags — atomic.Bool (read by 1ms cyclic, written by motion goroutines).
	pdoJogEnabled atomic.Bool // use EnableJogPDO()/IsJogEnabled()
	pdoPosEnabled atomic.Bool // use EnablePosPDO()/IsPosEnabled()

	// ---- Desired RxPDO values (atomics so motion goroutines are race-free) ----
	desiredTargetVelocity atomic.Int32
	desiredControlWord    atomic.Uint32 // U16 stored in low bits
	desiredDigOutMask     atomic.Uint32 // 0x60FE:01 Digital output enable mask (U32)
	desiredDigOutVal      atomic.Uint32 // 0x60FE:02 Digital output values (U32)
	// desiredMTFunc/desiredMTStart removed: multiturn reset uses async SDO requests,
	// not per-cycle PDO writes. See triggerMultiTurnReset() in reset.go.
	desiredOpMode         atomic.Int32 // S8 stored in low bits
	desiredTargetPosition atomic.Int32
	currentTargetPosition atomic.Int32
	ppSetpointPending     atomic.Bool // one-shot pulse for Profile Position new set-point (bit4)
	// posMoveAborted: set to true by emergency() when it cancels an in-flight PP move.
	// hasTargetReached polls this flag and exits immediately instead of waiting for the
	// 30-second timeout. Cleared by hasTargetReached on exit.
	posMoveAborted atomic.Bool

	Position int
	Name     string
	Device   ethercatDevice.Device
}

// RequestMaster requests a master from the IgH kernel module.
// Note: domain and slave config are set up separately in SetupPDOPosition;
// this function intentionally does NOT create them so that the caller can
// choose whether to run in PDO or pure SDO mode.
func RequestMaster(device ethercatDevice.Device) (*C.ec_master_t, error) {
	// FIX (Bug 1): Always pass master index 0 to ecrt_request_master.
	// device.ID is the EtherCAT ring position of the first slave — not the
	// IgH master index. On a standard Pi / x86 setup there is exactly one
	// IgH master (index 0) regardless of how many drives are on the bus.
	// Using device.ID here accidentally worked when position==0 but silently
	// failed (returned nil master) for any deployment where the first slave
	// is at position 1 or higher, or when two masters are in use.
	master0 := C.ecrt_request_master(0)
	if master0 == nil {
		return nil, errors.New("unable to find EtherCAT master")
	}
	return master0, nil
}

// SDODownload performs an ecrt_master_sdo_download write to the drive.
// Retries up to 5 times on failure.
func SDODownload(master *C.ec_master_t, position int, step ethercatDevice.Step) error {
	valueToWrite, errValue := step.GetValue()
	if errValue != nil {
		return errValue
	}
	abortCode := 0
	dataSize := getSize(step)
	logger.Trace("SDODownload →", step.Name,
		fmt.Sprintf("Addr:0x%04X Sub:0x%02X Val:%d Type:%s",
			step.Address, step.SubIndex, valueToWrite, step.DataType))

	var uploadErr error
	for retry := 0; retry < 5; retry++ {
		result := C.sdo_download(
			master,
			C.uint16_t(position),
			C.uint16_t(step.Address),
			C.uint8_t(step.SubIndex),
			(*C.uint8_t)(unsafe.Pointer(&valueToWrite)),
			dataSize,
			(*C.uint32_t)(unsafe.Pointer(&abortCode)),
		)
		if result >= 0 {
			uploadErr = nil
			break
		}
		uploadErr = fmt.Errorf("SDODownload failed for step %q (retry %d)", step.Name, retry)
		time.Sleep(2 * time.Millisecond)
		logger.Info("SDODownload retry", step.Name, "attempt", retry+1)
	}

	if step.Delay > 0 {
		time.Sleep(time.Duration(step.Delay) * time.Microsecond)
	}
	if uploadErr != nil {
		logger.Error(uploadErr)
		statusnotifier.Alarm("Error sending command to driver, step " + step.Name)
	}
	return uploadErr
}

// runSDOOperation executes all steps of a named EtherCAT operation via SDO.
// Reads use SDOUpload2, writes use SDODownload.
// This replaces the repeated "for _, step := range operation.Steps" pattern.
func runSDOOperation(master *C.ec_master_t, position int, operation ethercatDevice.Operation) {
	for _, step := range operation.Steps {
		if step.Action == "read" {
			SDOUpload2(master, position, step)
		} else {
			SDODownload(master, position, step)
		}
	}
}

// SDOUpload performs an ecrt_master_sdo_upload read from the drive.
// Uses correctly sized buffers for each data type so multi-byte
// reads are not silently truncated.
func SDOUpload(master *C.ec_master_t, position int, step ethercatDevice.Step) (int32, error) {
	abortCode := 0
	var toReturn int32

	switch step.DataType {
	case "U32":
		var v C.uint32_t
		ret := C.sdo_upload(master, C.uint16_t(position), C.uint16_t(step.Address),
			C.uint8_t(step.SubIndex), (*C.uint8_t)(unsafe.Pointer(&v)),
			C.uint32Size(), (*C.uint32_t)(unsafe.Pointer(&abortCode)))
		if ret < 0 {
			return 0, fmt.Errorf("SDOUpload U32 failed: %s", C.GoString(C.strerror(-ret)))
		}
		toReturn = int32(v)
	case "U16":
		var v C.uint16_t
		ret := C.sdo_upload(master, C.uint16_t(position), C.uint16_t(step.Address),
			C.uint8_t(step.SubIndex), (*C.uint8_t)(unsafe.Pointer(&v)),
			C.uint16Size(), (*C.uint32_t)(unsafe.Pointer(&abortCode)))
		if ret < 0 {
			return 0, fmt.Errorf("SDOUpload U16 failed: %s", C.GoString(C.strerror(-ret)))
		}
		toReturn = int32(v)
	case "I32":
		var v C.int32_t
		ret := C.sdo_upload(master, C.uint16_t(position), C.uint16_t(step.Address),
			C.uint8_t(step.SubIndex), (*C.uint8_t)(unsafe.Pointer(&v)),
			C.int32Size(), (*C.uint32_t)(unsafe.Pointer(&abortCode)))
		if ret < 0 {
			return 0, fmt.Errorf("SDOUpload I32 failed: %s", C.GoString(C.strerror(-ret)))
		}
		toReturn = int32(v)
	case "I16":
		var v C.int16_t
		ret := C.sdo_upload(master, C.uint16_t(position), C.uint16_t(step.Address),
			C.uint8_t(step.SubIndex), (*C.uint8_t)(unsafe.Pointer(&v)),
			C.int16Size(), (*C.uint32_t)(unsafe.Pointer(&abortCode)))
		if ret < 0 {
			return 0, fmt.Errorf("SDOUpload I16 failed: %s", C.GoString(C.strerror(-ret)))
		}
		toReturn = int32(v)
	default: // U8 / I8 / UINT / fallback
		var v C.uint8_t
		ret := C.sdo_upload(master, C.uint16_t(position), C.uint16_t(step.Address),
			C.uint8_t(step.SubIndex), (*C.uint8_t)(unsafe.Pointer(&v)),
			C.uint8Size(), (*C.uint32_t)(unsafe.Pointer(&abortCode)))
		if ret < 0 {
			return 0, fmt.Errorf("SDOUpload U8 failed: %s", C.GoString(C.strerror(-ret)))
		}
		toReturn = int32(v)
	}

	if step.Delay > 0 {
		time.Sleep(time.Duration(step.Delay) * time.Microsecond)
	}
	return toReturn, nil
}

// DrivePosition reads a drive parameter using drivePosition (SDO upload wrapper).
func DrivePosition(master *C.ec_master_t, position int, step ethercatDevice.Step) (int32, error) {
	valueToWrite, errValue := step.GetValue()
	if errValue != nil {
		return 0, errValue
	}
	pos := C.drivePosition(master,
		C.uint16_t(position),
		C.uint16_t(step.Address),
		C.uint8_t(step.SubIndex),
		C.uint8_t(valueToWrite),
	)
	return int32(pos), nil
}

// SDOUpload2 is a convenience wrapper around SDOUpload returning int.
func SDOUpload2(master *C.ec_master_t, position int, step ethercatDevice.Step) (int, error) {
	v, err := SDOUpload(master, position, step)
	if err != nil {
		return 0, err
	}
	if step.Delay > 0 {
		time.Sleep(time.Duration(step.Delay) * time.Microsecond)
	}
	return int(v), nil
}

func getSize(step ethercatDevice.Step) C.size_t {
	switch step.DataType {
	case "U32":
		return C.uint32Size()
	case "U16":
		return C.uint16Size()
	case "U8":
		return C.uint8Size()
	case "UINT":
		return C.unintSize()
	case "I16":
		return C.int16Size()
	case "I32":
		return C.int32Size()
	case "I8":
		return C.int8Size()
	default:
		return C.uint8Size()
	}
}

// ============================================================
// PDO motion control helpers
// ============================================================

// IsJogEnabled returns true when the PDO jog (velocity) mode is active.
// Safe to call from any goroutine including the 1ms cyclic task.
func (d *MasterDevice) IsJogEnabled() bool { return d.pdoJogEnabled.Load() }

// IsPosEnabled returns true when the PDO position mode is active.
// Safe to call from any goroutine including the 1ms cyclic task.
func (d *MasterDevice) IsPosEnabled() bool { return d.pdoPosEnabled.Load() }

// EnableJogPDO activates or deactivates cyclic velocity (jog) output.
// Atomically clears pos-enabled when activating jog (mutual exclusion).
func (d *MasterDevice) EnableJogPDO(enabled bool) {
	d.pdoJogEnabled.Store(enabled)
	if enabled {
		d.pdoPosEnabled.Store(false)
	}
}

// SetJogPDOSetpoints updates all three jog-related RxPDO values atomically.
// The values are written to the drive in the next cyclic tick.
func (d *MasterDevice) SetJogPDOSetpoints(controlWord uint16, opMode int8, targetVelocity int32) error {
	if !d.PdoJogReady {
		return fmt.Errorf("SetJogPDOSetpoints: PdoJogReady=false — RxPDO not configured")
	}
	d.desiredControlWord.Store(uint32(controlWord))
	d.desiredOpMode.Store(int32(opMode))
	d.desiredTargetVelocity.Store(targetVelocity)
	return nil
}

// SetTargetVelocityPDO updates only the target velocity; CW and mode are
// managed by the cyclic state machine.
func (d *MasterDevice) SetTargetVelocityPDO(targetVelocity int32) error {
	if !d.PdoJogReady {
		return fmt.Errorf("SetTargetVelocityPDO: PdoJogReady=false — RxPDO not configured")
	}
	d.desiredTargetVelocity.Store(targetVelocity)
	return nil
}

// EnablePosPDO activates or deactivates cyclic position (PP) output.
// Atomically clears jog-enabled when activating pos (mutual exclusion).
// Arms the one-shot set-point pulse when enabling.
func (d *MasterDevice) EnablePosPDO(enabled bool) {
	d.pdoPosEnabled.Store(enabled)
	if enabled {
		d.pdoJogEnabled.Store(false)
		// arm one-shot set-point pulse for Profile Position mode (opmode 1)
		d.ppSetpointPending.Store(true)
	}
}

// SetTargetPositionPDO sets the goal position for the cyclic position ramp.
func (d *MasterDevice) SetTargetPositionPDO(targetPosition int32) error {
	if !d.PdoPosReady {
		return fmt.Errorf("SetTargetPositionPDO: PdoPosReady=false — RxPDO not configured")
	}
	d.desiredTargetPosition.Store(targetPosition)
	// ensure next cyclic tick triggers a new set-point pulse (PP mode)
	d.ppSetpointPending.Store(true)
	return nil
}

// ============================================================
// Low-level SDO helpers for pre-activation use in setupDrivers.
// These bypass the Step/YAML layer so callers don't need cgo imports.
// ONLY valid before ecrt_master_activate() — after activation use PDO atomics.
// ============================================================

// sdoReadStatusword reads CiA-402 object 0x6041 (statusword) via SDO.
// Pre-activation only — after activation call the per-device PDOStatus field.
func sdoReadStatusword(master *C.ec_master_t, position int) (uint16, error) {
	var v C.uint16_t
	var abortCode C.uint32_t
	ret := C.sdo_upload(
		master,
		C.uint16_t(position),
		0x6041,
		0x00,
		(*C.uint8_t)(unsafe.Pointer(&v)),
		C.uint16Size(),
		&abortCode,
	)
	if ret < 0 {
		return 0, fmt.Errorf("sdoReadStatusword: sdo_upload failed ret=%d abort=0x%08X",
			int(ret), uint32(abortCode))
	}
	return uint16(v), nil
}

// sdoWriteControlword writes CiA-402 object 0x6040 (controlword) via SDO.
// Pre-activation only — after activation the PDO cyclic task owns 0x6040.
func sdoWriteControlword(master *C.ec_master_t, position int, cw uint16) error {
	v := C.uint16_t(cw)
	var abortCode C.uint32_t
	ret := C.sdo_download(
		master,
		C.uint16_t(position),
		0x6040,
		0x00,
		(*C.uint8_t)(unsafe.Pointer(&v)),
		C.uint16Size(),
		&abortCode,
	)
	if ret < 0 {
		return fmt.Errorf("sdoWriteControlword: sdo_download 0x%04X failed ret=%d abort=0x%08X",
			cw, int(ret), uint32(abortCode))
	}
	return nil
}