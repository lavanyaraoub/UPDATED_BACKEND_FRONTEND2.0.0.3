package motordriver

/*
Inovance SV680N-GINT (SV680NT012I) IMotorDriver implementation.

  VendorID: 0x00100000  ProductCode: 0x000C012A  Revision: 0x00010000
  ESI:      INOVANCE_SV680N-GINT_1Axis_V02.xml
  Config:   /configs/inovance_sv680n.yml

Design:
  The SV680N is a standard CiA-402 drive with digital inputs in 0x60FD
  (bit0 NOT, bit1 POT, bit2 HOME) — the same model as the Delta ASDA-A2-E.
  So this driver EMBEDS DeltaASDA2E and only overrides what differs.
  Everything not overridden here (SetupPDO via the generic YAML engine,
  IsTargetReached on bit10, potNotEnabled, the 0x60FD IO listener,
  finish signal via 0x60FE, fault reset 0x008F, ...) is inherited unchanged.

Overrides:
  - StandbyOpMode: Mode 1 (Profile Position hold) instead of the Delta's
    Mode 3. In Mode 3 vel=0 only the velocity loop is closed, so the DDR
    crept while idle (-288 counts in 40 s, -1,600 counts while stopped,
    see logs 16-Oct / 22-Oct). In Mode 1 with CW bit4 LOW the drive keeps
    holding the last latched 607A target with its position loop.
  - HoldInPPAfterMotion: true. Enables the PP-hold handling in StopJog
    (wait for standstill, re-latch 607A = actual in Mode 1) and in
    emergency() (Mode 3 velocity hold until the next motion command, so an
    aborted PP move decelerates instead of completing).
  - JogControlword: clear Halt (bit8) only. Bits 4-6 are mode-specific /
    reserved in PV mode under CiA-402; the Delta-specific 0x0070 workaround
    is not applied.
  - SupportsMultiTurnReset / ResetMultiTurn: disabled for v1 until the
    0x200D:0x15 (H0D-20) procedure is confirmed against the SV680N manual.

Known limitations (v1, single-axis bench bring-up):
  - The inherited IO listener shares the Delta stop-channel globals and logs
    with a "Delta" prefix. Fine for a single SV680N axis; give the SV680N its
    own listener before mixing it with a Delta on the same bus.
  - 0x60FD polarity must be verified on the bench (toggle POT/NOT and watch
    `ethercat upload -p 0 -t uint32 0x60fd 0`) before relying on
    StopWhenHWPOTNOT.
*/

import (
	"fmt"
)

// InovanceSV680N is the IMotorDriver implementation for the Inovance SV680N.
type InovanceSV680N struct {
	DeltaASDA2E
}

// StandbyOpMode returns Mode 1 (Profile Position) for SV680N standby.
//
// The cyclic standby branch keeps CW=0x000F with bit4 LOW, so no new set-point
// is triggered while idle; the drive holds the last latched 607A target with
// its position loop closed. This is the same standby mode the Panasonic A6
// uses (a6_minas_motor_driver.go).
func (d InovanceSV680N) StandbyOpMode() int8 {
	return 1
}

// HoldInPPAfterMotion marks this driver as a Profile-Position-hold driver.
// See usesPPHold() in drive_rotation.go.
func (d InovanceSV680N) HoldInPPAfterMotion() bool {
	return true
}

// JogControlword for Profile Velocity mode: clear the Halt bit (bit 8) so
// the drive follows 0x60FF. No ramp-bit workaround needed on the SV680N.
func (d InovanceSV680N) JogControlword(cwBase uint16) uint16 {
	return cwBase &^ uint16(0x0100)
}

// SupportsMultiTurnReset — disabled for v1 (see file header).
func (d InovanceSV680N) SupportsMultiTurnReset() bool {
	return false
}

// ResetMultiTurn — not implemented for v1. Returns an error instead of
// falling through to the Delta P2-08/P2-71 procedure, which would write
// Delta parameter addresses to an Inovance drive.
func (d InovanceSV680N) ResetMultiTurn(availableDevices []*MasterDevice) error {
	return fmt.Errorf("[MT-SV680N] multi-turn reset not implemented for Inovance SV680N yet")
}
