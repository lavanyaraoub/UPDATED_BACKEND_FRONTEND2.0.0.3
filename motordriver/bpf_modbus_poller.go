package motordriver

import (
	"EtherCAT/channels"
	"EtherCAT/helper"
	"EtherCAT/logger"
	"EtherCAT/settings"
	"context"
	"encoding/binary"
	"fmt"
	"io/ioutil"
	"net"
	"strconv"
	"sync"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// BPF Modbus Poller
//
// Terminal → Modbus bit → role:
//   1.1 → X0 → ALWAYS 1 — hardwired, dropped by shifting right 1
//   2.1 → X1 → Bit 1 (LSB / rightmost in binary string)
//   1.4 → X2 → Bit 2
//   2.4 → X3 → Bit 3
//   3.1 → X4 → Bit 4 (MSB / leftmost in binary string)
//
// Raw register always has X0=1 (terminal 1.1 hardwired).
// Shift right by 1 drops X0, leaving X1–X4 as a clean 4-bit signal.
//
// Example — only 2.4 plugged:
//   rawVal = 0b01001 = 9
//   (9 >> 1) & 0x000F = 4 → "0100"
//   Match against settings.json "binary": "0100"
//   Write gm_codes/bpf_program → execute via channels.NotifyRunProgram()
// ─────────────────────────────────────────────────────────────────────────────

const (
	bpfServerIP     = "192.168.1.10" // ILC 131 ETH static IP
	bpfServerPort   = 502            // Modbus/TCP standard port
	bpfRegisterAddr = 2              // udtMODBUS_Server_Data[2] — 0-indexed
	bpfPollMs       = 50             // poll interval milliseconds
	bpfGMFileName   = "bpf_program"  // GM code file owned by BPF poller
	bpfDriveName    = "A"            // drive axis to move
)

var (
	bpfPollerMu     sync.Mutex
	bpfPollerCancel context.CancelFunc
	bpfPollerDone   chan struct{}
)

// StartBPFPoller starts the BPF Modbus polling goroutine.
// Called from initListeners() in init_master.go after motor is initialised.
func StartBPFPoller() {
	bpfPollerMu.Lock()
	if bpfPollerCancel != nil {
		bpfPollerMu.Unlock()
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	bpfPollerCancel = cancel
	bpfPollerDone = done
	bpfPollerMu.Unlock()

	go func() {
		defer close(done)
		runBPFPoller(ctx)
	}()
}

// StopBPFPoller cancels any in-progress Modbus dial and stops retry/poll waits.
// It is safe to call when the poller is not running.
func StopBPFPoller() {
	bpfPollerMu.Lock()
	cancel := bpfPollerCancel
	done := bpfPollerDone
	bpfPollerCancel = nil
	bpfPollerDone = nil
	bpfPollerMu.Unlock()

	if cancel == nil {
		return
	}

	cancel()
	select {
	case <-done:
		logger.Info("[BPF] Modbus poller stopped")
	case <-time.After(time.Second):
		logger.Warn("[BPF] Modbus poller stop timed out; shutdown will continue")
	}
}

func runBPFPoller(ctx context.Context) {
	logger.Info("[BPF] Modbus poller starting — server:", bpfServerIP)
	logger.Info("[BPF] Wiring: 1.1=X0(ignored) 2.1=X1(LSB) 1.4=X2 2.4=X3 3.1=X4(MSB)")

	prevSignal := "0000"

	for {
		if !waitForBPFPoll(ctx, bpfPollMs*time.Millisecond) {
			return
		}

		// ── Read register 2 from ILC 131 ETH ─────────────────────────────
		rawVal, err := readModbusRegisterContext(ctx, bpfServerIP, bpfServerPort, bpfRegisterAddr)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			logger.Warn("[BPF] Modbus read error:", err, "— retrying in 5s")
			if !waitForBPFPoll(ctx, 5*time.Second) {
				return
			}
			continue
		}

		// ── Extract 4-bit signal dropping X0 (terminal 1.1, always 1) ────
		//
		// rawVal layout:
		//   bit4  bit3  bit2  bit1  bit0
		//   X4    X3    X2    X1    X0 ← always 1 (terminal 1.1)
		//   3.1   2.4   1.4   2.1
		//
		// Shift right by 1 drops X0.
		// Mask 0x000F keeps only X1–X4.
		// fmt.Sprintf("%04b") → MSB first, matches settings.json binary field.
		//
		// e.g. only 2.4 plugged: rawVal=9(01001) → (9>>1)&0xF=4 → "0100" ✓
		// e.g. nothing plugged:  rawVal=1(00001) → (1>>1)&0xF=0 → "0000" → ignored ✓
		signal := fmt.Sprintf("%04b", (rawVal>>1)&0x000F)

		// ── Rising edge — only act on change, ignore idle "0000" ──────────
		if signal == prevSignal || signal == "0000" {
			prevSignal = signal
			continue
		}

		logger.Info("[BPF] Rising edge:", prevSignal, "→", signal)
		prevSignal = signal

		// ── Match signal against BinaryPosFeeds from settings.json ────────
		drvSettings := settings.GetDriverSettings(bpfDriveName)
		matched := false

		for _, entry := range drvSettings.BinaryPosFeeds {
			if entry.Binary != signal {
				continue
			}

			logger.Info("[BPF] Matched:", signal,
				"→ pos:", entry.Position,
				"dir:", entry.Direction,
				"feed_rate:", entry.FeedRate)

			// ── Write GM code file "bpf_program" ─────────────────────
			filePath := helper.GetCodeFilePath() + "/" + bpfGMFileName
			if err := writeBPFGMFile(entry, filePath); err != nil {
				logger.Error("[BPF] Failed to write GM file:", err)
				break
			}

			// ── Execute via registered callback in channels ────────────
			// channels.NotifyRunProgram calls executors.RunCodeFile via
			// a function registered at startup — no import cycle.
			channels.NotifyRunProgram(filePath)

			matched = true
			break
		}

		if !matched {
			logger.Warn("[BPF] No mapping found for signal:", signal, "— ignoring")
		}
	}
}

func waitForBPFPoll(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// writeBPFGMFile generates GM code file at filePath.
// File is created on first signal and overwritten on every subsequent signal.
//
// Generated file format:
//
//	G01 F<feed_rate>;    ← feed rate from BPF mapping
//	G90;                 ← absolute positioning mode
//	A<pos>;              ← move to mapped degree (negative if dir=0)
//	M30;                 ← end of program
func writeBPFGMFile(entry settings.BinaryPosFeed, filePath string) error {
	// Apply direction: dir=1 → positive, dir=0 → negative
	pos := entry.Position
	if entry.Direction == 0 {
		if len(pos) > 0 && pos[0] != '-' {
			pos = "-" + pos
		}
	}

	feedRate := strconv.Itoa(int(entry.FeedRate))

	content := fmt.Sprintf(
		"G01 F%s;\nG90;\nA%s;\nM30;\n",
		feedRate,
		pos,
	)

	if err := ioutil.WriteFile(filePath, []byte(content), 0777); err != nil {
		return fmt.Errorf("writeBPFGMFile: %w", err)
	}

	logger.Info("[BPF] GM file written:", filePath)
	logger.Debug("[BPF] GM file content:\n", content)
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Minimal Modbus/TCP client — reads one holding register via FC03.
// No external library — raw TCP implementation.
//
// Request frame (12 bytes):
//   [0-1]   Transaction ID : 0x0001
//   [2-3]   Protocol ID    : 0x0000 (Modbus)
//   [4-5]   Length         : 0x0006
//   [6]     Unit ID        : 0x01
//   [7]     Function code  : 0x03 (Read Holding Registers)
//   [8-9]   Start address  : register index (0-based)
//   [10-11] Quantity       : 0x0001 (1 register)
//
// Response (11 bytes):
//   [0-5]  MBAP header
//   [6]    Unit ID
//   [7]    Function code (0x83 = exception)
//   [8]    Byte count (2) or exception code
//   [9-10] Register value big-endian uint16
// ─────────────────────────────────────────────────────────────────────────────

func readModbusRegister(ip string, port int, registerAddr uint16) (uint16, error) {
	return readModbusRegisterContext(context.Background(), ip, port, registerAddr)
}

func readModbusRegisterContext(ctx context.Context, ip string, port int, registerAddr uint16) (uint16, error) {
	addr := fmt.Sprintf("%s:%d", ip, port)

	dialer := net.Dialer{Timeout: 3 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return 0, fmt.Errorf("dial %s: %w", addr, err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))

	request := []byte{
		0x00, 0x01,
		0x00, 0x00,
		0x00, 0x06,
		0x01,
		0x03,
		byte(registerAddr >> 8), byte(registerAddr & 0xFF),
		0x00, 0x01,
	}

	if _, err := conn.Write(request); err != nil {
		return 0, fmt.Errorf("write request: %w", err)
	}

	response := make([]byte, 11)
	if _, err := conn.Read(response); err != nil {
		return 0, fmt.Errorf("read response: %w", err)
	}

	if response[7] == 0x83 {
		return 0, fmt.Errorf("modbus exception code: %d", response[8])
	}

	value := binary.BigEndian.Uint16(response[9:11])
	return value, nil
}