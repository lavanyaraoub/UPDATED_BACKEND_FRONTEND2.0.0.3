package motordriver

import (
	ethercatDevice "EtherCAT/ethercatdevicedatatypes"
	"errors"
)

// ErrNotSupported is returned by optional HAL methods the drive does not implement.
var ErrNotSupported = errors.New("operation not supported by this drive")

// IMotorDriver is the Hardware Abstraction Layer for all CiA-402 drives.
//
// ── HOW TO ADD A NEW DRIVE ────────────────────────────────────────────────────
// 1. Create motordriver/<newdrive>_motor_driver.go implementing this interface.
// 2. Register the drive in motor_driver_factory.go (one line in the map).
// 3. Create configs/<newdrive>.yml with the required operations.
// 4. Add the drive entry in configs/device-configuration.yml.
//
// NO OTHER FILES need to be modified. All motion logic files (drive_rotation.go,
// pdo_cyclic_task.go, drive_power.go, reset.go, zero_reference.go) are frozen.
// ─────────────────────────────────────────────────────────────────────────────
type IMotorDriver interface {

	// ── Legacy SDO-based methods (retained for interface compatibility) ────────
	hasTargetReached(masterDevice *MasterDevice, action int, immediate int,
		operation ethercatDevice.Operation) error
	potNotEnabled(masterDevice *MasterDevice) (bool, error)
	readDeclampSignal(masterDevice *MasterDevice, declampTiming int) (bool, error)
	readClampSignal(masterDevice *MasterDevice, clampTiming int) (bool, error)
	receivedECS(masterDevice *MasterDevice, operation ethercatDevice.Operation,
		stopECSChat chan bool) int
	receivedECSZero(masterDevice *MasterDevice, operation ethercatDevice.Operation,
		stopECSChat chan bool) int
	sendFinishSignal(masterDevice *MasterDevice, operation ethercatDevice.Operation) error
	pollIOStat(avilableDevices []*MasterDevice)
	stopPollIOStat()

	// ── PDO setup ─────────────────────────────────────────────────────────────
	// SetupPDO registers all PDO entries, sizes the domain, resolves all byte
	// offsets and sets all Pdo*Ready flags on dev.
	// Called once during InitMaster, before ecrt_master_activate.
	SetupPDO(dev *MasterDevice) error

	// ── PP target-reached logic ───────────────────────────────────────────────
	// IsTargetReached returns true when the drive has completed a Profile
	// Position move, given the current statusword sw.
	//
	//   Panasonic A6    : bit10=1 AND bit12=0  (strict Set-Point Ack handshake)
	//   Delta ASDA-A2-E : bit10=1 only         (bit12 clears inconsistently —
	//                                            including it causes a 30s hang)
	IsTargetReached(sw uint16) bool

	// ── Jog controlword fixup ─────────────────────────────────────────────────
	// JogControlword applies drive-specific bit adjustments to the base
	// controlword for Profile Velocity (jog) mode before it is written to the
	// RxPDO OffControlWord field each cycle.
	//
	//   Panasonic A6    : returns cwBase unchanged.
	//   Delta ASDA-A2-E : forces ramp bits 4,5,6 HIGH and Halt bit8 LOW.
	JogControlword(cwBase uint16) uint16

	// ── Fault reset controlword ───────────────────────────────────────────────
	// FaultResetControlword returns the CiA-402 controlword to write when
	// performing a fault reset (rising edge on bit 7 clears the fault).
	//
	//   Panasonic A6    : 0x0080 — bit 7 alone is sufficient per CiA-402 standard.
	//   Delta ASDA-A2-E : 0x008F — bit 7 PLUS bits 0-3 required simultaneously.
	//                     The Delta YAML faultReset operation uses 0x8F (10001111b).
	//                     Sending only 0x0080 leaves the Delta stuck in fault state
	//                     permanently — SW bit 3 never clears. This was the root
	//                     cause of "Hardware Emergency pressed" appearing on Delta
	//                     even though no hardware E-stop button exists on that drive.
	//
	// Used by cia402NextControlword() in pdo_cyclic_task.go.
	FaultResetControlword() uint16

	// ── Standby operation mode ────────────────────────────────────────────────
	// StandbyOpMode returns the CiA-402 operation mode the drive should hold
	// when no motion command is active (jog off, pos off). Written to 0x6060
	// every 1ms cycle by the standby branch of pdo_cyclic_task.go.
	//
	//   Panasonic A6    : 3 (Profile Velocity, vel=0) — mode 8 (CSP) triggers
	//                     a correction-torque pulse on A6, causing visible jerk.
	//   Delta ASDA-A2-E : 3 (Profile Velocity, vel=0) — same reason.
	//   New drive       : return whatever mode that drive holds position silently.
	StandbyOpMode() int8

	// ── Multi-turn encoder reset ──────────────────────────────────────────────
	// ResetMultiTurn performs the complete multi-turn encoder reset sequence for
	// this specific drive. All routing, protocol, and drive-specific objects are
	// handled entirely inside this method.
	//
	//   Panasonic A6    : async ec_sdo_request_t (0x4D01/0x4D00). PDO stays
	//                     running. Drive must be power-cycled after.
	//   Delta ASDA-A2-E : blocking SDO writes P2-08=271 then P2-71=1. PDO cyclic
	//                     keeps running (cyclic services the mailbox). Drive must
	//                     be power-cycled after.
	//   Unsupported     : return ErrNotSupported immediately.
	//
	// Replaces the routing logic that was in reset.go. Adding a new drive with a
	// different reset mechanism never requires touching reset.go.
	ResetMultiTurn(availableDevices []*MasterDevice) error
}
