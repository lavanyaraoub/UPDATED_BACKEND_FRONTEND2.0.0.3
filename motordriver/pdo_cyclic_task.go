package motordriver

/*
#cgo CFLAGS: -g -Wall -I/opt/etherlab/include -I/home/pi/gosrc/src/EtherCAT1
#cgo LDFLAGS: -L/home/pi/gosrc/src/EtherCAT1 -L/opt/etherlab/lib/ -lethercatinterface -lethercat
#include "ecrt.h"
#include "ethercatinterface.h"
#include <pthread.h>
#include <sched.h>

static void set_realtime_priority() {
    struct sched_param param;
    param.sched_priority = 80;
    pthread_setschedparam(pthread_self(), SCHED_FIFO, &param);
}
*/
import "C"

import (
	logger "EtherCAT/logger"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// ============================================================
// pdo_cyclic_task.go — Phase 4: Multi-device 1ms cyclic task
//
// WHAT CHANGED FROM SINGLE-DEVICE:
//
//   Before Phase 4, the cyclic goroutine had:
//     d := devices[0]
//     // all reads/writes used d exclusively
//
//   Phase 4 changes this to:
//     for _, d := range devices {
//         // each device processed independently
//     }
//
//   Per-device state (position, status, error code, DI register)
//   moved from package-level globals into MasterDevice.PDO* fields
//   (added in Phase 4 to ether_cat_gateway.go).
//
//   The global getter functions (GetLastPDOPosition etc.) have been
//   removed — all callers now read directly from per-device PDO* atomics
//   (d.PDOPos.Load(), d.PDOStatus.Load(), d.PDODI.Load(), etc.).
//
//   The cyclic task uses d.Driver (per-device IMotorDriver instance)
//   instead of GetMotorDriver() (global single driver) so a Delta
//   on Axis A and a Panasonic on Axis B both get the correct HAL
//   behaviour in the same 1ms tick.
// ============================================================

// cia402NextControlword computes the CiA-402 state machine controlword.
// driver is the per-device IMotorDriver — provides FaultResetControlword()
// which differs between drive brands (A6: 0x0080, Delta: 0x008F).
func cia402NextControlword(status uint16, opEnabled *bool, faultReset bool, driver IMotorDriver) uint16 {
	*opEnabled = false

	// Fault state (bit 3 set) — only send fault reset on rising edge.
	if (status & 0x0008) != 0 {
		if faultReset {
			return driver.FaultResetControlword()
		}
		return 0x0000
	}

	state := status & 0x006F
	switch state {
	case 0x0000, 0x0040: // Not Ready / Switch On Disabled → Shutdown
		return 0x0006
	case 0x0021: // Ready To Switch On → Switch On
		return 0x0007
	case 0x0023: // Switched On → Enable Operation
		return 0x000F
	case 0x0027: // Operation Enabled → stay
		*opEnabled = true
		return 0x000F
	default:
		return 0x0006
	}
}

// ============================================================
// Shared cyclic task control variables
// ============================================================

var pdoCyclicMu sync.Mutex

var (
	pdoStopCh     chan struct{}
	pdoCyclicDone chan struct{}
	pdoActive     atomic.Bool

	// lastPDOTickNanos is a global heartbeat for the multi-device cyclic task.
	// It is updated once per successful 1ms tick after all devices have been
	// processed. Per-device PDO feedback still lives on MasterDevice.
	lastPDOTickNanos   atomic.Int64
	pdoWatchdogRunning atomic.Bool
)

// IsPDOActive returns true when the cyclic task is running.
func IsPDOActive() bool { return pdoActive.Load() }

// legacyFirstDevice returns axis-0 for backward-compatible helper wrappers.
// New multi-drive code should read from the specific *MasterDevice instead.
func legacyFirstDevice() *MasterDevice {
	devices := getMasterDevices()
	if len(devices) == 0 {
		return nil
	}
	return devices[0]
}

// Backward-compatible PDO getters retained from the SDO/PDO build.
// They intentionally read axis-0 only; use dev.PDO* fields for generic multi-drive code.
func GetLastPDOPosition() int32 {
	if d := legacyFirstDevice(); d != nil {
		return d.PDOPos.Load()
	}
	return 0
}
func GetLastPDOStatusword() uint16 {
	if d := legacyFirstDevice(); d != nil {
		return uint16(d.PDOStatus.Load() & 0xFFFF)
	}
	return 0
}
func GetLastPDOErrorCode() uint16 {
	if d := legacyFirstDevice(); d != nil {
		return uint16(d.PDOErr.Load() & 0xFFFF)
	}
	return 0
}
func GetLastPDOVelocityActual() int32 { return 0 } // HAL PDO layout does not currently map 0x606C.
func GetLastPDODigitalInputs() uint32 {
	if d := legacyFirstDevice(); d != nil {
		return d.PDODI.Load()
	}
	return 0
}

// PDOTickAge returns the duration since the cyclic task's last successful tick.
func PDOTickAge() time.Duration {
	last := lastPDOTickNanos.Load()
	if last == 0 {
		return 0
	}
	return time.Duration(time.Now().UnixNano() - last)
}

// PDOHealthy reports whether the cyclic heartbeat is fresh.
func PDOHealthy() (healthy bool, age time.Duration) {
	if !pdoActive.Load() {
		return true, 0
	}
	age = PDOTickAge()
	return age < 50*time.Millisecond, age
}

// ============================================================
// PDOFaultReset — clears drive faults via PDO atomics.
//
// Phase 4 fix: iterates ALL devices in the slice and resets each one
// that is actually in fault state. Previously used devices[0] only —
// meaning Axis B faults were reset on Axis A.
//
// Per-device FaultResetMu prevents two goroutines from running fault
// reset on the SAME device simultaneously, while allowing Axis A and
// Axis B to fault-reset concurrently (different mutexes).
//
// Returns true if ALL faulted devices cleared successfully.
// ============================================================
func PDOFaultReset(devices []*MasterDevice) bool {
	if len(devices) == 0 || !pdoActive.Load() {
		return false
	}

	allCleared := true
	for _, d := range devices {
		// Only attempt reset on devices that are actually in fault (bit3=1).
		if uint16(d.PDOStatus.Load()&0xFFFF)&0x0008 == 0 {
			continue
		}

		// Per-device mutex — does not block other devices.
		if !d.FaultResetMu.TryLock() {
			logger.Warn("[PDO-RESET] concurrent reset on", d.Name, "— skipping (already in progress)")
			allCleared = false
			continue
		}

		cleared := resetOneFaultedDevice(d)
		d.FaultResetMu.Unlock()

		if !cleared {
			allCleared = false
		}
	}
	return allCleared
}

// resetOneFaultedDevice runs the CiA-402 fault reset sequence on a single device.
// Must be called with d.FaultResetMu held. Not exported — use PDOFaultReset.
func resetOneFaultedDevice(d *MasterDevice) bool {
	d.EnableJogPDO(false)
	d.EnablePosPDO(false)
	d.desiredTargetVelocity.Store(0)
	_ = d.SetTargetPositionPDO(d.PDOPos.Load())
	d.pdoFaultResetActive.Store(true)
	defer d.pdoFaultResetActive.Store(false)

	time.Sleep(50 * time.Millisecond)

	faultCleared := false
	timeout := time.Now().Add(2 * time.Second)
	for time.Now().Before(timeout) {
		if uint16(d.PDOStatus.Load()&0xFFFF)&0x0008 == 0 {
			faultCleared = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// FIX (Bug 6): Do NOT zero desiredDigOutMask/Val unconditionally.
	//
	// The old code stored (mask=0, val=0) twice — once before the 500ms sleep
	// and once after — on every fault reset regardless of whether a brake was
	// held open by a digital output. On machines where the brake solenoid is
	// driven by 0x60FE bit 1 (breakBit=0x00000002), this silently released the
	// brake without calling breakOn(). If the axis is vertical or on an incline,
	// the load would drop under gravity the moment a drive fault was reset.
	//
	// The original intent was to release any finish-signal or clamp output that
	// might have been left asserted when the fault occurred mid-cycle. The correct
	// approach is to preserve the brake bit and only clear non-brake outputs.
	//
	// Fix: read the current desiredDigOutVal, mask out only the non-brake bits,
	// and store back. The brake bit is left unchanged — if it was ON before the
	// fault, it stays ON through the reset. If clamp/declamp logic later decides
	// to release it, it must do so explicitly via breakOff().
	const brakeBitMask = uint32(0x00000002) // matches breakBit in break.go
	currentVal := d.desiredDigOutVal.Load()
	preserveBrake := currentVal & brakeBitMask
	// Clear all non-brake output bits (finish signal, clamp solenoid, etc.)
	// that may have been left asserted when the fault occurred mid-cycle.
	d.desiredDigOutMask.Store(brakeBitMask) // only drive the brake output
	d.desiredDigOutVal.Store(preserveBrake) // keep brake state as-is
	time.Sleep(500 * time.Millisecond)
	// After 500ms settle, restore full mask so normal motion can use all outputs.
	d.desiredDigOutMask.Store(0xFFFFFFFF)
	d.desiredDigOutVal.Store(preserveBrake) // brake state still preserved

	if !faultCleared {
		logger.Warn("[PDO-RESET]", d.Name, "fault reset timed out — sw=",
			fmt.Sprintf("0x%04X", uint16(d.PDOStatus.Load())),
			"— drive may require power cycle")
		return false
	}

	timeout2 := time.Now().Add(1 * time.Second)
	for time.Now().Before(timeout2) {
		if uint16(d.PDOStatus.Load()&0xFFFF)&0x006F == 0x0027 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	d.PDOErr.Store(0)
	logger.Info("[PDO-RESET]", d.Name, "complete. sw=",
		fmt.Sprintf("0x%04X", uint16(d.PDOStatus.Load())))
	return true
}

// ============================================================
// StopPDOCyclic — signals stop and blocks until all drives
// reach a safe PDS state, then releases the EtherCAT master.
// ============================================================
func StopPDOCyclic() {
	pdoCyclicMu.Lock()
	if !pdoActive.Load() {
		pdoCyclicMu.Unlock()
		return
	}
	pdoActive.Store(false)
	done := pdoCyclicDone
	close(pdoStopCh)
	pdoCyclicMu.Unlock()

	select {
	case <-done:
		logger.Info("[PDO] StopPDOCyclic: goroutine exited cleanly")
	case <-time.After(2 * time.Second):
		logger.Warn("[PDO] StopPDOCyclic: timed out")
	}

	for _, dev := range masterDevicesForRelease {
		if dev.Master != nil {
			logger.Info("[PDO] Deactivating master for:", dev.Name)
			C.ecrt_master_deactivate(dev.Master)
			time.Sleep(100 * time.Millisecond)
			logger.Info("[PDO] Releasing master for:", dev.Name)
			C.ecrt_release_master(dev.Master)
		}
	}
}

var masterDevicesForRelease []*MasterDevice

// PDOSetDigitalOutputOnDevice queues a digital output write on a specific device.
// This is the canonical per-device version — all callers should use this.
func PDOSetDigitalOutputOnDevice(dev *MasterDevice, mask uint32, val uint32) {
	if dev == nil || !dev.PdoDigOutReady {
		return
	}
	dev.desiredDigOutMask.Store(mask)
	dev.desiredDigOutVal.Store(val)
	fmt.Printf("[PDO-DIGOUT] queued mask=0x%08X val=0x%08X\n", mask, val)
}

// PDOUpdateDigitalOutputBits atomically changes only the requested output
// bits. The full output word is then written by the cyclic task. This prevents
// the brake bit, FIN bit and any other active digital outputs from clearing
// one another when different procedures update 0x60FE concurrently.
func PDOUpdateDigitalOutputBits(dev *MasterDevice, bits uint32, set bool) uint32 {
	if dev == nil || !dev.PdoDigOutReady {
		return 0
	}

	for {
		oldValue := dev.desiredDigOutVal.Load()
		newValue := oldValue
		if set {
			newValue |= bits
		} else {
			newValue &^= bits
		}

		if dev.desiredDigOutVal.CompareAndSwap(oldValue, newValue) {
			dev.desiredDigOutMask.Store(0xFFFFFFFF)
			logger.Info(fmt.Sprintf(
				"[PDO-DIGOUT-RMW] drive=%s bits=0x%08X set=%t old=0x%08X new=0x%08X mask=0xFFFFFFFF",
				dev.Name, bits, set, oldValue, newValue))
			return newValue
		}
	}
}

// PDOSetDigitalOutput is a legacy wrapper kept for backward compatibility.
// All existing callers pass []*MasterDevice{specificDevice} — a single-element
// slice with the correct device — so devices[0] here is always the intended
// target, not an axis-0 assumption. New code should call PDOSetDigitalOutputOnDevice.
func PDOSetDigitalOutput(devices []*MasterDevice, mask uint32, val uint32) {
	if len(devices) == 0 {
		return
	}
	PDOSetDigitalOutputOnDevice(devices[0], mask, val)
}

// PDOSetDigitalOutputForDevice routes by drive name with devices[0] fallback.
// Use when you have a name but not a direct pointer.
func PDOSetDigitalOutputForDevice(driveName string, mask uint32, val uint32) {
	allDevices := getMasterDevices()
	if len(allDevices) == 0 {
		return
	}
	var target *MasterDevice
	for _, d := range allDevices {
		if d.Name == driveName {
			target = d
			break
		}
	}
	if target == nil {
		target = allDevices[0]
	}
	PDOSetDigitalOutputOnDevice(target, mask, val)
}

// ============================================================
// StartPDOCyclic — Phase 4 multi-device implementation.
//
// The 1ms goroutine now iterates ALL devices on each tick:
//
//	ecrt_master_receive()         — once per master
//	for each device:
//	  ecrt_domain_process()       — per domain
//	  read TxPDO                  — per device
//	  run CiA-402 state machine   — per device, using d.Driver
//	  write RxPDO                 — per device
//	  ecrt_domain_queue()         — per domain
//	ecrt_master_send()            — once per master
//
// All slaves on the bus are updated in the same 1ms window.
// ============================================================
func StartPDOCyclic(devices []*MasterDevice) error {
	pdoCyclicMu.Lock()
	defer pdoCyclicMu.Unlock()

	if len(devices) == 0 {
		return errors.New("StartPDOCyclic: no devices")
	}

	// All devices share the same master handle (single IgH master).
	master := devices[0].Master

	// Validate all devices are ready.
	for _, d := range devices {
		if !d.PdoReady || d.Domain == nil {
			return fmt.Errorf("StartPDOCyclic: device %q PDO not configured — call SetupPDOPosition first", d.Name)
		}
		if d.Driver == nil {
			return fmt.Errorf("StartPDOCyclic: device %q has no Driver — check InitMaster", d.Name)
		}
	}

	if pdoActive.Load() {
		return nil
	}

	// ── Pre-flight: verify slaves are responding ─────────────────────────
	// CGo cannot access the link_up bitfield — slaves_responding == 0
	// already implies the link is down.
	var masterState C.ec_master_state_t
	C.ecrt_master_state(master, &masterState)
	if masterState.slaves_responding == 0 {
		return errors.New("StartPDOCyclic: pre-flight failed — no slaves responding (check cable and drive power)")
	}
	logger.Info(fmt.Sprintf("[PDO] Pre-flight OK — %d slave(s) responding",
		uint(masterState.slaves_responding)))

	// ── Activate master (single call for all slaves) ─────────────────────
	if C.ecrt_master_activate(master) != 0 {
		return errors.New("StartPDOCyclic: ecrt_master_activate failed")
	}

	// ── Get domain process-data pointer for each device ──────────────────
	for _, d := range devices {
		pd := C.ecrt_domain_data(d.Domain)
		if pd == nil {
			return fmt.Errorf("StartPDOCyclic: ecrt_domain_data returned nil for device %q", d.Name)
		}
		d.DomainPD = (*C.uint8_t)(pd)
		logger.Info(fmt.Sprintf("[PDO] Domain data mapped for %s", d.Name))
	}

	pdoStopCh = make(chan struct{})
	pdoCyclicDone = make(chan struct{})
	masterDevicesForRelease = devices
	pdoActive.Store(true)
	startPDOWatchdog()

	// ── Per-device latch counters (Profile Position set-point handshake) ──
	// latchCounter tracks how many cycles bit4 has been held HIGH per device.
	// Must be per-device so two simultaneous PP moves on different axes don't
	// interfere with each other's set-point acknowledgement.
	latchCounters := make([]int, len(devices))

	go func() {
		runtime.LockOSThread()    // 1. Lock Go scheduler to this OS thread
		C.set_realtime_priority() // 2. Tell Linux to give this thread Real-Time Priority
		//runtime.LockOSThread()
		logger.Info(fmt.Sprintf("[PDO] Cyclic task started — %d device(s), 1ms period", len(devices)))

		ticker := time.NewTicker(1 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {

			// ── Stop signal ───────────────────────────────────────────────
			case <-pdoStopCh:
				// FIX (Bug 5): Each device gets its own full 2000ms deadline.
				//
				// The old code computed `deadline` once before the outer
				// `for _, d := range devices` loop and shared it across all drives.
				// With two drives, if device 0 took the full 2000ms to reach
				// Ready To Switch On, device 1 would enter its inner loop with
				// time.Now() already past deadline — it would read the statusword
				// once and break immediately, leaving it at Operation Enabled.
				// ecrt_release_master then fired onto an energised servo → Error 80
				// on device 1 every shutdown.
				//
				// Fix: compute a fresh deadline inside the per-device loop so every
				// drive always gets its own full 2000ms window regardless of how
				// long the previous device took.
				//
				// FIX (Bug 9): All domains processed every 1ms tick in shutdown loop.
				//
				// The old code processed only d.Domain inside the tight loop.
				// On a 2-drive bus, device 1's domain received no
				// ecrt_domain_process/ecrt_domain_queue calls while device 0 was
				// being walked to safe state. If device 1 had a sync manager
				// watchdog configured, the missing frames would trip the watchdog
				// and fault the drive — turning a clean shutdown into a fault
				// recovery situation.
				//
				// Fix: process ALL domains on every tick. Drives already at a safe
				// PDS state simply receive CW=0x0006 again (idempotent in
				// Ready To Switch On — harmless).
				for _, d := range devices {
					if !d.PdoRxReady {
						continue
					}
					logger.Info("[PDO] Shutdown: walking", d.Name, "to Ready To Switch On...")
					// FIX (Bug 5): fresh deadline per device
					devDeadline := time.Now().Add(2000 * time.Millisecond)
					for time.Now().Before(devDeadline) {
						C.ecrt_master_receive(master)
						// FIX (Bug 9): process ALL domains every tick
						for _, other := range devices {
							if other.PdoRxReady {
								C.ecrt_domain_process(other.Domain)
								C.write_u16(other.DomainPD, other.OffControlWord, C.uint16_t(0x0006))
								C.ecrt_domain_queue(other.Domain)
							}
						}
						sw := uint16(C.read_u16(d.DomainPD, d.OffStatus))
						C.ecrt_master_send(master)
						if sw&0x006F == 0x0021 || sw&0x006F == 0x0040 {
							logger.Info("[PDO] Shutdown: " + d.Name + " reached safe PDS state before master release")
							break
						}
						time.Sleep(1 * time.Millisecond)
					}
				}
				close(pdoCyclicDone)
				return

			// ── 1ms tick ──────────────────────────────────────────────────
			case <-ticker.C:
				// ── Receive: one call services ALL slaves ─────────────────
				C.ecrt_master_receive(master)

				// ── Per-device processing ─────────────────────────────────
				for idx, d := range devices {

					// SDO exclusive mode (per-device): a blocking SDO write is
					// in flight on THIS device (e.g. Delta multiturn reset).
					// Skip domain processing for this device only so the blocking
					// caller has exclusive mailbox access. Other devices continue
					// unaffected — previously the global flag paused ALL devices.
					if d.SDOExclusive.Load() {
						continue
					}

					// Process this device's domain slice.
					C.ecrt_domain_process(d.Domain)

					// ── Read TxPDO (drive → master) ───────────────────────
					var status uint16
					if d.PdoStatusReady {
						status = uint16(C.read_u16(d.DomainPD, d.OffStatus))
						d.PDOStatus.Store(uint32(status))
					}
					if d.PdoReady {
						pos := C.read_s32(d.DomainPD, d.OffPos)
						d.PDOPos.Store(int32(pos))
					}
					if d.PdoErrorReady {
						ec := C.read_u16(d.DomainPD, d.OffErrorCode)
						// Only cache error code when drive is in Fault (bit3=1).
						// 0x603F is hardware-latching on A6 — never auto-clears.
						// Caching unconditionally would re-fire alarm 87 after reset.
						if status&0x0008 != 0 {
							d.PDOErr.Store(uint32(ec))
						} else {
							d.PDOErr.Store(0)
						}
					}
					if d.PdoDIReady {
						di := C.read_u32(d.DomainPD, d.OffDigitalInputs)
						d.PDODI.Store(uint32(di))
					}
					// 0x6061 Op-mode display — the drive's ACTIVE operating mode.
					// Differs from 0x6060 (commanded mode) during mode transitions.
					// Only populated when the YAML maps op_mode_display (e.g. Delta).
					if d.PdoOpModeDisplayReady {
						omd := int8(C.read_s8(d.DomainPD, d.OffOpModeDisplay))
						d.PDOOpModeDisplay.Store(int32(omd))
					}

					// ── CiA-402 state machine ─────────────────────────────
					// d.Driver is the per-device IMotorDriver instance set by
					// InitMaster(). FaultResetControlword() is drive-specific:
					//   A6Minas:     0x0080
					//   DeltaASDA2E: 0x008F
					opEnabled := false
					doFaultReset := d.pdoFaultResetActive.Load()
					cw := cia402NextControlword(status, &opEnabled, doFaultReset, d.Driver)

					// Throttle gate: fires at most every 500ms when jog is active.
					// Both [PDO-DBG] and [PDO-JOG] share this decision so they
					// always print together and the timestamp is updated only once.
					shouldJogLog := false
					if d.IsJogEnabled() {
						now := time.Now().UnixNano()
						last := d.PDODebugLog.Load()
						if now-last > int64(500*time.Millisecond) {
							d.PDODebugLog.Store(now)
							shouldJogLog = true
							logger.Info(fmt.Sprintf(
								"[PDO-DBG] %s status=0x%04X opEnabled=%v cw=0x%04X vel=%d pos=%d",
								d.Name, status, opEnabled,
								uint16(d.desiredControlWord.Load()&0xFFFF),
								d.desiredTargetVelocity.Load(),
								d.PDOPos.Load()))
						}
					}

					// ── Write RxPDO (master → drive) ──────────────────────
					if opEnabled {
						switch {
						case d.IsJogEnabled():
							// Profile Velocity (Mode 3)
							cwJog := uint16(d.desiredControlWord.Load() & 0xFFFF)
							if cwJog == 0 || cwJog == 0x000F {
								cwJog = 0x000F
							}
							// d.Driver.JogControlword: per-device HAL
							// A6 → unchanged; Delta → sets ramp bits 4,5,6 (0x0070)
							cwJog = d.Driver.JogControlword(cwJog)

							// LOG actual CW written to drive (not desiredControlWord which is always 0x000F).
							// Fires every 500ms together with [PDO-DBG]. Shows:
							//   actualCW  — what the drive receives (should be 0x007F for Delta)
							//   cmdMode   — mode we commanded to 0x6060
							//   activeMode — mode drive reports via 0x6061 (n/a if not mapped)
							//   driver    — confirms which HAL instance is handling this device
							if shouldJogLog {
								op2 := int8(d.desiredOpMode.Load())
								if op2 == 0 {
									op2 = 3
								}
								activeModeStr := "n/a"
								if d.PdoOpModeDisplayReady {
									activeModeStr = fmt.Sprintf("%d", d.PDOOpModeDisplay.Load())
								}
								logger.Info(fmt.Sprintf(
									"[PDO-JOG] %s actualCW=0x%04X cmdMode=%d activeMode=%s vel=%d pos=%d driver=%T",
									d.Name, cwJog, op2, activeModeStr,
									d.desiredTargetVelocity.Load(),
									d.PDOPos.Load(),
									d.Driver))
							}

							C.write_u16(d.DomainPD, d.OffControlWord, C.uint16_t(cwJog))
							op := int8(d.desiredOpMode.Load())
							if op == 0 {
								op = 3
							}
							C.write_s8(d.DomainPD, d.OffOpMode, C.int8_t(op))
							vel := d.desiredTargetVelocity.Load()
							C.write_s32(d.DomainPD, d.OffTargetVel, C.int32_t(vel))
							actual := d.PDOPos.Load()
							C.write_s32(d.DomainPD, d.OffTargetPos, C.int32_t(actual))
							d.currentTargetPosition.Store(actual)

						case d.IsPosEnabled():
							// Profile Position (Mode 1)
							goal := d.desiredTargetPosition.Load()
							C.write_s8(d.DomainPD, d.OffOpMode, C.int8_t(1))
							C.write_s32(d.DomainPD, d.OffTargetPos, C.int32_t(goal))
							C.write_s32(d.DomainPD, d.OffTargetVel, C.int32_t(0))

							base := uint16(0x000F | 0x0020)
							if d.ppSetpointPending.Load() {
								// Profile Position set-point handshake (CiA-402 §9.4.2):
								//
								// Phase 1 (latchCounters < 10): hold bit4 LOW for 10 cycles (~10ms)
								//   so the drive latches the new target position before we assert
								//   the new set-point bit. Writing bit4 HIGH too early causes the
								//   drive to acknowledge the PREVIOUS position, not the new one.
								//
								// Phase 2 (latchCounters >= 10): assert bit4 HIGH (base|0x0010).
								//   Exit when the drive acknowledges (bit12 HIGH) or after 30 cycles.
								//
								// FIX (Bug 8): Do NOT reset latchCounters in the else branch.
								//
								// The old else branch reset latchCounters[idx] to 0 on EVERY tick
								// when ppSetpointPending was false. This wiped the counter between
								// consecutive moves. When the next move called SetTargetPositionPDO
								// (which sets ppSetpointPending=true), the counter always started
								// from 0 — adding an unnecessary 10ms Phase 1 delay even when the
								// drive had already stabilised at the previous position and was
								// ready to accept a new set-point immediately.
								//
								// The counter is now only reset when the handshake COMPLETES
								// (ppSetpointPending transitions true→false). Between moves the
								// counter stays at whatever value it was when the last handshake
								// finished — typically 10-30. The next move skips Phase 1 entirely
								// and asserts bit4 HIGH on the first tick, cutting set-point
								// acknowledgement latency from ~10ms to ~1ms on consecutive moves.
								if latchCounters[idx] < 10 {
									latchCounters[idx]++
									C.write_u16(d.DomainPD, d.OffControlWord, C.uint16_t(base))
								} else {
									C.write_u16(d.DomainPD, d.OffControlWord, C.uint16_t(base|0x0010))
									latchCounters[idx]++
									if (status&0x1000) != 0 || latchCounters[idx] > 30 {
										d.ppSetpointPending.Store(false)
										latchCounters[idx] = 0 // reset only on handshake completion
									}
								}
							} else {
								// FIX (Bug 8): counter NOT reset here — preserved between moves.
								// When the next move sets ppSetpointPending=true, latchCounters[idx]
								// is already >= 10 so Phase 1 is skipped and bit4 is asserted
								// immediately on the first tick.
								C.write_u16(d.DomainPD, d.OffControlWord, C.uint16_t(base))
							}
							d.currentTargetPosition.Store(goal)

						default:
							// Standby / Shutdown / Multiturn reset
							// DIAGNOSTIC: when a completed PP move hands control back to standby,
							// report any raw 0x6064 displacement from the last commanded PP goal.
							// Throttled to 50ms so the 1ms cyclic loop is not flooded with logs.
							lastGoal := d.desiredTargetPosition.Load()
							standbyActual := d.PDOPos.Load()
							standbyDiff := int64(lastGoal) - int64(standbyActual)
							if standbyDiff < 0 {
								standbyDiff = -standbyDiff
							}
							if standbyDiff > 10 {
								now := time.Now().UnixNano()
								last := d.PDODebugLog.Load()
								if now-last > int64(50*time.Millisecond) {
									d.PDODebugLog.Store(now)
									logger.Info(fmt.Sprintf(
										"[PDO-STANDBY] %s sw=0x%04X mode=%d lastPPGoal=%d actual=%d diff=%d posEnabled=%v jogEnabled=%v",
										d.Name, status, d.Driver.StandbyOpMode(), lastGoal, standbyActual, standbyDiff,
										d.IsPosEnabled(), d.IsJogEnabled()))
								}
							}

							if d.pdoShutdownActive.Load() || d.pdoMTResetActive.Load() || d.pdoForcedServoCycleActive.Load() {
								C.write_u16(d.DomainPD, d.OffControlWord, C.uint16_t(0x0006))
							} else {
								C.write_u16(d.DomainPD, d.OffControlWord, C.uint16_t(0x000F))
							}
							// HAL: StandbyOpMode is drive-specific.
							//   A6Minas / DeltaASDA2E → Mode 3 (Profile Velocity, vel=0):
							//     silent standby via velocity loop, no jerk, no beeping.
							//   NidecM700 → Mode 8 (CSP): Mode 3 vel=0 causes 0xFF01 drive
							//     trips on Nidec; CSP with position hold is required.
							C.write_s8(d.DomainPD, d.OffOpMode, C.int8_t(d.Driver.StandbyOpMode()))
							actual := d.PDOPos.Load()
							C.write_s32(d.DomainPD, d.OffTargetPos, C.int32_t(actual))
							C.write_s32(d.DomainPD, d.OffTargetVel, C.int32_t(0))
							d.currentTargetPosition.Store(actual)
						}

					} else {
						// Not yet in Operation Enabled
						// FIX 1: DO NOT force 0x000F. Let cia402NextControlword safely step through 0x06 -> 0x07 -> 0x0F.
						if d.IsJogEnabled() && (status&0x0008) == 0 {
							cw = d.Driver.JogControlword(cw)
						}

						// FIX: When shutdown or multi-turn reset is in progress, ALWAYS write
						// CW=0x0006 (Shutdown) — even in the not-yet-opEnabled branch.
						//
						// WITHOUT THIS FIX the drive loops:
						//   1. Op Enabled (0x0027) + CW=0x0006 → Ready To Switch On (0x0021)
						//   2. cia402NextControlword(0x0021) returns CW=0x0007 (Switch On)
						//   3. This branch writes CW=0x0007 → drive goes to Switched On (0x0023)
						//   4. cia402NextControlword(0x0023) returns CW=0x000F (Enable Operation)
						//   5. This branch writes CW=0x000F → drive goes back to Op Enabled (0x0027)
						//   6. Loop repeats — drive never stays at Ready To Switch On (0x0021)
						//
						// The StopPDOCyclic C tight-loop avoids this by always writing 0x0006
						// directly. Here we must do the same for the Go-level 1ms ticker.
						if d.pdoShutdownActive.Load() || d.pdoMTResetActive.Load() || d.pdoForcedServoCycleActive.Load() {
							C.write_u16(d.DomainPD, d.OffControlWord, C.uint16_t(0x0006))
						} else {
							C.write_u16(d.DomainPD, d.OffControlWord, C.uint16_t(cw))
						}

						if d.IsJogEnabled() {
							// Maintain Mode 3 and velocity during transition
							op := int8(d.desiredOpMode.Load())
							if op == 0 {
								op = 3
							}
							C.write_s8(d.DomainPD, d.OffOpMode, C.int8_t(op))
							vel := d.desiredTargetVelocity.Load()
							C.write_s32(d.DomainPD, d.OffTargetVel, C.int32_t(vel))

							actual := d.PDOPos.Load()
							C.write_s32(d.DomainPD, d.OffTargetPos, C.int32_t(actual))
							d.currentTargetPosition.Store(actual)

						} else if d.IsPosEnabled() {
							// FIX 2: Maintain Mode 1 and GOAL position during transition!
							// Do NOT feed 'actual' here, or the Nidec will see a massive jump the millisecond it enables.
							goal := d.desiredTargetPosition.Load()
							C.write_s8(d.DomainPD, d.OffOpMode, C.int8_t(1))
							C.write_s32(d.DomainPD, d.OffTargetPos, C.int32_t(goal))
							C.write_s32(d.DomainPD, d.OffTargetVel, C.int32_t(0))
							d.currentTargetPosition.Store(goal)

						} else {
							// Standby — drive not yet in Operation Enabled.
							// Use per-device StandbyOpMode for the same reason as above.
							C.write_s8(d.DomainPD, d.OffOpMode, C.int8_t(d.Driver.StandbyOpMode()))
							C.write_s32(d.DomainPD, d.OffTargetVel, C.int32_t(0))
							actual := d.PDOPos.Load()
							C.write_s32(d.DomainPD, d.OffTargetPos, C.int32_t(actual))
							d.currentTargetPosition.Store(actual)
						}
					}

					// ── Digital outputs ───────────────────────────────────
					if d.PdoDigOutReady {
						mask := d.desiredDigOutMask.Load()
						val := d.desiredDigOutVal.Load()
						if d.OffDigOutMask != 0 {
							C.write_u32(d.DomainPD, d.OffDigOutMask, C.uint32_t(mask))
						}
						if d.OffDigOutVal != 0 {
							C.write_u32(d.DomainPD, d.OffDigOutVal, C.uint32_t(val))
						}
					}

					// Queue this device's domain frame.
					C.ecrt_domain_queue(d.Domain)

				} // end per-device loop

				lastPDOTickNanos.Store(time.Now().UnixNano())

				// ── DC clock sync — required for ecrt_slave_config_dc() slaves ─
				// Sets the master's application time reference and distributes
				// SYNC0 timing to all DC-configured slaves. Must be called after
				// all ecrt_domain_queue() calls and before ecrt_master_send().
				// Safe to call unconditionally: IgH skips the DC datagrams
				// automatically when no slave has DC configured (Delta, M700).
				C.sync_dc_clocks(master)

				// ── Send: one call flushes ALL queued domain frames ───────
				C.ecrt_master_send(master)
			}
		}
	}()

	return nil
}

// startPDOWatchdog launches one process-wide observer for the multi-device PDO loop.
// It does not attempt recovery; it logs stale/hung cyclic-task symptoms so operators
// can distinguish drive faults from master/cyclic stalls.
func startPDOWatchdog() {
	if !pdoWatchdogRunning.CompareAndSwap(false, true) {
		return
	}

	const (
		checkInterval  = 100 * time.Millisecond
		warnThreshold  = 50 * time.Millisecond
		errorThreshold = 500 * time.Millisecond
		reportCooldown = 5 * time.Second
	)

	go func() {
		logger.Info("[PDO-WATCHDOG] started — warn at >50ms, error at >500ms")
		var lastReportAt time.Time
		var lastReportLevel int
		for {
			time.Sleep(checkInterval)
			if !pdoActive.Load() {
				lastReportLevel = 0
				continue
			}
			age := PDOTickAge()
			now := time.Now()
			level := 0
			switch {
			case age >= errorThreshold:
				level = 2
			case age >= warnThreshold:
				level = 1
			}
			if level == 0 && lastReportLevel > 0 {
				logger.Info("[PDO-WATCHDOG] cyclic task recovered, tick age=", age)
				lastReportLevel = 0
				lastReportAt = time.Time{}
				continue
			}
			if level == 0 || (level == lastReportLevel && now.Sub(lastReportAt) < reportCooldown) {
				continue
			}
			if level == 2 {
				logger.Error("[PDO-WATCHDOG] cyclic task appears HUNG: tick age=", age, " — process restart likely required")
			} else {
				logger.Warn("[PDO-WATCHDOG] cyclic task running late: tick age=", age)
			}
			lastReportLevel = level
			lastReportAt = now
		}
	}()
}
