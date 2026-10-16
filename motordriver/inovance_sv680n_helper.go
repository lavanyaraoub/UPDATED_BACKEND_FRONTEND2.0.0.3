package motordriver

import (
	"fmt"
	"time"
)

// SV680NHelper — helpers for a DDR (1:1) on SV680N.
// Wire the two function fields to your existing HAL / cgo bridge.
type SV680NHelper struct {
	SetCW    func(cw uint16)                                           // RxPDO 6040h
	GetSW    func() uint16                                             // TxPDO 6041h

	ResetDI   int // panel reset button on DI1..DI5, 0 = none
	lastReset bool
}

// Scaling (0x6091) is written by the "configure" operation in inovance_sv680n.yml.

// ---------------- Digital inputs (60FDh) ----------------
// bit0 NOT, bit1 POT, bit2 Home, bit16..20 = DI1..DI5. Everything else ignored.
type SV680NInputs struct {
	NOT, POT, Home bool
	DI             [5]bool
}

func SV680NReadInputs(raw uint32) SV680NInputs {
	in := SV680NInputs{
		NOT:  raw&(1<<0) != 0,
		POT:  raw&(1<<1) != 0,
		Home: raw&(1<<2) != 0,
	}
	for i := 0; i < 5; i++ {
		in.DI[i] = raw&(1<<(16+i)) != 0
	}
	return in
}

// ResetPressed returns true once per button press (rising edge only).
func (d *SV680NHelper) ResetPressed(in SV680NInputs) bool {
	if d.ResetDI < 1 || d.ResetDI > 5 {
		return false
	}
	now := in.DI[d.ResetDI-1]
	pressed := now && !d.lastReset
	d.lastReset = now
	return pressed
}

// ---------------- Fault reset + enable ----------------
// Call AFTER the cyclic task is in OP. Every wait has a timeout, so it
// can never hang the application.
func (d *SV680NHelper) ResetAndEnable() error {
	if d.GetSW()&0x0008 != 0 { // fault bit
		d.SetCW(0x0006)
		time.Sleep(20 * time.Millisecond)
		d.SetCW(0x0086) // rising edge on bit 7 = fault reset
		time.Sleep(20 * time.Millisecond)
		d.SetCW(0x0006)
		if !d.waitSW(0x0008, 0, 3*time.Second) {
			return fmt.Errorf("fault not cleared, sw=0x%04X", d.GetSW())
		}
	}
	d.SetCW(0x0006) // Shutdown
	if !d.waitSW(0x006F, 0x0021, 500*time.Millisecond) {
		return fmt.Errorf("not Ready To Switch On, sw=0x%04X", d.GetSW())
	}
	d.SetCW(0x0007) // Switch On
	time.Sleep(10 * time.Millisecond)
	d.SetCW(0x000F) // Enable Operation
	if !d.waitSW(0x006F, 0x0027, time.Second) {
		return fmt.Errorf("not Operation Enabled, sw=0x%04X", d.GetSW())
	}
	return nil
}

// Disable: walk to Ready To Switch On (use on shutdown, before releasing master).
func (d *SV680NHelper) Disable() {
	d.SetCW(0x0006)
	d.waitSW(0x006F, 0x0021, 500*time.Millisecond)
}

func (d *SV680NHelper) waitSW(mask, want uint16, timeout time.Duration) bool {
	end := time.Now().Add(timeout)
	for time.Now().Before(end) {
		if d.GetSW()&mask == want {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return false
}

// ---------------- Error text (603Fh) ----------------
// Inovance panel code = "E" + 3 hex digits, e.g. 0x0E08 -> EE08, 0x0939 -> E939.
func SV680NErrorText(code uint16) string {
	switch code {
	case 0:
		return "No Alarms"
	case 0x0E08:
		return "EE08 EtherCAT sync loss"
	case 0x0939:
		return "E939 Motor power cable disconnected"
	}
	return fmt.Sprintf("E%03X", code&0x0FFF)
}
