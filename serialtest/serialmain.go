package serialtest

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/tarm/serial"

	"EtherCAT/channels"
	executors "EtherCAT/executors"
	"EtherCAT/helper"
	"EtherCAT/logger"
	motor "EtherCAT/motordriver"
	"EtherCAT/settings"
)

// ------------------------------------------------------------
// SERIAL PORT CONFIGURATION
//
// These were previously implicit: Size/Parity/StopBits were never set on
// serial.Config, so tarm/serial silently fell back to its own defaults
// (8 data bits, no parity, 1 stop bit). That happened to be correct for
// the Fanuc ISO-code (OUTPUT CODE=1) profile already field-validated at
// Rane Madras, but it was correct by accident, not by declaration.
//
// Fanuc has no independent "parity" parameter for RS232 program I/O —
// parity is a direct consequence of SETTING(HANDY) OUTPUT CODE:
//   OUTPUT CODE = 1 (ISO) -> 8 data bits, NO parity   (current field config)
//   OUTPUT CODE = 0 (EIA) -> 7 data bits, EVEN parity
// If a machine is ever switched to EIA/OUTPUT CODE=0, RTCParity below
// must be flipped to "even" and RTCDataBits to 7 to match — the two
// sides cannot disagree.
// ------------------------------------------------------------

const (
	defaultSerialDevice = "/dev/ttyUSB0"
	defaultBaudRate     = 9600
	defaultDataBits     = 8
	defaultStopBits     = 1
	defaultParity       = "none" // "none" | "even" | "odd"
)

// RS232Profile holds one machine's confirmed serial settings. Read from
// settings so a second machine (e.g. a Mazak on a different I/O CHANNEL
// register block) can carry its own profile without forking this file or
// recompiling.
type RS232Profile struct {
	Device   string
	Baud     int
	DataBits int    // 5,6,7,8
	Parity   string // "none" | "even" | "odd" | "mark" | "space"
	StopBits int    // 1 or 2
}

// loadRS232Profile reads the active profile from settings, falling back to
// the confirmed-working Fanuc/ISO defaults (9600-8-N-1) for any field the
// settings store doesn't provide. Wire this to settings.GetDriverSettings
// or an equivalent machine-scoped config once a second machine profile
// exists; for now it returns the confirmed default profile.
func loadRS232Profile() RS232Profile {
	p := RS232Profile{
		Device:   defaultSerialDevice,
		Baud:     defaultBaudRate,
		DataBits: defaultDataBits,
		Parity:   defaultParity,
		StopBits: defaultStopBits,
	}

	// Example of how a per-machine override would plug in once available:
	//
	//   if v := settings.GetSerialSetting("device"); v != "" { p.Device = v }
	//   if v := settings.GetSerialSetting("baud"); v != "" {
	//       if n, err := strconv.Atoi(v); err == nil { p.Baud = n }
	//   }
	//   if v := settings.GetSerialSetting("parity"); v != "" { p.Parity = v }
	//
	// Left as a documented no-op today because settings does not yet expose
	// a serial-profile section — flagged rather than silently guessed.

	return p
}

// parityFromString maps a human-readable parity name to the tarm/serial
// constant. Defaults to ParityNone on anything unrecognized, and logs
// loudly so a typo in config never silently changes wire behavior.
func parityFromString(name string) serial.Parity {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "none":
		return serial.ParityNone
	case "even":
		return serial.ParityEven
	case "odd":
		return serial.ParityOdd
	case "mark":
		return serial.ParityMark
	case "space":
		return serial.ParitySpace
	default:
		logger.Warn("[RS232] Unknown parity setting, defaulting to none:", name)
		return serial.ParityNone
	}
}

func stopBitsFromInt(n int) serial.StopBits {
	switch n {
	case 2:
		return serial.Stop2
	default:
		if n != 1 {
			logger.Warn("[RS232] Unsupported stop bits value, defaulting to 1:", n)
		}
		return serial.Stop1
	}
}

// buildSerialConfig turns a profile into the tarm/serial config, with
// every field set explicitly — nothing left to the library's defaults.
func buildSerialConfig(p RS232Profile) *serial.Config {
	return &serial.Config{
		Name:        p.Device,
		Baud:        p.Baud,
		Size:        byte(p.DataBits),
		Parity:      parityFromString(p.Parity),
		StopBits:    stopBitsFromInt(p.StopBits),
		ReadTimeout: time.Millisecond * 200,
	}
}

// getFilesPath returns the correct FILES path regardless of run location.
// Using helper.GetCodeFilePath() avoids the hardcoded /mnt/app/jamun path
// that broke execution when running from the dev directory.
func getFilesPath() string {
	return helper.GetCodeFilePath() + "/FILES"
}

var executionInProgress atomic.Bool
var fileUpdatedDuringExecution atomic.Bool

// pendingCommand holds the last RS232 command received while execution was
// in progress. Only the most recent command matters — older ones are
// superseded. This prevents a queue of stale commands building up.
var pendingCommand atomic.Value // stores string

// portHealthy tracks whether the current port session is considered good.
// Exposed via IsRS232PortHealthy() so other subsystems (UI status, alarms)
// can reflect real link state instead of assuming RS232 is always up.
var portHealthy atomic.Bool

func IsRS232PortHealthy() bool {
	return portHealthy.Load()
}

// ------------------------------------------------------------
// RELIABILITY
//
// The previous version opened the port once and returned permanently on
// failure — a cable pull, a USB-serial adapter re-enumerating, or the CNC
// power-cycling would kill RS232 for the rest of the jamun process
// lifetime with no recovery. StartSerialListener now retries with backoff
// and re-opens automatically if the port drops mid-session.
// ------------------------------------------------------------

const (
	reopenBackoffMin = 500 * time.Millisecond
	reopenBackoffMax = 5 * time.Second
)

func StartSerialListener() {
	profile := loadRS232Profile()
	logger.Info(fmt.Sprintf(
		"[RS232] Configured profile: device=%s baud=%d data=%d parity=%s stop=%d",
		profile.Device, profile.Baud, profile.DataBits, profile.Parity, profile.StopBits,
	))

	backoff := reopenBackoffMin
	for {
		opened, err := runSerialSession(profile)
		portHealthy.Store(false)

		if err == nil {
			// Clean shutdown path (not currently reachable, but keeps the
			// loop well-defined if a future caller adds a stop signal).
			return
		}

		// If the device opened successfully, this was a live-session fault,
		// not a repeated open failure. Reconnect quickly instead of carrying
		// forward an old 15-second exponential backoff window.
		if opened {
			backoff = reopenBackoffMin
		}

		logger.Error("[RS232] Session ended, will retry:", err)
		time.Sleep(backoff)

		// Exponential backoff is only for the case where /dev/ttyUSB0 cannot
		// be opened at all (adapter unplugged / not enumerated). A session that
		// had opened is always retried from the minimum delay above.
		if !opened && backoff < reopenBackoffMax {
			backoff *= 2
			if backoff > reopenBackoffMax {
				backoff = reopenBackoffMax
			}
		}
	}
}

// runSerialSession owns one open-port lifetime. The bool return tells the
// caller whether the device was successfully opened before the session ended.
// This lets StartSerialListener distinguish a real open failure from a
// live-session read failure and choose a sensible reconnect delay.
func runSerialSession(profile RS232Profile) (bool, error) {
	cfg := buildSerialConfig(profile)
	port, err := serial.OpenPort(cfg)
	if err != nil {
		return false, fmt.Errorf("failed to open %s: %w", profile.Device, err)
	}
	defer port.Close()

	logger.Info("[RS232] Port opened:", profile.Device)
	logger.Info("[RS232] Listener stable — idle ReadTimeout/EOF is treated as normal CNC silence")
	portHealthy.Store(true)

	buf := make([]byte, 256)
	frame := make([]byte, 0, 128)
	hardReadErrors := 0
	idleEOFs := 0
	const maxHardReadErrors = 3

	for {
		n, readErr := port.Read(buf)

		// Reader implementations are allowed to return n > 0 together with
		// an error. Never throw away received CNC bytes just because readErr
		// is also set; consume the bytes first and classify the error after.
		if n > 0 {
			hardReadErrors = 0
			idleEOFs = 0
		}

		for i := 0; i < n; i++ {
			b := buf[i]
			if b == 0x12 {
				continue
			}
			clean := b & 0x7F
			if clean == '\n' || clean == '\r' || clean == 0x14 {
				// FANUC can surround DPRNT data with control bytes/NUL padding,
				// and older formatting can produce values such as "B 10".
				// Clean transport noise and remove insignificant spaces BEFORE parsing.
				cmd := sanitizeRS232Frame(string(frame))
				frame = frame[:0]
				if cmd != "" {
					handleRS232Command(cmd)
				}
				continue
			}
			frame = append(frame, clean)
			if len(frame) > 4096 {
				// Runaway frame with no terminator seen — a real line fault
				// (wrong baud/parity causing terminators to be misread as
				// data) looks exactly like this. Drop it and log loudly
				// rather than growing forever.
				logger.Warn("[RS232] Frame exceeded 4096 bytes without a terminator, discarding — check baud/parity/stop-bit match with the CNC")
				frame = frame[:0]
			}
		}

		if readErr == nil {
			hardReadErrors = 0
			continue
		}

		// tarm/serial on Linux reports the configured ReadTimeout with no
		// received bytes as io.EOF. That is NORMAL when the CNC is silent.
		// V3.1 counted these EOFs as faults and deliberately tore the port
		// down after 25 x 200ms ~= 5 seconds, creating long blind windows.
		if errors.Is(readErr, io.EOF) {
			hardReadErrors = 0
			idleEOFs++

			// An unplugged USB adapter normally produces a hard read error, but
			// also verify the device node periodically so an EOF-only driver
			// cannot leave us believing a vanished adapter is still healthy.
			if idleEOFs >= 5 {
				idleEOFs = 0
				if _, statErr := os.Stat(profile.Device); statErr != nil {
					return true, fmt.Errorf("serial device disappeared: %w", statErr)
				}
			}
			continue
		}

		// This is a genuine read failure (EIO, closed fd, USB disconnect,
		// etc.), not an idle timeout. Allow two transient failures, then
		// reopen the port quickly on the third consecutive hard failure.
		idleEOFs = 0
		hardReadErrors++
		logger.Warn(fmt.Sprintf(
			"[RS232] Hard read error %d/%d: %v",
			hardReadErrors, maxHardReadErrors, readErr,
		))
		if hardReadErrors >= maxHardReadErrors {
			return true, fmt.Errorf("serial read failed after %d consecutive hard errors: %w", hardReadErrors, readErr)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// sanitizeRS232Frame removes FANUC/serial transport noise without changing
// the actual command meaning. Spaces and tabs are insignificant in the RTC
// RS232 command grammar, so "B 10", "B    10" and "B10" all become
// the same command before parsing.
func sanitizeRS232Frame(raw string) string {
	// Keep printable ASCII only. This removes NUL padding, ESC and other
	// control characters seen on the FANUC RS232 stream.
	var printable strings.Builder
	printable.Grow(len(raw))
	for _, r := range raw {
		if r >= 0x20 && r <= 0x7E {
			printable.WriteRune(r)
		}
	}

	s := strings.TrimSpace(printable.String())
	if s == "" {
		return ""
	}

	// Known FANUC punch/DPRNT header observed on this interface.
	// Strip only the header itself; do not discard command data after it.
	if idx := strings.Index(strings.ToUpper(s), "&HE:"); idx >= 0 {
		s = s[idx+4:]
	}

	// Whitespace is not meaningful for the supported RS232 G/M/axis syntax.
	// Removing it here also prevents the parser from taking a different path
	// merely because FANUC formatted a variable as "B 10".
	s = strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t':
			return -1
		default:
			return r
		}
	}, s)

	return strings.TrimSpace(s)
}

func rejectInvalidRS232(cmd, reason string) {
	logger.Warn("[RS232] Invalid command rejected:", cmd, "reason:", reason)
	channels.SendAlarm("Invalid RS232 command")
}

func handleRS232Command(cmd string) {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return
	}

	// The serial port remains open so the operator can enable RS232 at runtime,
	// but incoming data must never update FILES or start motion while RS232 is OFF.
	if !executors.RS232Enabled.Load() {
		logger.Info("[RS232] Command ignored because RS232 is disabled:", cmd)
		return
	}

	// FINAL RS232 / ECS SAFETY BARRIER:
	// If an invalid state is ever reached, reject the command before parsing,
	// FILES rebuild, executor launch, motion, or FIN generation.
	if settings.GetDriverSettings("A").ECS != 1 {
		logger.Error("[RS232-SAFETY] Command blocked because ECS is disabled. cmd:", cmd)
		channels.SendAlarm("RS232 command blocked: Enable ECS")
		return
	}

	// A missing/stale backlash_state.json is no longer a reason to kill PDO.
	// It becomes a ZERO-REQUIRED runtime state instead. Reject RS232 here, before
	// parsing or touching FILES, until a successful Zero recreates the trusted
	// position reference automatically.
	if !motor.IsPositionReferenceValid("A") {
		logger.Error("[RS232-SAFETY] Command blocked BEFORE FILES/execution — Zero Reference required. cmd:", cmd)
		channels.SendAlarm("Position reference invalid - perform Zero Reference")
		return
	}

	parts := parseBatchCommands(cmd)
	logger.Info("[RS232] Batch received:", cmd)
	logger.Info("[RS232] Parsed commands:", strings.Join(parts, " | "))
	if len(parts) == 0 {
		rejectInvalidRS232(cmd, "no valid command could be parsed")
		return
	}

	// Validate every token BEFORE writing anything to FILES.
	// If any token is invalid, reject the entire batch and log it.
	normalized := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		norm := normalizeControllerToken(p)
		if norm == "" {
			rejectInvalidRS232(cmd, "invalid token: "+p)
			return
		}
		normalized = append(normalized, norm)
	}

	if len(normalized) == 0 {
		rejectInvalidRS232(cmd, "no valid commands after normalization")
		return
	}

	// B-AXIS TRIPLE-REDUNDANCY VOTE:
	// The RTC has no CRC on this link, so the operator sends the B (angle)
	// value three times in one batch, e.g. "B10,B10,B10". All three copies
	// must be byte-identical after normalization before the value is
	// trusted. Any other count of B tokens (1, 2, 4+) is treated as
	// corruption too — a dropped or duplicated copy is exactly the failure
	// this scheme exists to catch, so it fails closed rather than guessing.
	votedNormalized, ok := voteAndCollapseBAxis(normalized)
	if !ok {
		// Be explicit: this is NOT a FILES execution failure. The raw command
		// is rejected before rebuildFILES() and before any executor is launched.
		logger.Warn("[RS232-VOTE] REJECTED BEFORE FILES/EXECUTION raw=", cmd)
		channels.SendAlarm("Comm Fault: RS232 triple validation failed")
		return
	}
	normalized = votedNormalized

	// All tokens valid — rebuild FILES atomically from the full batch.
	// rebuildFILES enforces strict line ordering and G68 rules.
	if err := rebuildFILES(normalized); err != nil {
		logger.Error("[RS232] rebuildFILES failed, falling back to token update:", err)
		for _, norm := range normalized {
			if err2 := updateFILESFromRS232(norm); err2 != nil {
				logger.Error("[RS232] FILES update failed for token:", norm, "error:", err2)
			} else {
				logger.Info("[RS232] FILES updated with:", norm)
			}
		}
	} else {
		logger.Info("[RS232] FILES rebuilt successfully from batch:", cmd)
	}

	if executionInProgress.Load() {
		// Interrupt the currently running executor immediately so it exits
		// RunCodeFile and returns. The deferred command will then execute
		// cleanly in the re-launch of executeFilesProgramOnce.
		// Without this, the old execution stays blocked in doECSCheck or
		// waitForProgramFileUpdate indefinitely.
		channels.WriteCommandExecInput("stop_prog_exec", "")
		fileUpdatedDuringExecution.Store(true)
		pendingCommand.Store(cmd)
		logger.Warn("[RS232] Execution interrupted + queued:", cmd)
		return
	}

	executionInProgress.Store(true)
	go executeFilesProgramOnce()
}

// voteAndCollapseBAxis enforces 3-of-3 agreement on any B (rotary angle)
// tokens present in a normalized command batch, then collapses the three
// duplicates down to the single B token the rest of the pipeline expects.
//
//   - Zero B tokens: nothing to vote on, batch passes through unchanged
//     (e.g. a bare "G90" or "G68" line with no angle command in it).
//   - Exactly 3 B tokens: all three must be identical after normalization
//     (e.g. all resolve to "B10;"). If they agree, they're collapsed to one.
//   - Any other count (1, 2, 4+): rejected. A single un-repeated B is not
//     accepted once this scheme is in use — a missing or extra copy is
//     itself evidence of a dropped/duplicated frame on the line.
//
// Strict 3-of-3 is used rather than 2-of-3 majority on purpose: this value
// drives physical rotation, so a single disagreeing copy is enough to
// distrust the whole batch and demand a resend rather than act on it.
func voteAndCollapseBAxis(normalized []string) ([]string, bool) {
	var bVotes []string
	var others []string

	for _, tok := range normalized {
		if strings.HasPrefix(strings.ToUpper(tok), "B") {
			bVotes = append(bVotes, tok)
		} else {
			others = append(others, tok)
		}
	}

	switch len(bVotes) {
	case 0:
		return normalized, true
	case 3:
		if bVotes[0] == bVotes[1] && bVotes[1] == bVotes[2] {
			logger.Info("[RS232-VOTE] B-axis triple match confirmed:", bVotes[0])
			return append(others, bVotes[0]), true
		}
		logger.Error("[RS232-VOTE] B-axis mismatch across three copies:", strings.Join(bVotes, " | "))
		return nil, false
	default:
		logger.Error(fmt.Sprintf("[RS232-VOTE] Expected exactly 3 B copies, got %d: %s", len(bVotes), strings.Join(bVotes, " | ")))
		return nil, false
	}
}

// ------------------------------------------------------------
// FILES REBUILD
// Constructs FILES from scratch with strict line ordering:
//
//   G01 F<n>;     — feedrate (always first)
//   G90; / G91;   — absolute / incremental mode
//   [G68;]        — shortest path (G90 only, omitted in G91)
//   A<deg>;       — target axis position
//   M30;          — end program (runs once, does not loop back)
//
// G68 rules:
//   G91 sent       → G68 removed (not valid in incremental mode)
//   G90 + G68 sent → G68 added after G90 line
//   G90 no G68     → G68 omitted
// ------------------------------------------------------------

func rebuildFILES(normalized []string) error {
	var feedrate, modeCmd, axisCmd string
	var hasG68 bool

	for _, tok := range normalized {
		u := upperTrim(tok)
		switch {
		case isFeedrate(u):
			feedrate = tok
		case u == "G90":
			modeCmd = "G90;"
		case u == "G91":
			modeCmd = "G91;"
		case u == "G68":
			hasG68 = true
		case u == "G69":
			hasG68 = false
			if modeCmd == "" {
				modeCmd = "G90;"
			}
		case isAxisKey(u):
			axisCmd = tok
		}
	}

	// Fall back to existing FILES content for any field not supplied.
	existingFeedrate, existingAxis, existingMode := readExistingFILES()
	if feedrate == "" {
		feedrate = firstNonEmpty(existingFeedrate, "G01 F20;")
	}
	if axisCmd == "" {
		axisCmd = firstNonEmpty(existingAxis, "A0;")
	}
	if modeCmd == "" {
		modeCmd = firstNonEmpty(existingMode, "G90;")
	}

	lines := []string{feedrate, modeCmd}

	if strings.HasPrefix(modeCmd, "G90") {
		if hasG68 {
			lines = append(lines, "G68;")
			logger.Info("[RS232] G68 added — absolute mode with shortest path")
		} else {
			logger.Info("[RS232] G68 omitted — absolute mode without shortest path")
		}
	} else if hasG68 {
		logger.Info("[RS232] G68 removed — not valid in incremental (G91) mode")
	}

	lines = append(lines, axisCmd, "M30;")

	logger.Info("[RS232] Rebuilding FILES:")
	for i, l := range lines {
		logger.Info(fmt.Sprintf("[RS232]   line %d: %s", i, l))
	}

	return writeFILESAtomic(lines)
}

func readExistingFILES() (feedrate, axis, mode string) {
	content, err := os.ReadFile(getFilesPath())
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(content), "\n") {
		lt := strings.TrimSpace(line)
		if lt == "" {
			continue
		}
		u := upperTrim(lt)
		switch {
		case isFeedrate(u):
			feedrate = lt
		case u == "G90" || u == "G91":
			mode = lt
		case isAxisKey(u):
			axis = lt
		}
	}
	return
}

func upperTrim(s string) string {
	return strings.ToUpper(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), ";")))
}

func isFeedrate(upper string) bool {
	return strings.HasPrefix(upper, "G01") || strings.HasPrefix(upper, "G1 ") ||
		(strings.HasPrefix(upper, "G0 ") && strings.Contains(upper, "F"))
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func writeFILESAtomic(lines []string) error {
	data := strings.Join(lines, "\n")
	if !strings.HasSuffix(data, "\n") {
		data += "\n"
	}
	tmp := getFilesPath() + ".tmp"
	if err := os.WriteFile(tmp, []byte(data), 0644); err != nil {
		return fmt.Errorf("failed to write temp FILES: %w", err)
	}
	if err := os.Rename(tmp, getFilesPath()); err != nil {
		return fmt.Errorf("failed to rename FILES: %w", err)
	}
	return nil
}

// ------------------------------------------------------------
// EXECUTION LOGIC
// ------------------------------------------------------------

func executeFilesProgramOnce() {
	defer executionInProgress.Store(false)

	logger.Info("[RS232] Preparing program execution")

	channels.WriteCommandExecInput("stop_prog_exec", "")
	executors.ResetExecutingProgram()
	time.Sleep(200 * time.Millisecond)

	motor.RefreshCurrentPosition()

	file := getFilesPath()

	if _, err := os.Stat(file); err != nil {
		logger.Error("[RS232] Program file not found or not accessible:", file, "error:", err)
		fileUpdatedDuringExecution.Store(false)
		return
	}

	if content, err := os.ReadFile(file); err == nil {
		logger.Info("[RS232] Executing file content:\n", string(content))
	}

	logger.Info("[RS232] EXECUTING Program:", file)

	if err := executors.RunCodeFile(file); err != nil {
		logger.Error("[RS232] Program execution failed:", err)
	} else {
		logger.Info("[RS232] Program execution completed successfully")
	}

	if fileUpdatedDuringExecution.Load() {
		pending, _ := pendingCommand.Load().(string)
		logger.Info("[RS232] Deferred update detected, re-executing latest program. Triggered by:", pending)
		fileUpdatedDuringExecution.Store(false)
		pendingCommand.Store("")
		executionInProgress.Store(true)
		go executeFilesProgramOnce()
	}
}

// ------------------------------------------------------------
// PARSER
// ------------------------------------------------------------

func parseBatchCommands(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}

	if strings.Count(s, ";") > 1 {
		raw := strings.Split(s, ";")
		out := make([]string, 0, len(raw)*2)
		for _, r := range raw {
			r = strings.TrimSpace(r)
			if r == "" {
				continue
			}
			// A segment may itself contain concatenated FANUC commands such as
			// G01F5G90G68B15.000. Parse that segment fully rather than treating it
			// as one G token; this is required when the remaining two B votes are
			// separated by semicolons.
			nested := parseBatchCommands(r)
			if len(nested) == 0 {
				return nil
			}
			out = append(out, nested...)
		}
		return out
	}

	s = strings.TrimSuffix(s, ";")
	s = strings.TrimSpace(s)

	if strings.Contains(s, ",") {
		raw := strings.Split(s, ",")
		out := make([]string, 0, len(raw)*2)
		for _, r := range raw {
			r = strings.TrimSpace(r)
			if r == "" {
				continue
			}
			// Important for the intended FANUC triple format:
			//   G01F5G90G68B15.000,B15.000,B15.000
			// The first comma field contains several concatenated commands. Parse it
			// recursively so the voter sees all three B tokens, not only the last two.
			nested := parseBatchCommands(r)
			if len(nested) == 0 {
				return nil
			}
			out = append(out, nested...)
		}
		return out
	}

	if strings.Contains(s, " ") || strings.Contains(s, "\t") {
		toks := strings.Fields(s)
		if len(toks) == 0 {
			return nil
		}

		isCmdStart := func(t string) bool {
			if t == "" {
				return false
			}
			u := strings.ToUpper(strings.TrimSuffix(t, ";"))
			switch u[0] {
			case 'G', 'M', 'A', 'B', 'X', 'Y', 'Z', 'D':
				return true
			default:
				return false
			}
		}

		isParam := func(t string) bool {
			if t == "" {
				return false
			}
			u := strings.ToUpper(strings.TrimSuffix(t, ";"))
			switch u[0] {
			case 'F', 'P', 'I', 'J', 'K', 'R', 'X', 'Y', 'Z':
				return true
			default:
				return false
			}
		}

		var out []string
		var cur []string
		for _, t := range toks {
			if isCmdStart(t) {
				if len(cur) > 0 {
					out = append(out, strings.Join(cur, " "))
					cur = cur[:0]
				}
				cur = append(cur, t)
				continue
			}
			if len(cur) > 0 && isParam(t) {
				cur = append(cur, t)
				continue
			}
			if len(cur) > 0 {
				cur = append(cur, t)
			}
		}
		if len(cur) > 0 {
			out = append(out, strings.Join(cur, " "))
		}
		return out
	}

	return splitConcatenatedBatch(s)
}

func splitConcatenatedBatch(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}

	u := strings.ToUpper(s)

	isStart := func(c byte) bool {
		switch c {
		case 'G', 'M', 'A', 'B', 'D':
			return true
		default:
			return false
		}
	}

	var out []string
	i := 0
	for i < len(u) {
		if u[i] == ' ' || u[i] == '\t' {
			i++
			continue
		}
		if !isStart(u[i]) {
			// After transport sanitization there should be no arbitrary printable
			// prefix left. Reject rather than silently skipping an unknown command.
			return nil
		}
		start := i
		i++
		if u[start] == 'G' || u[start] == 'M' {
			for i < len(u) && u[i] >= '0' && u[i] <= '9' {
				i++
			}
			for i < len(u) && !isStart(u[i]) {
				i++
			}
		} else {
			for i < len(u) && !isStart(u[i]) {
				i++
			}
		}
		if token := strings.TrimSpace(s[start:i]); token != "" {
			out = append(out, token)
		}
	}
	return out
}

func normalizeControllerToken(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}

	if v, err := strconv.ParseFloat(s, 64); err == nil {
		return fmt.Sprintf("A%g;", v)
	}

	s = strings.TrimSuffix(s, ";")
	s = strings.ReplaceAll(s, ",", " ")
	s = strings.ToUpper(s)

	if strings.HasPrefix(s, "GO") && len(s) >= 3 {
		s = "G0" + s[2:]
	}

	if len(s) == 0 {
		return ""
	}

	// Axis values must be numeric. Accept any amount of whitespace between the
	// axis letter and value, but never accept malformed values such as BABC.
	if strings.ContainsRune("ABXYZD", rune(s[0])) {
		value := strings.Join(strings.Fields(s[1:]), "")
		if value == "" {
			logger.Warn("[RS232] Axis command missing numeric value:", s)
			return ""
		}
		if _, err := strconv.ParseFloat(value, 64); err != nil {
			logger.Warn("[RS232] Invalid axis value rejected:", s)
			return ""
		}
		return string(s[0]) + value + ";"
	}

	// Standalone feed values are numeric too.
	if s[0] == 'F' {
		value := strings.Join(strings.Fields(s[1:]), "")
		if value == "" {
			return ""
		}
		if _, err := strconv.ParseFloat(value, 64); err != nil {
			logger.Warn("[RS232] Invalid feed value rejected:", s)
			return ""
		}
		return "F" + value + ";"
	}

	switch s[0] {
	case 'G', 'M', 'A', 'B', 'X', 'Y', 'Z', 'D', 'F':
	default:
		logger.Warn("[RS232] Unrecognized command token rejected:", s)
		return ""
	}

	if s[0] == 'G' || s[0] == 'M' {
		s = insertSpacesBeforeLetters(s)
	}

	s = strings.Join(strings.Fields(s), " ")

	if strings.HasPrefix(s, "G0 F") {
		s = "G01" + s[2:]
	}

	if !strings.HasSuffix(s, ";") {
		s += ";"
	}
	return s
}

func insertSpacesBeforeLetters(s string) string {
	var out []rune
	var prev rune
	for i, r := range s {
		if i > 0 && (r >= 'A' && r <= 'Z') && (prev >= '0' && prev <= '9') {
			out = append(out, ' ')
		}
		out = append(out, r)
		prev = r
	}
	return string(out)
}

// ------------------------------------------------------------
// FILE UPDATE HELPERS
// ------------------------------------------------------------

func isAxisKey(k string) bool {
	if k == "" {
		return false
	}
	switch k[0] {
	case 'A', 'B', 'X', 'Y', 'Z', 'D':
		return true
	default:
		return false
	}
}

func findInsertBeforeEnd(lines []string) int {
	for i, line := range lines {
		lt := strings.TrimSpace(line)
		if strings.HasPrefix(lt, "M99") || strings.HasPrefix(lt, "M30") {
			return i
		}
	}
	return len(lines)
}

// updateFILESFromRS232 is the fallback token-by-token updater used when
// rebuildFILES fails. Kept for safety — should rarely be reached.
func updateFILESFromRS232(oneCmd string) error {
	oneCmd = strings.TrimSpace(oneCmd)
	if oneCmd == "" {
		return fmt.Errorf("empty command")
	}
	if !strings.HasSuffix(oneCmd, ";") {
		oneCmd += ";"
	}

	content, err := os.ReadFile(getFilesPath())
	if err != nil {
		return fmt.Errorf("failed to read FILES: %w", err)
	}
	lines := strings.Split(string(content), "\n")

	cmdNoSemi := strings.TrimSuffix(oneCmd, ";")
	toks := strings.Fields(cmdNoSemi)
	if len(toks) == 0 {
		return fmt.Errorf("invalid command: %q", oneCmd)
	}
	cmdKey := toks[0]
	replaced := false

	for i, line := range lines {
		lt := strings.TrimSpace(line)
		if lt == "" {
			continue
		}
		if isAxisKey(cmdKey) {
			if strings.HasPrefix(lt, string(cmdKey[0])) {
				lines[i] = oneCmd
				replaced = true
				break
			}
			continue
		}
		if cmdKey == "G90" || cmdKey == "G91" {
			if strings.HasPrefix(lt, "G90") || strings.HasPrefix(lt, "G91") {
				lines[i] = oneCmd
				replaced = true
				break
			}
			continue
		}
		if strings.HasPrefix(lt, cmdKey) {
			lines[i] = oneCmd
			replaced = true
			break
		}
	}

	if !replaced {
		idx := findInsertBeforeEnd(lines)
		lines = append(lines[:idx], append([]string{oneCmd}, lines[idx:]...)...)
	}

	return writeFILESAtomic(lines)
}