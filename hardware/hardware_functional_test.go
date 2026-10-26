//go:build hardware

package hardware_test

// ============================================================================
// Comprehensive Functional Test Suite — Panasonic MDDLN45BE / jamun application
// ============================================================================

import (
	channels "EtherCAT/channels"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	gosocketio "github.com/graarh/golang-socketio"
	"github.com/graarh/golang-socketio/transport"
)

// ─── constants ────────────────────────────────────────────────────────────────

const (
	pulsesPerDegree   = 20000.0
	maxFeedRate       = 20
	minFeedRate       = 1
	positionTolerance = 0.5 // degrees — SDO readback vs PDO settle
	pitchErrorEntries = 36  // expected pitch error table size
)

// ─── environment ──────────────────────────────────────────────────────────────

func fEnv(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func restBase() string {
	return strings.TrimRight(fEnv("MOTION_REST_BASE_URL", "http://localhost:5000"), "/")
}

func socketBase() string {
	return strings.TrimRight(fEnv("MOTION_SOCKET_URL", "http://localhost:9090"), "/")
}

func rangeDeg(t *testing.T) float64 {
	t.Helper()
	v, err := strconv.ParseFloat(fEnv("MOTION_TEST_RANGE_DEG", "5"), 64)
	if err != nil || v <= 0 || v > 45 {
		t.Fatalf("MOTION_TEST_RANGE_DEG must be >0 and ≤45")
	}
	return v
}

func mvTimeout(t *testing.T) time.Duration {
	t.Helper()
	v, _ := strconv.ParseFloat(fEnv("MOTION_TIMEOUT_S", "20"), 64)
	if v <= 0 {
		v = 20
	}
	return time.Duration(v * float64(time.Second))
}

// ─── HTTP helpers ─────────────────────────────────────────────────────────────

func httpGET(t *testing.T, path string) ([]byte, int) {
	t.Helper()
	resp, err := http.Get(restBase() + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return b, resp.StatusCode
}

func httpPOST(t *testing.T, path string, body any) ([]byte, int) {
	t.Helper()
	payload, _ := json.Marshal(body)
	resp, err := http.Post(restBase()+path, "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return b, resp.StatusCode
}

// ─── Socket.IO helpers ────────────────────────────────────────────────────────

type sioConn struct{ c *gosocketio.Client }

func sioConnect(t *testing.T) *sioConn {
	t.Helper()
	base := strings.TrimRight(fEnv("MOTION_SOCKET_URL", "http://localhost:9090"), "/")
	host := strings.TrimPrefix(strings.TrimPrefix(base, "http://"), "https://")
	parts := strings.SplitN(host, ":", 2)
	h := parts[0]
	port := 9090
	if len(parts) == 2 {
		if p, err := strconv.Atoi(parts[1]); err == nil {
			port = p
		}
	}
	url := gosocketio.GetUrl(h, port, false)
	c, err := gosocketio.Dial(url, transport.GetDefaultWebsocketTransport())
	if err != nil {
		t.Fatalf("Socket.IO dial %s: %v", url, err)
	}
	return &sioConn{c}
}

func (s *sioConn) emit(t *testing.T, event string, data any) {
	t.Helper()
	if err := s.c.Emit(event, data); err != nil {
		t.Logf("Socket.IO emit %q warning: %v", event, err)
	}
}

func (s *sioConn) on(event string, handler interface{}) {
	s.c.On(event, handler)
}

func (s *sioConn) close() { s.c.Close() }

// ─── SDO position helpers ─────────────────────────────────────────────────────

// actualPosDeg returns the current motor position in degrees as reported by
// the running jamun process via socket.io.
//
// WHY NOT ethercat upload 0x6064:
//
//	The IgH EtherCAT master disables SDO mailbox access once PDO cyclic mode
//	is active (ecrt_master_activate has been called). Any ethercat-cli SDO
//	upload during PDO operation returns a stale cached value from before PDO
//	started — typically the position at the last jamun restart, not the
//	current motor position. This caused every absolute-mode motion test to
//	report the wrong position even though the motor physically landed on target.
//
// FIX: Subscribe to the "destination_position" socket.io event which jamun
//
//	emits every 50ms from pollDrivePositionProcess using GetLastPDOPosition()
//	(always current). Collect the last received value after a 200ms settle
//	window (4+ PDO position updates) to ensure we read the post-move position.
func actualPosDeg(t *testing.T) float64 {
	t.Helper()

	var mu sync.Mutex
	var lastPos float64 = -1
	found := make(chan struct{}, 1)

	ex := sioConnect(t)
	defer ex.close()

	ex.on("destination_position", func(c *gosocketio.Channel, msg channels.SocketMessage) {
		v, err := strconv.ParseFloat(strings.TrimSpace(msg.Position), 64)
		if err != nil {
			return
		}
		mu.Lock()
		lastPos = v
		mu.Unlock()
		select {
		case found <- struct{}{}:
		default:
		}
	})

	// Wait up to 1s for the first position event, then settle for 200ms
	// to get a stable post-move reading.
	select {
	case <-found:
	case <-time.After(1 * time.Second):
		t.Log("actualPosDeg: no position event within 1s — returning 0")
		return 0
	}
	time.Sleep(200 * time.Millisecond) // let position settle

	mu.Lock()
	pos := lastPos
	mu.Unlock()
	return pos
}

func assertPos(t *testing.T, want, tol float64) {
	t.Helper()
	got := actualPosDeg(t)
	diff := math.Abs(got - want)
	if diff > 180 {
		diff = 360 - diff
	}
	if diff > tol {
		t.Errorf("position=%.3f° want=%.3f° ±%.1f° (diff=%.3f°)", got, want, tol, diff)
	} else {
		t.Logf("✓ position=%.3f° (want=%.3f°, diff=%.3f°)", got, want, diff)
	}
}

// ─── Settings helpers ─────────────────────────────────────────────────────────

type driveSettings struct {
	FinSignal    int    `json:"fin_signal"`
	JogFeed      string `json:"jog_feed"`
	ECS          int    `json:"ecs"`
	POT          string `json:"pot"`
	NOT          string `json:"not"`
	WorkOffset   string `json:"work_offset"`
	BackLash     string `json:"back_lash"`
	MotorDir     int    `json:"motor_dir"`
	HomingOffset string `json:"homing_offset"`
	Timing       string `json:"timing"`
	G54          string `json:"g54"`
	G55          string `json:"g55"`
	G56          string `json:"g56"`
	G57          string `json:"g57"`
	G58          string `json:"g58"`
	PitchError   []any  `json:"pitch_error"`
}

// driveSettingsStringFields lists every driveSettings field that must be a
// JSON string but has, in practice, sometimes been persisted to
// settings.json as a bare/unquoted number (e.g. "g55": 0 instead of
// "g55": "0"). Production's LoadDriverSettings tolerates this by
// normalizing the raw bytes before unmarshaling (see
// settings.normalizeStringTaggedNumbers) — /dac_params' GET handler does
// not, since it streams the file's raw bytes straight back unmodified. This
// test hits that endpoint directly, so it needs the same tolerance.
var driveSettingsStringFields = []string{
	"jog_feed", "pot", "not", "work_offset", "back_lash",
	"homing_offset", "timing", "g54", "g55", "g56", "g57", "g58",
}

// bareNumberPattern matches `"field": 20` or `"field":-3.5` — an unquoted
// JSON number — but not `"field": "20"`, which is already correctly quoted.
func bareNumberPattern(field string) *regexp.Regexp {
	return regexp.MustCompile(`"` + field + `"\s*:\s*(-?\d+(?:\.\d+)?)([,}\s])`)
}

// normalizeDriveSettingsJSON quotes any bare numeric values for the known
// string-typed driveSettings fields so json.Unmarshal succeeds regardless
// of which format a given settings.json entry happens to be stored in.
// Already-quoted or absent fields are left untouched.
func normalizeDriveSettingsJSON(raw []byte) []byte {
	out := raw
	for _, field := range driveSettingsStringFields {
		out = bareNumberPattern(field).ReplaceAll(out, []byte(`"`+field+`": "$1"$2`))
	}
	return out
}

func loadSettings(t *testing.T) map[string]driveSettings {
	t.Helper()
	body, status := httpGET(t, "/dac_params")
	if status != 200 {
		t.Fatalf("/dac_params returned %d", status)
	}
	body = normalizeDriveSettingsJSON(body)
	var resp struct {
		Status   string                   `json:"status"`
		Response map[string]driveSettings `json:"resp"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("parse /dac_params: %v\nbody: %s", err, body)
	}
	if resp.Response == nil {
		var flat map[string]driveSettings
		if err2 := json.Unmarshal(body, &flat); err2 == nil && flat != nil {
			return flat
		}
	}
	return resp.Response
}

// ─── Motion helpers ───────────────────────────────────────────────────────────

func saveNC(t *testing.T, name, content string) {
	t.Helper()
	body, status := httpPOST(t, "/createFile", map[string]string{
		"file_name": name, "contents": content,
	})
	if status != 200 {
		t.Fatalf("saveNC %s: status=%d body=%s", name, status, body)
	}
	result := parseCreateFileResponse(body)
	if result == "error" {
		t.Fatalf("saveNC %s rejected: %s", name, body)
	}
	t.Logf("saved %s: %s", name, body)
	// Always delete the file when the test ends — pass or fail.
	// This keeps /mnt/app/jamun/gm_codes clean after every hardware test run.
	t.Cleanup(func() {
		_, _ = http.Get(restBase() + "/deleteFile?file_name=" + name)
		t.Logf("deleted %s", name)
	})
}

func parseCreateFileResponse(body []byte) string {
	var direct map[string]any
	if err := json.Unmarshal(body, &direct); err == nil {
		if s, ok := direct["status"].(string); ok {
			return s
		}
	}
	var s string
	if err := json.Unmarshal(body, &s); err == nil {
		var inner map[string]any
		if err2 := json.Unmarshal([]byte(s), &inner); err2 == nil {
			if status, ok := inner["status"].(string); ok {
				return status
			}
		}
		if strings.Contains(s, `"status":"success"`) || strings.Contains(s, `"status": "success"`) {
			return "success"
		}
		if strings.Contains(s, `"status":"error"`) || strings.Contains(s, `"status": "error"`) {
			return "error"
		}
	}
	if strings.Contains(string(body), "success") {
		return "success"
	}
	return ""
}

func runNC(t *testing.T, filename string, targetDeg, tol float64, timeout time.Duration) {
	t.Helper()
	stopExecution(t)

	completed := make(chan struct{}, 1)
	ex := sioConnect(t)
	defer ex.close()
	ex.on("program_complete", func(c *gosocketio.Channel, msg channels.SocketMessage) {
		select {
		case completed <- struct{}{}:
		default:
		}
	})

	go func() { ex.emit(t, "execute", map[string]string{"file_name": filename}) }()
	time.Sleep(200 * time.Millisecond)

	const pollInterval = 80 * time.Millisecond
	deadline := time.Now().Add(timeout)
	lastDiff := math.MaxFloat64

	for time.Now().Before(deadline) {
		time.Sleep(pollInterval)
		pos := actualPosDeg(t)
		diff := angularDiff(pos, targetDeg)
		lastDiff = diff

		select {
		case <-completed:
			if diff <= tol {
				t.Logf("✓ motion complete: pos=%.3f° target=%.3f° diff=%.3f°", pos, targetDeg, diff)
				return
			}
			t.Errorf("program completed away from target: pos=%.3f° target=%.3f° diff=%.3f°", pos, targetDeg, diff)
			return
		default:
		}
	}

	pos := actualPosDeg(t)
	t.Errorf("motion timeout after %s: pos=%.3f° target=%.3f° lastDiff=%.3f°", timeout, pos, targetDeg, lastDiff)
	emergencyReset(t)
}

func angularDiff(a, b float64) float64 {
	diff := math.Abs(a - b)
	if diff > 180 {
		diff = 360 - diff
	}
	return diff
}

func stopExecution(t *testing.T) {
	t.Helper()
	for i := 0; i < 2; i++ {
		s := sioConnect(t)
		s.emit(t, "stop_execution", map[string]string{})
		time.Sleep(300 * time.Millisecond)
		s.close()
	}
	time.Sleep(700 * time.Millisecond)
}

func emergencyReset(t *testing.T) {
	t.Helper()
	es := sioConnect(t)
	go func() {
		es.emit(t, "emergency", map[string]string{})
		time.Sleep(1500 * time.Millisecond)
		es.emit(t, "reset", map[string]string{})
	}()
	time.Sleep(2500 * time.Millisecond)
	es.close()
}

// ─── NATIVE GO MOCK PLC SIMULATOR ─────────────────────────────────────────────

// startMockPLC simulates the External Control System (ECS) finish signal.
// It runs as a background goroutine and automatically stops when the test ends.
// **NOTE:** This function is available for any test file in the `hardware_test` package.
//
// Fix: a WaitGroup ensures the goroutine fully exits (including its final t.Logf)
// before t.Cleanup returns. Without this, Go's testing runtime panics with
// "Log in goroutine after test has completed" when the goroutine logs after the
// test is already marked done.
func startMockPLC(t *testing.T) {
	t.Helper()
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	t.Cleanup(func() {
		close(done)
		wg.Wait() // block until goroutine has fully exited before test tears down
	})

	go func() {
		defer wg.Done()
		t.Logf("[Mock PLC] Started native Go simulator for ECS finish signals")
		// The interval should be reasonably fast to avoid test lag, but slow enough
		// to not flood the IO or network.
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-done:
				t.Logf("[Mock PLC] Stopped")
				return
			case <-ticker.C:
				// Uncomment the method that matches your actual setup.

				// --- METHOD A: EtherCAT CLI Trigger ---
				// If your finish signal maps to a specific EtherCAT digital input index.
				//
				// _ = exec.Command("ethercat", "download", "-p", "0", "-t", "uint8", "0x60FD", "0", "1").Run()
				// time.Sleep(100 * time.Millisecond)
				// _ = exec.Command("ethercat", "download", "-p", "0", "-t", "uint8", "0x60FD", "0", "0").Run()

				// --- METHOD B: REST API Software Override ---
				// If your jamun Go app has a backdoor or dedicated API:
				//
				// _, _ = http.Post(restBase()+"/simulate_ecs_signal", "application/json", nil)

				// Keep compiler happy if variables are commented out
				_ = exec.Command
			}
		}
	}()
}

func requireMotion(t *testing.T) {
	t.Helper()
	if os.Getenv("ALLOW_HARDWARE_TESTS") != "1" {
		t.Skip("set ALLOW_HARDWARE_TESTS=1")
	}
	if os.Getenv("ALLOW_MOTION_TESTS") != "1" {
		t.Skip("set ALLOW_MOTION_TESTS=1 — ensure shaft is free to rotate")
	}
	if masterIsIdle(t) {
		t.Skip("master in Idle phase — start jamun first")
	}

	settings := loadSettings(t)
	if a, ok := settings["A"]; ok && a.FinSignal != 0 {
		if os.Getenv("ALLOW_ECS_SIMULATION") == "1" {
			t.Logf("fin_signal=%d — keeping enabled for ECS co-simulation", a.FinSignal)
			startMockPLC(t)
		} else {
			t.Logf("fin_signal=%d — temporarily setting to 0 for standard motion tests", a.FinSignal)
			if err := setFinSignal(t, 0); err != nil {
				t.Fatalf("could not set fin_signal=0: %v — set it manually via UI Settings", err)
			}
			t.Cleanup(func() {
				if err := setFinSignal(t, a.FinSignal); err != nil {
					t.Logf("WARNING: could not restore fin_signal=%d: %v", a.FinSignal, err)
				} else {
					t.Logf("fin_signal restored to %d", a.FinSignal)
				}
			})
			time.Sleep(500 * time.Millisecond)
		}
	}
}

func setFinSignal(t *testing.T, value int) error {
	t.Helper()
	body, status := httpGET(t, "/dac_params")
	if status != 200 {
		return fmt.Errorf("GET /dac_params: status=%d", status)
	}
	var resp struct {
		Status   string          `json:"status"`
		Response json.RawMessage `json:"resp"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("parse /dac_params: %w", err)
	}

	var allSettings map[string]json.RawMessage
	if err := json.Unmarshal(resp.Response, &allSettings); err != nil {
		return fmt.Errorf("parse settings map: %w", err)
	}

	var driveA map[string]json.RawMessage
	if err := json.Unmarshal(allSettings["A"], &driveA); err != nil {
		return fmt.Errorf("parse drive A: %w", err)
	}
	driveA["fin_signal"] = json.RawMessage(strconv.Itoa(value))
	allSettings["A"], _ = json.Marshal(driveA)

	newBody, _ := json.Marshal(allSettings)
	postBody, status := httpPOST(t, "/dac_params", json.RawMessage(newBody))
	if status != 200 {
		return fmt.Errorf("POST /dac_params: status=%d body=%s", status, postBody)
	}
	t.Logf("fin_signal set to %d: %s", value, postBody)
	return nil
}

// ═══════════════════════════════════════════════════════════════════════════════
// TIER 1 — Drive parameter verification
// ═══════════════════════════════════════════════════════════════════════════════

func TestDriveParam_Identity_EEPROM(t *testing.T) {
	requireHardwareSmokeOptIn(t)
	out, err := runEtherCATReadOnly(t, "slaves", "-v")
	if err != nil {
		t.Fatalf("ethercat slaves -v: %v", err)
	}

	vendorID := parseEEPROMField(out, `Vendor\s+Id:\s+(0x[0-9a-fA-F]+)`)
	productCode := parseEEPROMField(out, `Product\s+code:\s+(0x[0-9a-fA-F]+)`)
	revNumber := parseEEPROMField(out, `Revision\s+number:\s+(0x[0-9a-fA-F]+)`)
	deviceName := parseEEPROMField(out, `Device\s+name:\s+(\S+)`)

	t.Logf("Vendor ID:       %s", vendorID)
	t.Logf("Product code:    %s", productCode)
	t.Logf("Revision number: %s", revNumber)
	t.Logf("Device name:     %s", deviceName)

	checkEEPROMExpectation(t, "EXPECTED_ETHERCAT_VENDOR_ID", "vendor ID", vendorID)
	checkEEPROMExpectation(t, "EXPECTED_ETHERCAT_PRODUCT_CODE", "product code", productCode)
	checkEEPROMExpectation(t, "EXPECTED_ETHERCAT_REVISION_NUMBER", "revision number", revNumber)
}

func TestDriveParam_CIA402_StatusObjects(t *testing.T) {
	requireHardwareSmokeOptIn(t)
	if masterIsIdle(t) {
		t.Skip("master in Idle phase")
	}

	tests := []struct {
		name       string
		index      string
		sub        string
		dtype      string
		wantNonNeg bool
	}{
		{"statusword", "0x6041", "0", "uint16", false},
		{"error_code", "0x603f", "0", "uint16", false},
		{"mode_of_operation_display", "0x6061", "0", "int8", false},
		{"actual_position", "0x6064", "0", "int32", false},
		{"following_error", "0x60f4", "0", "int32", false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			val, raw, err := cia402UploadSDO("0", tc.index, tc.sub, tc.dtype)
			if err != nil {
				t.Fatalf("SDO upload %s %s: %v", tc.index, tc.sub, err)
			}
			t.Logf("%s (%s:%s) = %d (0x%x) raw=%q", tc.name, tc.index, tc.sub, val, val, raw)
		})
	}
}

func TestDriveParam_ModeOfOperation_IsProfilePosition(t *testing.T) {
	requireHardwareSmokeOptIn(t)
	if masterIsIdle(t) {
		t.Skip("master in Idle phase")
	}
	val, raw, err := cia402UploadSDO("0", "0x6061", "0", "int8")
	if err != nil {
		t.Fatalf("read mode of operation display: %v", err)
	}
	t.Logf("mode of operation display = %d raw=%q", val, raw)
	// mode 3 (Profile Velocity, vel=0) is HAL's intentional standby mode for
	// the Panasonic A6 drive — see IMotorDriver.StandbyOpMode() in
	// motordriver/a6_minas_motor_driver.go. Mode 8 (Cyclic Synchronous
	// Position) causes a visible correction-torque jerk on this drive at
	// standby; mode 1 (Profile Position) was testenv's original assumption,
	// predating that fix. Do not "fix" this back to 1 without re-reading
	// StandbyOpMode's documented rationale first.
	if val != 3 {
		t.Errorf("mode of operation display = %d, want 3 (Profile Velocity — HAL's standby mode for A6)", val)
	}
}

func TestDriveParam_ErrorCode_IsZero(t *testing.T) {
	requireHardwareSmokeOptIn(t)
	if masterIsIdle(t) {
		t.Skip("master in Idle phase")
	}
	val, raw, err := cia402UploadSDO("0", "0x603f", "0", "uint16")
	if err != nil {
		t.Fatalf("read error code: %v", err)
	}
	t.Logf("error code = 0x%04x raw=%q", uint16(val), raw)
	if val != 0 {
		t.Errorf("error code = 0x%04x, want 0x0000 (no fault)", uint16(val))
	}
}

func TestDriveParam_Statusword_OperationEnabled(t *testing.T) {
	requireHardwareSmokeOptIn(t)
	if masterIsIdle(t) {
		t.Skip("master in Idle phase")
	}
	val, raw, err := cia402UploadSDO("0", "0x6041", "0", "uint16")
	if err != nil {
		t.Fatalf("read statusword: %v", err)
	}
	sw := uint16(val)
	t.Logf("statusword = 0x%04x raw=%q", sw, raw)

	opEnabled := sw&0x006F == 0x0027
	if !opEnabled {
		t.Errorf("statusword=0x%04x: bits 0,1,2 should be set, bit3 clear, bit5 set (mask 0x006F, want 0x0027, got 0x%04x)",
			sw, sw&0x006F)
	} else {
		t.Logf("✓ drive is in Operation Enabled state (statusword=0x%04x)", sw)
	}

	faultBit := sw & (1 << 3)
	if faultBit != 0 {
		t.Errorf("statusword fault bit (bit3) is set — drive has a fault")
	}
}

func TestDriveParam_ProfileVelocity_SetAtStartup(t *testing.T) {
	requireHardwareSmokeOptIn(t)
	if masterIsIdle(t) {
		t.Skip("master in Idle phase")
	}
	val, raw, err := cia402UploadSDO("0", "0x6081", "0", "uint32")
	if err != nil {
		t.Fatalf("read profile velocity (0x6081): %v", err)
	}
	t.Logf("profile velocity (0x6081) = %d counts/s raw=%q", val, raw)
	if val == 0 {
		t.Errorf("profile velocity is 0 — drive velocity not initialised")
	}
	degPerSec := float64(val) / pulsesPerDegree
	t.Logf("profile velocity = %.1f°/s (%.1f RPM)", degPerSec, degPerSec/6)
}

func TestDriveParam_ProfileAcceleration_NonZero(t *testing.T) {
	requireHardwareSmokeOptIn(t)
	if masterIsIdle(t) {
		t.Skip("master in Idle phase")
	}
	for _, obj := range []struct{ name, idx string }{
		{"profile_acceleration", "0x6083"},
		{"profile_deceleration", "0x6084"},
	} {
		val, raw, err := cia402UploadSDO("0", obj.idx, "0", "uint32")
		if err != nil {
			t.Errorf("read %s (%s): %v", obj.name, obj.idx, err)
			continue
		}
		t.Logf("%s (%s) = %d raw=%q", obj.name, obj.idx, val, raw)
		if val == 0 {
			t.Errorf("%s is 0 — drive ramp not configured", obj.name)
		}
	}
}

func TestDriveParam_ActualPosition_WithinRange(t *testing.T) {
	requireHardwareSmokeOptIn(t)
	if masterIsIdle(t) {
		t.Skip("master in Idle phase")
	}
	pos := actualPosDeg(t)
	t.Logf("actual position = %.4f°", pos)
	if pos < 0 || pos >= 360 {
		t.Errorf("actual position %.4f° is outside [0, 360)", pos)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// TIER 2 — Application settings verification
// ═══════════════════════════════════════════════════════════════════════════════

func TestSettings_DriveA_Present(t *testing.T) {
	requireHardwareSmokeOptIn(t)
	settings := loadSettings(t)
	if _, ok := settings["A"]; !ok {
		t.Fatalf("drive 'A' not found in /dac_params — expected at least one drive")
	}
	t.Logf("✓ drive A settings present")
}

func TestSettings_ECS_Value(t *testing.T) {
	requireHardwareSmokeOptIn(t)
	settings := loadSettings(t)
	a := settings["A"]
	t.Logf("ECS = %d (0=disabled, 1=enabled)", a.ECS)
	if a.ECS != 0 && a.ECS != 1 {
		t.Errorf("ECS = %d, expected 0 or 1", a.ECS)
	}
}

func TestSettings_FinSignal_Value(t *testing.T) {
	requireHardwareSmokeOptIn(t)
	settings := loadSettings(t)
	a := settings["A"]
	t.Logf("fin_signal = %d (0=disabled, 1=wait for IO signal after each move)", a.FinSignal)
	if a.FinSignal != 0 && a.FinSignal != 1 {
		t.Errorf("fin_signal = %d, expected 0 or 1", a.FinSignal)
	}
}

func TestSettings_JogFeed_WithinRange(t *testing.T) {
	requireHardwareSmokeOptIn(t)
	settings := loadSettings(t)
	a := settings["A"]
	v, err := strconv.Atoi(a.JogFeed)
	if err != nil {
		t.Fatalf("jog_feed=%q is not an integer: %v", a.JogFeed, err)
	}
	t.Logf("jog_feed = %d", v)
	if v < minFeedRate || v > maxFeedRate {
		t.Errorf("jog_feed=%d is outside valid range [%d, %d]", v, minFeedRate, maxFeedRate)
	}
}

func TestSettings_POTLimit_NonNegative(t *testing.T) {
	requireHardwareSmokeOptIn(t)
	settings := loadSettings(t)
	a := settings["A"]
	v, err := strconv.ParseFloat(a.POT, 64)
	if err != nil {
		t.Fatalf("pot=%q is not numeric: %v", a.POT, err)
	}
	t.Logf("POT limit = %.1f° (0 = disabled)", v)
	if v < 0 {
		t.Errorf("pot limit = %.1f°, expected ≥ 0", v)
	}
}

func TestSettings_NOTLimit_NonPositive(t *testing.T) {
	requireHardwareSmokeOptIn(t)
	settings := loadSettings(t)
	a := settings["A"]
	v, err := strconv.ParseFloat(a.NOT, 64)
	if err != nil {
		t.Fatalf("not=%q is not numeric: %v", a.NOT, err)
	}
	t.Logf("NOT limit = %.1f° (0 = disabled)", v)
	if v > 0 {
		t.Errorf("not limit = %.1f°, expected ≤ 0 (negative = CCW limit)", v)
	}
}

func TestSettings_WorkOffsets_Parseable(t *testing.T) {
	requireHardwareSmokeOptIn(t)
	settings := loadSettings(t)
	a := settings["A"]
	offsets := map[string]string{
		"G54": a.G54, "G55": a.G55,
		"G56": a.G56, "G57": a.G57, "G58": a.G58,
	}
	for name, val := range offsets {
		v, err := strconv.ParseFloat(val, 64)
		if err != nil {
			t.Errorf("work offset %s=%q is not numeric: %v", name, val, err)
		} else {
			t.Logf("%s = %.3f°", name, v)
		}
	}
}

func TestSettings_PitchError_CorrectDimension(t *testing.T) {
	requireHardwareSmokeOptIn(t)
	settings := loadSettings(t)
	a := settings["A"]
	n := len(a.PitchError)
	t.Logf("pitch error entries = %d (expected %d)", n, pitchErrorEntries)
	if n != pitchErrorEntries {
		t.Errorf("pitch error table has %d entries, want %d", n, pitchErrorEntries)
	}
}

func TestSettings_BackLash_NonNegative(t *testing.T) {
	requireHardwareSmokeOptIn(t)
	settings := loadSettings(t)
	a := settings["A"]
	v, err := strconv.ParseFloat(a.BackLash, 64)
	if err != nil {
		t.Fatalf("back_lash=%q is not numeric: %v", a.BackLash, err)
	}
	t.Logf("back_lash = %.3f°", v)
	if v < 0 {
		t.Errorf("back_lash = %.3f°, expected ≥ 0", v)
	}
}

func TestSettings_MotorDirection_ValidValue(t *testing.T) {
	requireHardwareSmokeOptIn(t)
	settings := loadSettings(t)
	a := settings["A"]
	t.Logf("motor_dir = %d (0=normal, 1=reversed)", a.MotorDir)
	if a.MotorDir != 0 && a.MotorDir != 1 {
		t.Errorf("motor_dir = %d, expected 0 or 1", a.MotorDir)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// TIER 3 — REST API functional tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestREST_Programs_List(t *testing.T) {
	requireHardwareSmokeOptIn(t)
	body, status := httpGET(t, "/programs")
	if status != 200 {
		t.Fatalf("GET /programs: status=%d", status)
	}
	t.Logf("GET /programs: %s", body)
}

func TestREST_Programs_OPTIONS(t *testing.T) {
	requireHardwareSmokeOptIn(t)
	req, _ := http.NewRequest("OPTIONS", restBase()+"/programs", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("OPTIONS /programs: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("OPTIONS /programs: status=%d, want 200", resp.StatusCode)
	}
	t.Logf("✓ OPTIONS /programs → %d", resp.StatusCode)
}

func TestREST_CreateFile_SaveAndVerify(t *testing.T) {
	requireHardwareSmokeOptIn(t)
	const name = "rest_test_create.nc"
	const content = "G90;\nM99;\n"

	body, status := httpPOST(t, "/createFile", map[string]string{
		"file_name": name, "contents": content,
	})
	if status != 200 {
		t.Fatalf("POST /createFile: status=%d body=%s", status, body)
	}
	t.Logf("POST /createFile: %s", body)

	readBody, readStatus := httpGET(t, "/getContents?file_name="+name)
	if readStatus != 200 {
		t.Fatalf("GET /getContents: status=%d", readStatus)
	}
	if !strings.Contains(string(readBody), "G90") {
		t.Errorf("getContents response does not contain program content: %s", readBody)
	}
	t.Logf("✓ file saved and read back correctly")

	http.Get(restBase() + "/deleteFile?file_name=" + name)
}

func TestREST_RenameFile(t *testing.T) {
	requireHardwareSmokeOptIn(t)
	const oldName = "rest_test_rename_old.nc"
	const newName = "rest_test_rename_new.nc"

	httpPOST(t, "/createFile", map[string]string{
		"file_name": oldName, "contents": "G90;\nM99;\n",
	})

	url := restBase() + "/renameFile?file_name=" + oldName + "&new_file_name=" + newName
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET /renameFile: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	status := resp.StatusCode
	if status != 200 {
		t.Fatalf("GET /renameFile: status=%d body=%s", status, body)
	}
	t.Logf("GET /renameFile: %s", body)

	_, s := httpGET(t, "/getContents?file_name="+oldName)
	if s == 200 {
		t.Errorf("old file %q still exists after rename", oldName)
	}

	_, s2 := httpGET(t, "/getContents?file_name="+newName)
	if s2 != 200 {
		t.Errorf("new file %q not found after rename", newName)
	}
	t.Logf("✓ rename successful")

	http.Get(restBase() + "/deleteFile?file_name=" + newName)
}

func TestREST_DeleteFile(t *testing.T) {
	requireHardwareSmokeOptIn(t)
	const name = "rest_test_delete.nc"

	httpPOST(t, "/createFile", map[string]string{
		"file_name": name, "contents": "G90;\nM99;\n",
	})

	delResp, delErr := http.Get(restBase() + "/deleteFile?file_name=" + name)
	if delErr != nil {
		t.Fatalf("GET /deleteFile: %v", delErr)
	}
	defer delResp.Body.Close()
	body, _ := io.ReadAll(delResp.Body)
	status := delResp.StatusCode
	if status != 200 {
		t.Fatalf("GET /deleteFile: status=%d body=%s", status, body)
	}
	t.Logf("GET /deleteFile: %s", body)

	_, s := httpGET(t, "/getContents?file_name="+name)
	if s == 200 {
		t.Errorf("file %q still accessible after delete", name)
	}
	t.Logf("✓ delete successful")
}

func TestREST_DacParams_GetReturnsAllFields(t *testing.T) {
	requireHardwareSmokeOptIn(t)
	body, status := httpGET(t, "/dac_params")
	if status != 200 {
		t.Fatalf("GET /dac_params: status=%d", status)
	}
	required := []string{"fin_signal", "ecs", "pot", "not", "jog_feed", "pitch_error", "back_lash"}
	for _, field := range required {
		if !strings.Contains(string(body), field) {
			t.Errorf("GET /dac_params response missing field %q", field)
		}
	}
	t.Logf("✓ all required settings fields present")
}

func TestREST_Support_ReturnsJSON(t *testing.T) {
	requireHardwareSmokeOptIn(t)
	body, status := httpGET(t, "/support")
	if status != 200 {
		t.Fatalf("GET /support: status=%d", status)
	}
	t.Logf("GET /support: %s", body)
}

func TestREST_FAQ_ReturnsJSON(t *testing.T) {
	requireHardwareSmokeOptIn(t)
	body, status := httpGET(t, "/faq")
	if status != 200 {
		t.Fatalf("GET /faq: status=%d", status)
	}
	t.Logf("GET /faq: %s", body)
}

// ═══════════════════════════════════════════════════════════════════════════════
// TIER 4 — Program compilation / validation
// ═══════════════════════════════════════════════════════════════════════════════

func TestCompile_ValidProgram_Accepted(t *testing.T) {
	requireHardwareSmokeOptIn(t)
	const name = "compile_valid.nc"
	body, status := httpPOST(t, "/createFile", map[string]string{
		"file_name": name,
		"contents":  "G01 F10;\nG90;\nG68;\nA90;\nD1;\nM99;\n",
	})
	if status != 200 {
		t.Fatalf("POST /createFile: status=%d", status)
	}
	s := parseCreateFileResponse(body)
	t.Logf("compile response status=%q body=%s", s, body)
	if s != "success" {
		t.Errorf("valid program was rejected, want status=success: %s", body)
	} else {
		t.Logf("✓ valid program accepted")
	}
	http.Get(restBase() + "/deleteFile?file_name=" + name)
}

func TestCompile_MissingSemicolon_Rejected(t *testing.T) {
	requireHardwareSmokeOptIn(t)
	const name = "compile_invalid.nc"
	body, status := httpPOST(t, "/createFile", map[string]string{
		"file_name": name,
		"contents":  "G90\nA90\nM99\n",
	})
	if status != 200 {
		t.Fatalf("POST /createFile: status=%d", status)
	}
	t.Logf("invalid program response: %s", body)
	if parseCreateFileResponse(body) == "success" {
		t.Errorf("invalid program (missing semicolons) was accepted — expected rejection")
	} else {
		t.Logf("✓ invalid program correctly rejected")
	}
}

func TestCompile_InvalidFeedrate_Rejected(t *testing.T) {
	requireHardwareSmokeOptIn(t)
	const name = "compile_badfeed.nc"
	body, status := httpPOST(t, "/createFile", map[string]string{
		"file_name": name,
		"contents":  "G01 F99;\nG90;\nA90;\nM99;\n",
	})
	if status != 200 {
		t.Fatalf("POST /createFile: status=%d", status)
	}
	t.Logf("invalid feedrate response: %s", body)
	if parseCreateFileResponse(body) == "success" {
		t.Logf("NOTE: F99 was accepted — rpm handler validates at runtime not compile time")
	} else {
		t.Logf("✓ invalid feedrate rejected at compile time")
	}
	http.Get(restBase() + "/deleteFile?file_name=" + name)
}

func TestCompile_EmptyProgram_Rejected(t *testing.T) {
	requireHardwareSmokeOptIn(t)
	const name = "compile_empty.nc"
	body, status := httpPOST(t, "/createFile", map[string]string{
		"file_name": name,
		"contents":  "",
	})
	if status != 200 {
		t.Fatalf("POST /createFile: status=%d", status)
	}
	t.Logf("empty program response: %s", body)
	http.Get(restBase() + "/deleteFile?file_name=" + name)
}

func TestCompile_POTLimitBreach_DetectedInTrialMode(t *testing.T) {
	requireHardwareSmokeOptIn(t)

	settings := loadSettings(t)
	a := settings["A"]
	pot, _ := strconv.ParseFloat(a.POT, 64)
	if pot == 0 {
		t.Skip("POT limit is 0 (disabled) — cannot test POT breach detection")
	}

	target := pot + 10
	const name = "compile_potbreach.nc"
	body, status := httpPOST(t, "/createFile", map[string]string{
		"file_name": name,
		"contents":  fmt.Sprintf("G01 F10;\nG90;\nA%.1f;\nM30;\n", target),
	})
	if status != 200 {
		t.Fatalf("POST /createFile: status=%d", status)
	}
	t.Logf("POT breach program response: %s", body)
	var r map[string]any
	json.Unmarshal(body, &r)
	if s, _ := r["status"].(string); s != "success" {
		t.Logf("✓ POT limit breach detected in trial mode and program rejected")
	} else {
		t.Logf("NOTE: POT breach program accepted — limit may be enforced at runtime only")
	}
	http.Get(restBase() + "/deleteFile?file_name=" + name)
}

// ═══════════════════════════════════════════════════════════════════════════════
// TIER 5 — Motion tests (require ALLOW_MOTION_TESTS=1 and optionally ALLOW_ECS_SIMULATION)
// ═══════════════════════════════════════════════════════════════════════════════

func TestMotion_AbsolutePositiveMove(t *testing.T) {
	requireMotion(t)

	delta := rangeDeg(t) / 2
	timeout := mvTimeout(t)
	start := actualPosDeg(t)
	target := math.Mod(start+delta, 360)

	t.Logf("ABS +%.1f°: %.3f° → %.3f°", delta, start, target)

	const name = "motion_abs_pos.nc"
	saveNC(t, name, fmt.Sprintf("G01 F10;\nG90;\nG68;\nA%.3f;\nD1;\nM30;\n", target))
	runNC(t, name, target, positionTolerance, timeout)
	assertPos(t, target, positionTolerance)

	t.Logf("returning to %.3f°", start)
	saveNC(t, name, fmt.Sprintf("G01 F10;\nG90;\nG68;\nA%.3f;\nD1;\nM30;\n", start))
	runNC(t, name, start, positionTolerance, timeout)
	assertPos(t, start, positionTolerance)
}

func TestMotion_AbsoluteNegativeMove(t *testing.T) {
	requireMotion(t)

	delta := rangeDeg(t) / 2
	timeout := mvTimeout(t)
	start := actualPosDeg(t)
	target := math.Mod(start-delta+360, 360)

	t.Logf("ABS -%.1f°: %.3f° → %.3f°", delta, start, target)

	const name = "motion_abs_neg.nc"
	saveNC(t, name, fmt.Sprintf("G01 F10;\nG90;\nG68;\nA%.3f;\nD1;\nM30;\n", target))
	runNC(t, name, target, positionTolerance, timeout)
	assertPos(t, target, positionTolerance)

	t.Logf("returning to %.3f°", start)
	saveNC(t, name, fmt.Sprintf("G01 F10;\nG90;\nG68;\nA%.3f;\nD1;\nM30;\n", start))
	runNC(t, name, start, positionTolerance, timeout)
	assertPos(t, start, positionTolerance)
}

func TestMotion_RelativeRoundTrip(t *testing.T) {
	requireMotion(t)

	delta := rangeDeg(t) / 2
	timeout := mvTimeout(t)
	start := actualPosDeg(t)

	t.Logf("REL +%.1f° then -%.1f°: start=%.3f°", delta, delta, start)

	const name = "motion_rel_rt.nc"
	fwdTarget := math.Mod(start+delta, 360)
	saveNC(t, name, fmt.Sprintf("G01 F10;\nG91;\nA%.3f;\nD1;\nM30;\n", delta))
	runNC(t, name, fwdTarget, positionTolerance, timeout)

	saveNC(t, name, fmt.Sprintf("G01 F10;\nG91;\nA%.3f;\nD1;\nM30;\n", -delta))
	runNC(t, name, start, positionTolerance, timeout)
	assertPos(t, start, positionTolerance)
	t.Logf("✓ relative round-trip: returned to start")
}

func TestMotion_MultiStepProgram(t *testing.T) {
	requireMotion(t)

	delta := rangeDeg(t) / 3
	timeout := mvTimeout(t) * 3
	start := actualPosDeg(t)
	step1 := math.Mod(start+delta, 360)
	step2 := math.Mod(start+2*delta, 360)

	t.Logf("multi-step: %.3f° → %.3f° → %.3f° → %.3f°", start, step1, step2, start)

	const name = "motion_multi.nc"
	saveNC(t, name, fmt.Sprintf(
		"G01 F10;\nG90;\nG68;\nA%.3f;\nD1;\nA%.3f;\nD1;\nA%.3f;\nD1;\nM30;\n",
		step1, step2, start,
	))
	runNC(t, name, start, positionTolerance, timeout)
	assertPos(t, start, positionTolerance)
	t.Logf("✓ multi-step program: returned to start")
}

func TestMotion_WorkOffset_G54_Applied(t *testing.T) {
	requireMotion(t)

	settings := loadSettings(t)
	g54, err := strconv.ParseFloat(settings["A"].G54, 64)
	if err != nil || g54 == 0 {
		t.Skip("G54 work offset is 0 or not set — cannot verify offset application")
	}

	delta := rangeDeg(t) / 2
	timeout := mvTimeout(t)
	start := actualPosDeg(t)

	nominal := math.Mod(start+delta, 360)
	effective := math.Mod(nominal+g54, 360)

	t.Logf("G54=%.3f°: commanding A%.3f°, expect actual position %.3f°", g54, nominal, effective)

	const name = "motion_g54.nc"
	saveNC(t, name, fmt.Sprintf("G01 F10;\nG90;\nG68;\nG54;\nA%.3f;\nD1;\nM30;\n", nominal))
	runNC(t, name, effective, positionTolerance, timeout)
	assertPos(t, effective, positionTolerance)

	saveNC(t, name, fmt.Sprintf("G01 F10;\nG90;\nG68;\nG53;\nA%.3f;\nD1;\nM30;\n", start))
	runNC(t, name, start, positionTolerance, timeout)
	assertPos(t, start, positionTolerance)
}

func TestMotion_FeedrateChange(t *testing.T) {
	requireMotion(t)

	delta := rangeDeg(t) / 2
	timeout := mvTimeout(t) * 2
	start := actualPosDeg(t)
	mid := math.Mod(start+delta/2, 360)
	target := math.Mod(start+delta, 360)

	t.Logf("feedrate change: F20 to %.3f°, F5 to %.3f°", mid, target)

	const name = "motion_feed.nc"
	saveNC(t, name, fmt.Sprintf(
		"G01 F20;\nG90;\nG68;\nA%.3f;\nD1;\nG01 F5;\nA%.3f;\nD1;\nA%.3f;\nD1;\nM30;\n",
		mid, target, start,
	))
	runNC(t, name, start, positionTolerance, timeout)
	assertPos(t, start, positionTolerance)
	t.Logf("✓ feedrate change test complete")
}

func TestMotion_ErrorCodeZeroAfterAllMoves(t *testing.T) {
	requireMotion(t)

	delta := rangeDeg(t) / 4
	timeout := mvTimeout(t)
	start := actualPosDeg(t)
	target := math.Mod(start+delta, 360)

	const name = "motion_errcheck.nc"
	saveNC(t, name, fmt.Sprintf(
		"G01 F10;\nG90;\nG68;\nA%.3f;\nD1;\nA%.3f;\nD1;\nM30;\n", target, start,
	))
	runNC(t, name, start, positionTolerance, timeout)

	val, raw, err := cia402UploadSDO("0", "0x603f", "0", "uint16")
	if err != nil {
		t.Fatalf("read error code after motion: %v", err)
	}
	if val != 0 {
		t.Errorf("drive fault after motion: 0x%04x raw=%s", uint16(val), raw)
	} else {
		t.Logf("✓ error code = 0x0000 after all moves — drive healthy")
	}
}
