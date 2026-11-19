package motordriver

import (
	"encoding/binary"
	"fmt"
	"io"
	"time"
)

// Both idle and code must remain continuously stable for 100 ms.
// No idle at startup/reconnect means no new request: a held code is not replayed.
const bpfSignalStableTime = 100 * time.Millisecond

type bpfSignalFilter struct {
	armed          bool
	idle           bool
	idleSince      time.Time
	candidate      string
	candidateSince time.Time
}

func (f *bpfSignalFilter) reset() { *f = bpfSignalFilter{} }
func (f *bpfSignalFilter) accept(signal string, now time.Time) bool {
	if signal == "0000" {
		f.candidate = ""
		if !f.idle {
			f.idle = true
			f.idleSince = now
		}
		if now.Sub(f.idleSince) >= bpfSignalStableTime {
			f.armed = true
		}
		return false
	}
	f.idle = false
	if !f.armed {
		return false
	}
	if signal != f.candidate {
		f.candidate = signal
		f.candidateSince = now
		return false
	}
	if now.Sub(f.candidateSince) < bpfSignalStableTime {
		return false
	}
	f.armed = false
	f.candidate = ""
	return true
}

// TCP is a byte stream: short reads must not turn missing bytes into idle bits.
func readBPFModbusResponse(r io.Reader) (uint16, error) {
	var header [7]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return 0, fmt.Errorf("read MBAP: %w", err)
	}
	if binary.BigEndian.Uint16(header[0:2]) != 1 || binary.BigEndian.Uint16(header[2:4]) != 0 || header[6] != 1 {
		return 0, fmt.Errorf("invalid Modbus transaction/protocol/unit")
	}
	length := int(binary.BigEndian.Uint16(header[4:6]))
	if length != 5 && length != 3 {
		return 0, fmt.Errorf("invalid Modbus response length %d", length)
	}
	payload := make([]byte, length-1)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, fmt.Errorf("read Modbus payload: %w", err)
	}
	if len(payload) == 2 && payload[0] == 0x83 {
		return 0, fmt.Errorf("modbus exception code: %d", payload[1])
	}
	if len(payload) != 4 || payload[0] != 3 || payload[1] != 2 {
		return 0, fmt.Errorf("invalid holding-register response")
	}
	return binary.BigEndian.Uint16(payload[2:]), nil
}
