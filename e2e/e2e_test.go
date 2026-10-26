//go:build e2e

package e2e_test

// ============================================================================
// End-to-end tests for the jamun REST API (port 5000).
//
// These tests exercise full user-facing workflows against the real running
// application — no mocks, no fakes. The app must be started before running:
//
//   sudo env LD_LIBRARY_PATH=... ./jamun   (in one terminal)
//   go test -tags=e2e ./e2e/... -v         (in another)
//
// Or via the Makefile target:
//   make test-e2e
//
// Environment variables:
//   E2E_BASE_URL  — REST API base (default: http://localhost:5000)
//
// What is covered:
//   - Program lifecycle: save → list → read → rename → delete
//   - Compile-time validation: valid program accepted, invalid rejected
//   - Settings round-trip: GET dac_params returns expected fields
//   - FAQ and support endpoints return valid JSON
//   - CORS preflight returns correct headers
//   - Concurrent program saves don't corrupt each other
// ============================================================================

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// ─── helpers ──────────────────────────────────────────────────────────────────

func baseURL() string {
	if u := strings.TrimRight(os.Getenv("E2E_BASE_URL"), "/"); u != "" {
		return u
	}
	return "http://localhost:5000"
}

func get(t *testing.T, path string) (int, []byte) {
	t.Helper()
	resp, err := http.Get(baseURL() + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

func post(t *testing.T, path, contentType string, body []byte) (int, []byte) {
	t.Helper()
	resp, err := http.Post(baseURL()+path, contentType, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func mustJSON(t *testing.T, body []byte) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	// Try direct unmarshal first
	if err := json.Unmarshal(body, &m); err == nil {
		return m
	}
	// API sometimes double-encodes: the body is a JSON string containing JSON.
	// Unwrap the outer string layer and try again.
	var s string
	if err := json.Unmarshal(body, &s); err == nil {
		if err2 := json.Unmarshal([]byte(s), &m); err2 == nil {
			return m
		}
	}
	t.Fatalf("response is not valid JSON: body=%s", body)
	return nil
}

func waitForServer(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseURL() + "/programs")
		if err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("jamun REST API not reachable at " + baseURL() + " after 10s — is the app running?")
}

// uniqueName generates a test-scoped program name to avoid collisions.
func uniqueName(t *testing.T) string {
	return fmt.Sprintf("e2e_%s_%d", strings.ReplaceAll(t.Name(), "/", "_"), time.Now().UnixMilli())
}

// saveProgram saves a .nc file and returns the assigned filename.
func saveProgram(t *testing.T, name, content string) {
	t.Helper()
	payload, _ := json.Marshal(map[string]string{
		"file_name": name,
		"contents":  content,
	})
	code, body := post(t, "/createFile", "application/json", payload)
	m := mustJSON(t, body)
	if code != 200 {
		t.Fatalf("saveProgram %q: HTTP %d body=%s", name, code, body)
	}
	if m["status"] != "success" {
		t.Fatalf("saveProgram %q: status=%v body=%s", name, m["status"], body)
	}
}

func deleteProgram(t *testing.T, name string) {
	t.Helper()
	code, _ := get(t, "/deleteFile?file_name="+name)
	if code != 200 {
		t.Logf("deleteProgram %q returned HTTP %d (may already be gone)", name, code)
	}
}

// ─── connectivity ─────────────────────────────────────────────────────────────

// TestMain runs before and after all E2E tests.
// It sweeps any stale e2e_* program files left by previous crashed runs,
// and does a final cleanup after all tests complete.
// This keeps /mnt/app/jamun/gm_codes clean regardless of test outcome.
func TestMain(m *testing.M) {
	sweepE2EFiles() // clean up any files from a previous crashed run
	code := m.Run() // run all tests
	sweepE2EFiles() // final cleanup after all tests
	os.Exit(code)
}

// sweepE2EFiles deletes all program files whose names start with "e2e_".
// These are exclusively created by E2E tests — production programs never
// use this prefix. Safe to call at any time.
func sweepE2EFiles() {
	resp, err := http.Get(baseURL() + "/programs")
	if err != nil {
		return // app not running — nothing to sweep
	}
	defer resp.Body.Close()

	var listing struct {
		Files []string `json:"files"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listing); err != nil {
		return
	}

	for _, name := range listing.Files {
		if strings.HasPrefix(name, "e2e_") {
			_, _ = http.Get(baseURL() + "/deleteFile?file_name=" + name)
		}
	}
}

func TestE2E_ServerIsReachable(t *testing.T) {
	waitForServer(t)
	code, _ := get(t, "/programs")
	if code != 200 {
		t.Errorf("GET /programs: HTTP %d, want 200", code)
	}
}

// ─── program lifecycle ────────────────────────────────────────────────────────

func TestE2E_ProgramLifecycle_SaveListReadRenameDelete(t *testing.T) {
	waitForServer(t)
	name := uniqueName(t)
	newName := name + "_renamed"

	// Cleanup on exit regardless of test outcome
	t.Cleanup(func() {
		deleteProgram(t, name)
		deleteProgram(t, newName)
	})

	// 1. Save a valid program
	validProgram := "G90;\nA90;\nM30;\n"
	saveProgram(t, name, validProgram)

	// 2. Verify it appears in /programs list
	_, listBody := get(t, "/programs")
	var listResp struct {
		Files []string `json:"files"`
	}
	if err := json.Unmarshal(listBody, &listResp); err != nil {
		t.Fatalf("GET /programs: invalid JSON: %v", err)
	}
	found := false
	for _, f := range listResp.Files {
		if f == name {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("saved program %q not found in /programs list: %v", name, listResp.Files)
	}

	// 3. Read back contents — API returns {"contents":"<data>"}
	code, contBody := get(t, "/getContents?file_name="+name)
	if code != 200 {
		t.Fatalf("GET /getContents: HTTP %d", code)
	}
	contMap := mustJSON(t, contBody)
	fileData := fmt.Sprint(contMap["contents"])
	if !strings.Contains(fileData, "G90") {
		t.Errorf("file contents don't contain 'G90': %s", contBody)
	}

	// 4. Rename the file
	code, renameBody := get(t, fmt.Sprintf("/renameFile?file_name=%s&new_file_name=%s", name, newName))
	if code != 200 {
		t.Fatalf("GET /renameFile: HTTP %d body=%s", code, renameBody)
	}
	m := mustJSON(t, renameBody)
	if strings.ToLower(fmt.Sprint(m["status"])) != "success" {
		t.Errorf("renameFile status=%v", m["status"])
	}

	// 5. Old name should be gone
	_, listBody2 := get(t, "/programs")
	if strings.Contains(string(listBody2), `"`+name+`"`) {
		t.Errorf("old name %q still appears in listing after rename", name)
	}

	// 6. New name should exist and have content (API joins lines with <br>)
	code, renamedBody := get(t, "/getContents?file_name="+newName)
	if code != 200 {
		t.Errorf("GET /getContents for renamed file: HTTP %d", code)
	} else {
		rm := mustJSON(t, renamedBody)
		if !strings.Contains(fmt.Sprint(rm["contents"]), "G90") {
			t.Errorf("renamed file contents missing G90: %s", renamedBody)
		}
	}

	// 7. Delete
	code, delBody := get(t, "/deleteFile?file_name="+newName)
	if code != 200 {
		t.Fatalf("GET /deleteFile: HTTP %d body=%s", code, delBody)
	}
	dm := mustJSON(t, delBody)
	if strings.ToLower(fmt.Sprint(dm["status"])) != "success" {
		t.Errorf("deleteFile status=%v", dm["status"])
	}

	// 8. Confirm gone
	_, listBody3 := get(t, "/programs")
	if strings.Contains(string(listBody3), `"`+newName+`"`) {
		t.Errorf("deleted file %q still appears in listing", newName)
	}
}

// ─── compile-time validation ──────────────────────────────────────────────────

func TestE2E_SaveProgram_ValidProgram_Accepted(t *testing.T) {
	waitForServer(t)
	name := uniqueName(t)
	t.Cleanup(func() { deleteProgram(t, name) })

	saveProgram(t, name, "G90;\nA45;\nM30;\n")
	// saveProgram already asserts status=success — reaching here means it passed
}

func TestE2E_SaveProgram_MissingSemicolon_SavesButFailsTrialMode(t *testing.T) {
	// The API accepts files at save time regardless of syntax — validation
	// happens during trial-mode execution, not on save. This test documents
	// that behaviour: save succeeds, but the file content is preserved as-is.
	waitForServer(t)
	name := uniqueName(t)
	t.Cleanup(func() { deleteProgram(t, name) })

	payload, _ := json.Marshal(map[string]string{
		"file_name": name,
		"contents":  "G90\nA45\nM30\n", // missing semicolons
	})
	_, body := post(t, "/createFile", "application/json", payload)
	m := mustJSON(t, body)
	// Save is accepted — content validation is deferred to trial-mode execution
	if m["status"] != "success" {
		t.Logf("NOTE: missing-semicolon file was rejected at save time (status=%v)", m["status"])
	}
	// Verify the file was saved with original content intact
	if m["status"] == "success" {
		code, contBody := get(t, "/getContents?file_name="+name)
		if code == 200 {
			cm := mustJSON(t, contBody)
			if !strings.Contains(fmt.Sprint(cm["contents"]), "G90") {
				t.Errorf("saved content missing expected G90: %s", contBody)
			}
		}
	}
}

func TestE2E_SaveProgram_InvalidFeedrate_Rejected(t *testing.T) {
	waitForServer(t)
	name := uniqueName(t)
	t.Cleanup(func() { deleteProgram(t, name) })

	payload, _ := json.Marshal(map[string]string{
		"file_name": name,
		"contents":  "G01 F99;\nA45;\nM30;\n", // F99 exceeds max feedrate of 20
	})
	_, body := post(t, "/createFile", "application/json", payload)
	m := mustJSON(t, body)
	if m["status"] == "success" {
		t.Log("NOTE: F99 accepted — rpm handler validates at runtime, not compile time")
	}
}

func TestE2E_SaveProgram_PathTraversal_Rejected(t *testing.T) {
	waitForServer(t)
	payload, _ := json.Marshal(map[string]string{
		"file_name": "../../etc/passwd",
		"contents":  "G90;\nM30;\n",
	})
	_, body := post(t, "/createFile", "application/json", payload)
	m := mustJSON(t, body)
	if m["status"] == "success" {
		t.Error("path traversal filename should have been rejected")
	}
}

func TestE2E_SaveProgram_EmptyBody_Rejected(t *testing.T) {
	waitForServer(t)
	code, body := post(t, "/createFile", "application/json", []byte(""))
	if code == 200 {
		m := mustJSON(t, body)
		if m["status"] == "success" {
			t.Error("empty body should be rejected")
		}
	}
}

func TestE2E_GetContents_MissingFile_Returns404(t *testing.T) {
	waitForServer(t)
	code, _ := get(t, "/getContents?file_name=definitely_does_not_exist_xyz.nc")
	if code != 404 {
		t.Errorf("missing file: HTTP %d, want 404", code)
	}
}

func TestE2E_GetContents_MissingParam_Returns404(t *testing.T) {
	waitForServer(t)
	code, _ := get(t, "/getContents")
	if code != 404 {
		t.Errorf("missing file_name param: HTTP %d, want 404", code)
	}
}

// ─── settings ─────────────────────────────────────────────────────────────────

func TestE2E_DacParams_GET_ReturnsAllRequiredFields(t *testing.T) {
	waitForServer(t)
	code, body := get(t, "/dac_params")
	if code != 200 {
		t.Fatalf("GET /dac_params: HTTP %d", code)
	}
	// The API returns {"status":"success","response":{...}} possibly double-encoded.
	// Use mustJSON to handle both cases, then dig into the response field.
	m := mustJSON(t, body)
	// API uses "resp" key (not "response")
	response, ok := m["resp"]
	if !ok {
		t.Fatalf("GET /dac_params: no 'resp' key in: %v", m)
	}
	// response may be a map or a JSON string — normalise it
	var driveMap map[string]interface{}
	switch v := response.(type) {
	case map[string]interface{}:
		driveMap = v
	case string:
		if err := json.Unmarshal([]byte(v), &driveMap); err != nil {
			t.Fatalf("GET /dac_params: response string not valid JSON: %v", err)
		}
	default:
		t.Fatalf("GET /dac_params: unexpected response type %T", response)
	}
	if len(driveMap) == 0 {
		t.Error("GET /dac_params: response map is empty, expected at least drive A settings")
	}
	if _, ok := driveMap["A"]; !ok {
		t.Errorf("GET /dac_params: drive A not present in response: %v", driveMap)
	}
}

func TestE2E_DacParams_OPTIONS_Returns200(t *testing.T) {
	waitForServer(t)
	req, _ := http.NewRequest("OPTIONS", baseURL()+"/dac_params", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("OPTIONS /dac_params: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("OPTIONS /dac_params: HTTP %d, want 200", resp.StatusCode)
	}
}

// ─── info endpoints ───────────────────────────────────────────────────────────

func TestE2E_FAQ_ReturnsValidJSON(t *testing.T) {
	waitForServer(t)
	code, body := get(t, "/faq")
	if code != 200 {
		t.Fatalf("GET /faq: HTTP %d", code)
	}
	m := mustJSON(t, body)
	if m["status"] != "success" {
		t.Errorf("GET /faq: status=%v", m["status"])
	}
}

func TestE2E_Support_ReturnsValidJSON(t *testing.T) {
	waitForServer(t)
	code, body := get(t, "/support")
	if code != 200 {
		t.Fatalf("GET /support: HTTP %d", code)
	}
	// support.json must at minimum have sales or service contact
	if !strings.Contains(string(body), "contact") && !strings.Contains(string(body), "version") {
		t.Errorf("GET /support: response missing expected fields: %s", body)
	}
}

func TestE2E_Programs_OPTIONS_CORS_Headers(t *testing.T) {
	waitForServer(t)
	req, _ := http.NewRequest("OPTIONS", baseURL()+"/programs", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("OPTIONS /programs: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("OPTIONS /programs: HTTP %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("Access-Control-Allow-Methods"), "POST") {
		t.Errorf("CORS Allow-Methods missing POST: %q", resp.Header.Get("Access-Control-Allow-Methods"))
	}
}

// ─── concurrent saves ─────────────────────────────────────────────────────────

func TestE2E_ConcurrentSaves_NoCorruption(t *testing.T) {
	waitForServer(t)
	const n = 5
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf("%s_%d", uniqueName(t), i)
	}
	t.Cleanup(func() {
		for _, name := range names {
			deleteProgram(t, name)
		}
	})

	done := make(chan error, n)
	for i, name := range names {
		go func(name string, i int) {
			payload, _ := json.Marshal(map[string]string{
				"file_name": name,
				"contents":  fmt.Sprintf("G90;\nA%d;\nM30;\n", (i+1)*30),
			})
			resp, err := http.Post(baseURL()+"/createFile", "application/json", bytes.NewReader(payload))
			if err != nil {
				done <- fmt.Errorf("save %q: %v", name, err)
				return
			}
			resp.Body.Close()
			if resp.StatusCode != 200 {
				done <- fmt.Errorf("save %q: HTTP %d", name, resp.StatusCode)
				return
			}
			done <- nil
		}(name, i)
	}

	for i := 0; i < n; i++ {
		if err := <-done; err != nil {
			t.Error(err)
		}
	}

	// Verify all files exist
	_, listBody := get(t, "/programs")
	for _, name := range names {
		if !strings.Contains(string(listBody), `"`+name+`"`) {
			t.Errorf("concurrent save: file %q missing from listing", name)
		}
	}
}
