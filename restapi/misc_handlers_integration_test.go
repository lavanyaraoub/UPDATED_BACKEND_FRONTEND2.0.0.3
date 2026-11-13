//go:build integration

package restapi

// Tests for the remaining HTTP handler stubs:
//   manipulateSettings (GET/POST)
//   readFaq (GET, OPTIONS)
//   readPwd (GET, OPTIONS)
//   getSupport (GET, OPTIONS)
//
// All use httptest so no real network is involved. The file-reading paths
// use AppendWDPath which resolves to the test binary's temp directory, so
// the file won't exist — we test the error-path behavior (graceful response)
// rather than the happy path (which needs a real config file on disk).

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ─── readFaq ──────────────────────────────────────────────────────────────

func TestReadFaq_OptionsReturns200(t *testing.T) {
	req := httptest.NewRequest(http.MethodOptions, "/faq", nil)
	w := httptest.NewRecorder()
	readFaq(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("OPTIONS status = %d, want 200", w.Code)
	}
}

func TestReadFaq_PostIsIgnored(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/faq", nil)
	w := httptest.NewRecorder()
	readFaq(w, req)
	// Non-GET non-OPTIONS is silently ignored — body is empty, status 200
	if w.Code != http.StatusOK {
		t.Errorf("POST status = %d, want 200 (ignored)", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Errorf("POST body should be empty, got %q", w.Body.String())
	}
}

func TestReadFaq_GetReturnsJSONWithStatus(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/faq", nil)
	w := httptest.NewRecorder()
	readFaq(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET status = %d, want 200", w.Code)
	}
	var resp SettingsResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("GET decode: %v", err)
	}
	if resp.Status != "success" {
		t.Errorf("GET status field = %q, want success", resp.Status)
	}
}

// ─── readPwd ──────────────────────────────────────────────────────────────

func TestReadPwd_OptionsReturns200(t *testing.T) {
	req := httptest.NewRequest(http.MethodOptions, "/pwd", nil)
	w := httptest.NewRecorder()
	readPwd(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("OPTIONS status = %d, want 200", w.Code)
	}
}

func TestReadPwd_GetReturnsJSONWithStatus(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/pwd", nil)
	w := httptest.NewRecorder()
	readPwd(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET status = %d, want 200", w.Code)
	}
	var resp SettingsResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("GET decode: %v", err)
	}
	if resp.Status != "success" {
		t.Errorf("GET status field = %q, want success", resp.Status)
	}
}

func TestReadPwd_PostIsIgnored(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/pwd", nil)
	w := httptest.NewRecorder()
	readPwd(w, req)
	if w.Body.Len() != 0 {
		t.Errorf("POST body should be empty, got %q", w.Body.String())
	}
}

// ─── getSupport ───────────────────────────────────────────────────────────

func TestGetSupport_OptionsReturns200(t *testing.T) {
	req := httptest.NewRequest(http.MethodOptions, "/support", nil)
	w := httptest.NewRecorder()
	getSupport(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("OPTIONS status = %d, want 200", w.Code)
	}
}

func TestGetSupport_GetReturnsJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/support", nil)
	w := httptest.NewRecorder()
	getSupport(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET status = %d, want 200", w.Code)
	}
	// Even if the support.json file is missing, handler encodes empty struct
	var resp Support
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("GET decode: %v", err)
	}
	// Version is always set from constants regardless of file presence
	if resp.Version == "" {
		t.Errorf("Version is empty — constants.SystemVersion may not be set")
	}
}

// ─── manipulateSettings ───────────────────────────────────────────────────

func TestManipulateSettings_OptionsReturns200(t *testing.T) {
	req := httptest.NewRequest(http.MethodOptions, "/settings", nil)
	w := httptest.NewRecorder()
	manipulateSettings(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("OPTIONS status = %d, want 200", w.Code)
	}
}

func TestManipulateSettings_GetReturnsJSONResponse(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/settings", nil)
	w := httptest.NewRecorder()
	manipulateSettings(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET status = %d, want 200", w.Code)
	}
	var resp SettingsResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("GET decode: %v", err)
	}
	if resp.Status != "success" {
		t.Errorf("GET status field = %q, want success", resp.Status)
	}
}

func TestManipulateSettings_PostWithValidJSONReturnsResponse(t *testing.T) {
	// POST writes to AppendWDPath("/settings/settings.json") which won't
	// exist in the test binary's temp dir — handler should return an
	// error JSON (not panic).
	body := []byte(`{"test": true}`)
	req := httptest.NewRequest(http.MethodPost, "/settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	manipulateSettings(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("POST status = %d, want 200", w.Code)
	}
	// Body should be a JSON string — either success or error
	body = w.Body.Bytes()
	if len(body) == 0 {
		t.Errorf("POST response body is empty")
	}
}

// ─── getFaultHistory / clearFaultHistory ──────────────────────────────────
//
// getFaultHistory is fully safe: motor.GetFaultHistory() only reads an
// in-memory ring buffer, populated by LoadFaultHistory() at InitMaster time
// — never called in this test binary, so the ring is simply empty. No disk
// access happens in isolation.
//
// clearFaultHistory's success path is NOT exercised here: motor.ClearFaultHistory()
// calls os.Remove() on a hardcoded real path (/var/tmp/ethercat_fault_history.json)
// that could hold genuine fault history from real operation on this machine.
// Only the OPTIONS and method-guard (non-POST) paths are tested — both
// return before ever reaching ClearFaultHistory().

func TestGetFaultHistory_OptionsReturns200(t *testing.T) {
	req := httptest.NewRequest(http.MethodOptions, "/fault/history", nil)
	w := httptest.NewRecorder()
	getFaultHistory(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("OPTIONS status = %d, want 200", w.Code)
	}
}

func TestGetFaultHistory_Get_ReturnsJSONArray(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/fault/history", nil)
	w := httptest.NewRecorder()
	getFaultHistory(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET status = %d, want 200", w.Code)
	}
	var entries []map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &entries); err != nil {
		t.Errorf("GET response is not valid JSON array: %v (body: %s)", err, w.Body.String())
	}
}

func TestClearFaultHistory_OptionsReturns200WithoutClearing(t *testing.T) {
	req := httptest.NewRequest(http.MethodOptions, "/fault/clear", nil)
	w := httptest.NewRecorder()
	clearFaultHistory(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("OPTIONS status = %d, want 200", w.Code)
	}
}

func TestClearFaultHistory_GetReturnsMethodNotAllowedWithoutClearing(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/fault/clear", nil)
	w := httptest.NewRecorder()
	clearFaultHistory(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}
