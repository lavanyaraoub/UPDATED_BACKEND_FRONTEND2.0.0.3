// Package cmmlink lets CMM metrology software command the RTC over Ethernet.
//
// Design: moves are executed exactly like the RS232 path, by writing a small
// G-code program and running it with executors.RunCodeFile(). That reuses the
// existing interlocks, pitch-error/backlash compensation, G68 shortest path,
// block-wait and alarm handling. The CMM link uses its own program file (M1),
// so it never collides with RS232's FILES. Jog, Zero Reference, Reset and
// software E-stop use the same driver actions the UI sends.
//
// Protocol (ASCII, one command per line, ended by CR, LF or CR LF):
//   MOVE <deg> [F<feed>]     absolute move      -> ACK, later DONE <deg> | ERR
//   MOVEREL <deg> [F<feed>]  incremental move   -> ACK, later DONE <deg> | ERR
//   JOG + [<feed>] / JOG - [<feed>]  continuous jog -> ACK
//                            (auto-stops after 30 s unless JOG is re-sent,
//                             and immediately if the client disconnects)
//   JOGSTOP                  stop jog            -> ACK
//   HOME                     Zero Reference      -> ACK, later DONE <deg> | ERR
//   STOP                     stop move / jog     -> ACK
//   ESTOP                    software emergency  -> ACK (powers drives off)
//   RESET                    drive reset         -> ACK
//   POS?                     -> POS <deg>
//   STATUS?                  -> STATUS HOMED= INPOS= ALARM= RS232= JOG= BUSY=
//
// Start from main alongside the RS232 listener:  go cmmlink.Start()
package cmmlink

import (
	"bufio"
	"bytes"
	"fmt"
	"math"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	channels "EtherCAT/channels"
	executors "EtherCAT/executors"
	"EtherCAT/helper"
	"EtherCAT/logger"
	motor "EtherCAT/motordriver"
)

const (
	// Bound to the CMM-link adapter only, so Wi-Fi clients cannot command motion.
	listenAddr = "192.168.50.10:5100"

	driveName   = "A"
	programName = "M1"

	// Same axis letter as the field-proven RS232 FILES program (B<deg>;).
	axisLetter = "B"

	defaultFeed     = 20.0 // same default as rebuildFILES (G01 F20;)
	maxFeed         = 20.0 // TODO: confirm the machine's maximum programmed feed
	maxJogFeed      = 20.0 // same range the UI Manual Jog accepts (0..20)
	useShortestPath = true // absolute moves use G90 + G68

	inPositionTolDeg = 0.01 // TODO: set from the measurement plan
	moveTimeout      = 60 * time.Second
	homeTimeout      = 120 * time.Second
	homeSettleTime   = 1 * time.Second
	homeStartDelay   = 2 * time.Second
	jogDeadman       = 30 * time.Second
	relistenDelay    = 2 * time.Second
)

var (
	busy          atomic.Bool  // a MOVE / MOVEREL / HOME is running
	stopRequested atomic.Bool  // STOP or ESTOP received during the current operation
	jogging       atomic.Bool  // a jog started by this link is active
	jogDir        atomic.Int32 // +1 / -1 of the active jog
	jogDeadline   atomic.Int64 // unix nanos; jog auto-stops after this
)

// positionReader returns the actual table angle in degrees (encoder based),
// computed the same way as the angle the UI displays.
var positionReader = func() (float64, error) {
	return motor.GetActualPositionDeg(driveName)
}

// Start runs the CMM listener forever. If the adapter or its IP is missing
// (unplugged, not yet up at boot), it keeps retrying instead of giving up.
func Start() {
	for {
		ln, err := net.Listen("tcp", listenAddr)
		if err != nil {
			logger.Warn("[CMM] listen failed, retrying:", err)
			time.Sleep(relistenDelay)
			continue
		}
		logger.Info("[CMM] listening on", listenAddr)
		for {
			nc, err := ln.Accept()
			if err != nil {
				logger.Warn("[CMM] accept failed, re-listening:", err)
				break
			}
			handle(nc) // one client at a time
		}
		ln.Close()
		time.Sleep(relistenDelay)
	}
}

// client serialises writes: DONE/ERR come from worker goroutines while the
// read loop keeps answering STATUS?/POS?/STOP.
type client struct {
	nc net.Conn
	mu sync.Mutex
}

func (c *client) send(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.nc.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err := fmt.Fprintf(c.nc, "%s\r\n", msg); err != nil {
		logger.Warn("[CMM] write failed:", err, "msg:", msg)
		return
	}
	logger.Info("[CMM] ->", msg)
}

func handle(nc net.Conn) {
	defer nc.Close()
	remote := nc.RemoteAddr().String()
	c := &client{nc: nc}
	logger.Info("[CMM] client connected:", remote)

	sc := bufio.NewScanner(nc)
	sc.Split(splitLines) // accept CR, LF or CR LF as the line ending
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		logger.Info("[CMM] <-", line)
		f := strings.Fields(strings.ToUpper(line))

		switch f[0] {
		case "MOVE":
			handleMove(c, f, false)

		case "MOVEREL":
			handleMove(c, f, true)

		case "JOG":
			handleJog(c, f)

		case "JOGSTOP":
			stopJog("JOGSTOP")
			c.send("ACK")

		case "HOME":
			handleHome(c)

		case "STOP":
			if busy.Load() {
				stopRequested.Store(true)
			}
			if jogging.Load() {
				stopJog("STOP")
			}
			// Same pair the UI's stop_execution sends.
			channels.WriteCommandExecInput("stop_prog_exec", "")
			channels.WriteCommandExecInput("move_next_line", "1")
			c.send("ACK")

		case "ESTOP":
			if busy.Load() {
				stopRequested.Store(true)
			}
			jogging.Store(false)
			channels.NotifyMotorDriver("EMERGENCY", "", driveName, 0)
			c.send("ACK")

		case "RESET":
			if busy.Load() || jogging.Load() {
				c.send("ERR E03 BUSY")
				continue
			}
			channels.NotifyMotorDriver("RESET", "", driveName, 0)
			c.send("ACK")

		case "POS?":
			pos, err := positionReader()
			if err != nil {
				c.send("ERR E08 POS_UNAVAILABLE")
				continue
			}
			c.send("POS %.4f", pos)

		case "STATUS?":
			homed := motor.IsPositionReferenceValid(driveName)
			alarm := motor.GetCurrentAlarm() != "No Alarms"
			running := busy.Load()
			jog := jogging.Load()
			c.send("STATUS HOMED=%d INPOS=%d ALARM=%d RS232=%d JOG=%d BUSY=%d",
				b2i(homed), b2i(homed && !running && !jog && !alarm), b2i(alarm),
				b2i(executors.IsRS232Enabled()), b2i(jog), b2i(running))

		default:
			c.send("ERR E99 UNKNOWN_CMD")
		}
	}

	// Dead-man: a jog must never outlive the client that started it.
	// A MOVE or HOME in progress is NOT stopped: it completes on its own,
	// and the client checks STATUS? after reconnecting.
	if jogging.Load() {
		stopJog("client disconnected")
	}
	logger.Info("[CMM] client disconnected:", remote)
}

// ---------------------------------------------------------------- interlocks

// baseInterlock: RS232Enabled changes executor behaviour globally (fresh
// start, 60 s wait for FILES updates), so the CMM link refuses motion while
// it is on. An active drive alarm also blocks all motion.
func baseInterlock() string {
	switch {
	case executors.IsRS232Enabled():
		return "E06 RS232_ACTIVE"
	case motor.GetCurrentAlarm() != "No Alarms":
		return "E02 ALARM_ACTIVE"
	case jogging.Load():
		return "E03 BUSY JOG_ACTIVE"
	case busy.Load():
		return "E03 BUSY"
	}
	return ""
}

// ---------------------------------------------------------------- MOVE / MOVEREL

func handleMove(c *client, f []string, relative bool) {
	if len(f) < 2 || len(f) > 3 {
		c.send("ERR E10 BAD_ARGS")
		return
	}
	val, err := strconv.ParseFloat(f[1], 64)
	if err != nil {
		c.send("ERR E11 BAD_ANGLE")
		return
	}
	if relative {
		if val == 0 || math.Abs(val) > 360 {
			c.send("ERR E11 BAD_ANGLE")
			return
		}
	} else if val < 0 || val >= 360 {
		c.send("ERR E11 BAD_ANGLE")
		return
	}

	feed := defaultFeed
	if len(f) == 3 {
		feed, err = parseFeed(f[2], maxFeed)
		if err != nil {
			c.send("ERR E12 BAD_FEED")
			return
		}
	}

	if code := baseInterlock(); code != "" {
		c.send("ERR %s", code)
		return
	}
	if !motor.IsPositionReferenceValid(driveName) {
		c.send("ERR E01 NOT_HOMED")
		return
	}
	if !busy.CompareAndSwap(false, true) {
		c.send("ERR E03 BUSY")
		return
	}

	target := val
	if relative {
		start, err := positionReader()
		if err != nil {
			busy.Store(false)
			c.send("ERR E08 POS_UNAVAILABLE")
			return
		}
		target = norm360(start + val)
	}

	path, err := writeProgram(relative, val, feed)
	if err != nil {
		busy.Store(false)
		logger.Error("[CMM] writing program failed:", err)
		c.send("ERR E09 PROGRAM_WRITE_FAILED")
		return
	}
	stopRequested.Store(false)
	c.send("ACK")
	go runMove(c, path, target)
}

func writeProgram(relative bool, val, feed float64) (string, error) {
	lines := []string{"G01 F" + fmtNum(feed) + ";"}
	if relative {
		lines = append(lines, "G91;") // G68 is not valid in incremental mode
	} else {
		lines = append(lines, "G90;")
		if useShortestPath {
			lines = append(lines, "G68;")
		}
	}
	lines = append(lines, axisLetter+fmtNum(val)+";", "M30;")

	path := helper.GetCodeFilePath() + "/" + programName
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", err
	}
	return path, nil
}

func runMove(c *client, path string, target float64) {
	defer busy.Store(false)

	// Each CMM move is a fresh one-shot program: never resume mid-file.
	executors.ClearExecutionResumeState()

	done := make(chan error, 1)
	go func() { done <- executors.RunCodeFile(path) }()

	var execErr error
	select {
	case execErr = <-done:
	case <-time.After(moveTimeout):
		logger.Error("[CMM] move timeout, stopping program")
		channels.WriteCommandExecInput("stop_prog_exec", "")
		channels.WriteCommandExecInput("move_next_line", "1")
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			logger.Error("[CMM] executor did not unwind 10 s after stop")
		}
		c.send("ERR E05 TIMEOUT%s", posSuffix())
		return
	}

	if execErr != nil {
		c.send("ERR E05 EXEC_FAILED %s", oneLine(execErr.Error()))
		return
	}

	// RunCodeFile also returns nil after a stop or an alarm-triggered stop,
	// so success is confirmed independently before DONE is sent.
	if stopRequested.Load() {
		c.send("ERR E04 STOPPED%s", posSuffix())
		return
	}
	if alarm := motor.GetCurrentAlarm(); alarm != "No Alarms" {
		c.send("ERR E05 FAULT %s%s", oneLine(alarm), posSuffix())
		return
	}
	pos, err := positionReader()
	if err != nil {
		c.send("ERR E08 POS_UNAVAILABLE")
		return
	}
	if diff := angleDiff(pos, target); diff > inPositionTolDeg {
		c.send("ERR E05 NOT_IN_POSITION POS %.4f", pos)
		return
	}
	c.send("DONE %.4f", pos)
}

// ---------------------------------------------------------------- JOG

// JOG + [feed] / JOG - [feed]. Without a feed, the drive uses the Machine
// Parameter jog feed, exactly like legacy UI/GPIO jog.
func handleJog(c *client, f []string) {
	if len(f) < 2 || len(f) > 3 {
		c.send("ERR E10 BAD_ARGS")
		return
	}
	var dir int32
	switch f[1] {
	case "+":
		dir = 1
	case "-":
		dir = -1
	default:
		c.send("ERR E10 BAD_ARGS use JOG + or JOG -")
		return
	}

	value := ""
	if len(f) == 3 {
		feed, err := parseFeed(f[2], maxJogFeed)
		if err != nil {
			c.send("ERR E12 BAD_FEED")
			return
		}
		value = fmt.Sprintf("%.6f", feed)
	}

	// Same direction while already jogging: just renew the dead-man.
	if jogging.Load() {
		if jogDir.Load() != dir {
			c.send("ERR E03 BUSY JOG_ACTIVE send JOGSTOP first")
			return
		}
		jogDeadline.Store(time.Now().Add(jogDeadman).UnixNano())
		c.send("ACK")
		return
	}

	switch {
	case executors.IsRS232Enabled():
		c.send("ERR E06 RS232_ACTIVE")
		return
	case motor.GetCurrentAlarm() != "No Alarms":
		c.send("ERR E02 ALARM_ACTIVE")
		return
	case busy.Load():
		c.send("ERR E03 BUSY")
		return
	}

	jogDir.Store(dir)
	jogDeadline.Store(time.Now().Add(jogDeadman).UnixNano())
	jogging.Store(true)
	channels.DriverActionChannel <- channels.DriverAction{
		Action:    "MANUAL_JOG",
		Direction: int(dir),
		DriveName: driveName,
		Value:     value,
	}
	go jogWatchdog()
	c.send("ACK")
}

func stopJog(reason string) {
	jogging.Store(false)
	channels.DriverActionChannel <- channels.DriverAction{
		Action:    "STOP_JOG",
		DriveName: driveName,
	}
	logger.Info("[CMM] jog stopped:", reason)
}

func jogWatchdog() {
	t := time.NewTicker(200 * time.Millisecond)
	defer t.Stop()
	for range t.C {
		if !jogging.Load() {
			return
		}
		if time.Now().UnixNano() > jogDeadline.Load() {
			stopJog("dead-man timeout (JOG not renewed)")
			return
		}
	}
}

// ---------------------------------------------------------------- HOME

// HOME runs the same Zero Reference as the UI button and GPIO 22.
// Completion is detected by polling: reference valid, no alarm, and the
// position steady for homeSettleTime.
// TODO: replace polling with the driver's own "zero reference complete"
// notification once it is exposed.
func handleHome(c *client) {
	if code := baseInterlock(); code != "" {
		c.send("ERR %s", code)
		return
	}
	if !busy.CompareAndSwap(false, true) {
		c.send("ERR E03 BUSY")
		return
	}
	stopRequested.Store(false)
	channels.NotifyMotorDriver("ZERO_REF", "", driveName, 0)
	c.send("ACK")
	go waitHomeDone(c)
}

func waitHomeDone(c *client) {
	defer busy.Store(false)

	time.Sleep(homeStartDelay) // let the zero-reference move begin
	deadline := time.Now().Add(homeTimeout)

	last, err := positionReader()
	if err != nil {
		c.send("ERR E08 POS_UNAVAILABLE")
		return
	}
	stableSince := time.Now()

	t := time.NewTicker(50 * time.Millisecond)
	defer t.Stop()
	for range t.C {
		if stopRequested.Load() {
			c.send("ERR E04 STOPPED%s", posSuffix())
			return
		}
		if alarm := motor.GetCurrentAlarm(); alarm != "No Alarms" {
			c.send("ERR E05 FAULT %s%s", oneLine(alarm), posSuffix())
			return
		}
		if time.Now().After(deadline) {
			c.send("ERR E05 TIMEOUT%s", posSuffix())
			return
		}
		pos, err := positionReader()
		if err != nil {
			continue
		}
		if angleDiff(pos, last) > 0.0005 {
			last = pos
			stableSince = time.Now()
			continue
		}
		if time.Since(stableSince) >= homeSettleTime && motor.IsPositionReferenceValid(driveName) {
			c.send("DONE %.4f", pos)
			return
		}
	}
}

// ---------------------------------------------------------------- helpers

// splitLines is a bufio.SplitFunc that ends a line at CR, LF or CR LF, so
// clients that send only CR (common in terminal tools) also work. An empty
// line produced by a CR LF split across reads is skipped by the caller.
func splitLines(data []byte, atEOF bool) (int, []byte, error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		j := i + 1
		if data[i] == '\r' && j < len(data) && data[j] == '\n' {
			j++
		}
		return j, data[:i], nil
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// parseFeed accepts "10" or "F10"; must be > 0 and <= limit.
func parseFeed(s string, limit float64) (float64, error) {
	s = strings.TrimPrefix(s, "F")
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v <= 0 || v > limit {
		return 0, fmt.Errorf("bad feed %q", s)
	}
	return v, nil
}

func posSuffix() string {
	if pos, err := positionReader(); err == nil {
		return fmt.Sprintf(" POS %.4f", pos)
	}
	return ""
}

// angleDiff is the smallest absolute difference between two angles, in degrees.
func angleDiff(a, b float64) float64 {
	d := norm360(a - b)
	if d > 180 {
		d = 360 - d
	}
	return d
}

func norm360(x float64) float64 {
	x = math.Mod(x, 360)
	if x < 0 {
		x += 360
	}
	return x
}

func fmtNum(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}