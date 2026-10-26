package motordriver

/*
#cgo CFLAGS: -g -Wall -I/opt/etherlab/include -I${SRCDIR}/..
#cgo LDFLAGS: -L${SRCDIR}/.. -L/opt/etherlab/lib/ -lethercatinterface -lethercat
#include "ethercatinterface.h"
*/
import "C"

import (
	logger "EtherCAT/logger"
	"fmt"
	"time"
)

// profileVelocityScaler is an optional HAL capability for drives whose 0x6081
// Profile Velocity units differ from their 0x60FF jog velocity units.
type profileVelocityScaler interface {
	ScaleProfileVelocity(scaledRPM int, dev *MasterDevice) uint32
}

// setRpm updates the velocity for both Jog (Mode 3) and Program Execution (Mode 1).
func setRpm(masterDevice *MasterDevice, rpm int) error {
	scaledRPM := masterDevice.Device.RPMConst * rpm

	if !IsPDOActive() || !masterDevice.PdoJogReady {
		return fmt.Errorf("setRpm: PDO not active or RxPDO not ready â€” " +
			"cannot set velocity setpoint (PDO mandatory in this build)")
	}

	// 1. Update the PDO cache (0x60FF) used exclusively for Manual Jog moves
	masterDevice.desiredTargetVelocity.Store(int32(scaledRPM))
	logger.Info("[PDO] setRpm: velocity setpoint cached for Jog. scaledRPM:", scaledRPM, "driver:", masterDevice.Name)

	// 2. Update Profile Velocity (0x6081) via async SDO for Program Execution (Profile Position mode).
	//
	// WHY WE MUST WAIT FOR ACK BEFORE doneDriverAction():
	//
	//   The executor runs G01 F20 â†’ G90 â†’ G68 â†’ A-90 in ~6ms total.
	//   The EtherCAT CoE mailbox takes 10-20ms to deliver the SDO write to the
	//   drive. If doneDriverAction() is called before the ACK, the first PP move
	//   starts while 0x6081 still holds the startup default (1000000 counts/s).
	//   All subsequent moves are correct because the ACK arrives before the second
	//   command executes â€” but the first rotation runs at the wrong (slow) speed.
	//
	//   Fix: arm the SDO request then poll until SUCCESS/ERROR before unblocking
	//   the executor. The mailbox round-trip is typically 5-15ms â€” imperceptible
	//   to the operator but critical for velocity correctness on the first move.
	if masterDevice.PdoVelSdoReady && masterDevice.PdoVelSdoReq != nil {
		profileVel := uint32(scaledRPM)
		if scaler, ok := masterDevice.Driver.(profileVelocityScaler); ok {
			profileVel = scaler.ScaleProfileVelocity(scaledRPM, masterDevice)
		}
		armed := false
		for i := 0; i < 10; i++ {
			rc := int(C.trigger_profile_vel_request(masterDevice.PdoVelSdoReq, C.uint32_t(profileVel)))
			if rc == 0 {
				armed = true
				break
			}
			if rc == -16 { // EBUSY â€” previous mailbox still in flight
				time.Sleep(5 * time.Millisecond)
				continue
			}
			logger.Error("[PDO] setRpm: Failed to arm Profile Velocity SDO request, rc=", rc)
			break
		}
		if armed {
			// CRITICAL: wait at least one PDO cyclic tick (1ms) before polling.
			//
			// ROOT CAUSE OF "first cycle runs at wrong speed after zero ref":
			//
			//   After zero ref, the async SDO request for 0x6081 is in
			//   EC_REQUEST_SUCCESS state (drive ACKed the zero ref speed).
			//   When setRpm is called for G01 Fxx, trigger_profile_vel_request
			//   writes the new value and calls ecrt_sdo_request_write â€” which
			//   arms the request. However, IgH only transitions the state from
			//   SUCCESS â†’ QUEUED on the NEXT ecrt_master_receive() call inside
			//   the 1ms cyclic task.
			//
			//   Without this sleep, the polling loop immediately reads
			//   EC_REQUEST_SUCCESS (the STALE state from zero ref), assumes the
			//   new value was ACKed, and calls doneDriverAction() â€” but 0x6081
			//   in the drive still holds the old speed. The first PP move then
			//   runs at the wrong speed. By the second cycle IgH has processed
			//   the request and 0x6081 is correct, which is exactly what was
			//   observed: wrong speed on cycle 1, correct from cycle 2 onward.
			//
			//   Fix: sleep 2ms (two cyclic ticks) so IgH transitions the state
			//   before we start polling. Total cost: 2ms extra per setRpm call
			//   inside the action listener â€” imperceptible to the operator.
			time.Sleep(2 * time.Millisecond)

			// Poll until the drive ACKs (IgH services the mailbox inside
			// ecrt_master_receive() on every 1ms cyclic tick).
			deadline := time.Now().Add(100 * time.Millisecond)
			for time.Now().Before(deadline) {
				state := int(C.get_profile_vel_state(masterDevice.PdoVelSdoReq))
				if state == 1 { // EC_REQUEST_SUCCESS
					logger.Info("[PDO] setRpm: Profile Velocity (0x6081) ACKed by drive. scaledRPM:", scaledRPM, "profileVel:", profileVel)
					break
				}
				if state == -1 { // EC_REQUEST_ERROR
					logger.Error("[PDO] setRpm: drive returned error for Profile Velocity SDO")
					break
				}
				time.Sleep(1 * time.Millisecond)
			}
		}
	} else {
		logger.Warn("[PDO] setRpm: PdoVelSdoReady is false, Profile Position feed rate will not update dynamically.")
	}

	// Signal executor to advance to the next program line.
	doneDriverAction()
	return nil
}