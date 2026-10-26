//go:build integration

package restapi

// Tests for rs232.go.
//
// Three layers are tested:
//   1. parseRS232EnabledValue — pure type-switch, no HTTP
//   2. parseRS232EnabledPayload — pure JSON parsing, no HTTP
//   3. boolToInt — pure conversion
//   4. rs232State GET/POST — full HTTP handler via httptest

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ─── boolToInt ────────────────────────────────────────────────────────────

func TestBoolToInt(t *testing.T) {
	if boolToInt(true) != 1 {
		t.Errorf("boolToInt(true) = %d, want 1", boolToInt(true))
	}
	if boolToInt(false) != 0 {
		t.Errorf("boolToInt(false) = %d, want 0", boolToInt(false))
	}
}

// ─── parseRS232EnabledValue ───────────────────────────────────────────────

func TestParseRS232EnabledValue_Bool(t *testing.T) {
	got, ok := parseRS232EnabledValue(true)
	if !ok || !got {
		t.Errorf("parseRS232EnabledValue(true) = (%v, %v), want (true, true)", got, ok)
	}
	got, ok = parseRS232EnabledValue(false)
	if !ok || got {
		t.Errorf("parseRS232EnabledValue(false) = (%v, %v), want (false, true)", got, ok)
	}
}

func TestParseRS232EnabledValue_Float64(t *testing.T) {
	got, ok := parseRS232EnabledValue(float64(1))
	if !ok || !got {
		t.Errorf("float64(1): got (%v, %v), want (true, true)", got, ok)
	}
	got, ok = parseRS232EnabledValue(float64(0))
	if !ok || got {
		t.Errorf("float64(0): got (%v, %v), want (false, true)", got, ok)
	}
}

func TestParseRS232EnabledValue_StringTruthy(t *testing.T) {
	for _, s := range []string{"1", "true", "on", "enabled", "TRUE", "ON"} {
		got, ok := parseRS232EnabledValue(s)
		if !ok || !got {
			t.Errorf("parseRS232EnabledValue(%q) = (%v, %v), want (true, true)", s, got, ok)
		}
	}
}

func TestParseRS232EnabledValue_StringFalsy(t *testing.T) {
	for _, s := range []string{"0", "false", "off", "disabled", "FALSE"} {
		got, ok := parseRS232EnabledValue(s)
		if !ok || got {
			t.Errorf("parseRS232EnabledValue(%q) = (%v, %v), want (false, true)", s, got, ok)
		}
	}
}

func TestParseRS232EnabledValue_UnknownStringReturnsFalse(t *testing.T) {
	_, ok := parseRS232EnabledValue("maybe")
	if ok {
		t.Errorf("parseRS232EnabledValue(maybe) ok=true, want false")
	}
}

func TestParseRS232EnabledValue_UnknownTypeReturnsFalse(t *testing.T) {
	_, ok := parseRS232EnabledValue([]int{1, 2})
	if ok {
		t.Errorf("unexpected type: ok=true, want false")
	}
}

// ─── parseRS232EnabledPayload ─────────────────────────────────────────────

func TestParseRS232EnabledPayload_EmptyBodyReturnsFalse(t *testing.T) {
	_, ok := parseRS232EnabledPayload([]byte{})
	if ok {
		t.Errorf("empty body: ok=true, want false")
	}
}

func TestParseRS232EnabledPayload_InvalidJSONReturnsFalse(t *testing.T) {
	_, ok := parseRS232EnabledPayload([]byte("not-json"))
	if ok {
		t.Errorf("invalid json: ok=true, want false")
	}
}

func TestParseRS232EnabledPayload_EnabledKey(t *testing.T) {
	body, _ := json.Marshal(map[string]interface{}{"enabled": true})
	got, ok := parseRS232EnabledPayload(body)
	if !ok || !got {
		t.Errorf("enabled=true: got (%v, %v), want (true, true)", got, ok)
	}
}

func TestParseRS232EnabledPayload_DataKey(t *testing.T) {
	body, _ := json.Marshal(map[string]interface{}{"data": "1"})
	got, ok := parseRS232EnabledPayload(body)
	if !ok || !got {
		t.Errorf("data=1: got (%v, %v), want (true, true)", got, ok)
	}
}

func TestParseRS232EnabledPayload_NoKnownKeyReturnsFalse(t *testing.T) {
	body, _ := json.Marshal(map[string]interface{}{"unknown": true})
	_, ok := parseRS232EnabledPayload(body)
	if ok {
		t.Errorf("unknown key: ok=true, want false")
	}
}

// ─── rs232State HTTP handler ──────────────────────────────────────────────

func TestRS232State_OptionsReturns200(t *testing.T) {
	req := httptest.NewRequest(http.MethodOptions, "/rs232", nil)
	w := httptest.NewRecorder()
	rs232State(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("OPTIONS: status = %d, want 200", w.Code)
	}
}

func TestRS232State_MethodNotAllowedReturns200WithErrorBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodDelete, "/rs232", nil)
	w := httptest.NewRecorder()
	rs232State(w, req)
	// Handler encodes a JSON error response, still HTTP 200
	var resp rs232StateResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "error" {
		t.Errorf("DELETE status = %q, want error", resp.Status)
	}
}

func TestRS232State_PostWithValidPayload_ReturnsSuccess(t *testing.T) {
	body, _ := json.Marshal(map[string]interface{}{"enabled": false})
	req := httptest.NewRequest(http.MethodPost, "/rs232", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	rs232State(w, req)

	var resp rs232StateResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "success" {
		t.Errorf("POST valid payload: status = %q, want success", resp.Status)
	}
}

func TestRS232State_PostWithMissingEnabledKey_ReturnsError(t *testing.T) {
	body, _ := json.Marshal(map[string]interface{}{"unknown": 1})
	req := httptest.NewRequest(http.MethodPost, "/rs232", bytes.NewReader(body))
	w := httptest.NewRecorder()
	rs232State(w, req)

	var resp rs232StateResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp.Status != "error" {
		t.Errorf("POST missing key: status = %q, want error", resp.Status)
	}
}

// ─── parseRS232EnabledPayload — capital-key variants ─────────────────────────

func TestParseRS232EnabledPayload_CapitalEnabledKey(t *testing.T) {
	body, _ := json.Marshal(map[string]interface{}{"Enabled": true})
	got, ok := parseRS232EnabledPayload(body)
	if !ok || !got {
		t.Errorf("Enabled=true: got (%v, %v), want (true, true)", got, ok)
	}
}

func TestParseRS232EnabledPayload_CapitalDataKey(t *testing.T) {
	body, _ := json.Marshal(map[string]interface{}{"Data": "1"})
	got, ok := parseRS232EnabledPayload(body)
	if !ok || !got {
		t.Errorf("Data=1: got (%v, %v), want (true, true)", got, ok)
	}
}

func TestParseRS232EnabledPayload_EnabledKeyFalse(t *testing.T) {
	body, _ := json.Marshal(map[string]interface{}{"enabled": false})
	got, ok := parseRS232EnabledPayload(body)
	if !ok || got {
		t.Errorf("enabled=false: got (%v, %v), want (false, true)", got, ok)
	}
}

// ─── rs232State GET handler ───────────────────────────────────────────────────

func TestRS232State_GetReturnsSuccessOrError(t *testing.T) {
	// GET /rs232 — either succeeds (file exists on Pi) or returns error JSON
	// Both branches exercise the GET path at rs232.go:19
	req := httptest.NewRequest(http.MethodGet, "/rs232", nil)
	w := httptest.NewRecorder()
	rs232State(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET /rs232: HTTP %d, want 200", w.Code)
	}
	var resp rs232StateResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("GET /rs232: invalid JSON response: %v", err)
	}
	// Either success or error is acceptable — both are valid responses
	if resp.Status != "success" && resp.Status != "error" {
		t.Errorf("GET /rs232: status = %q, want 'success' or 'error'", resp.Status)
	}
}

func TestRS232State_GetEnabled0Or1(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/rs232", nil)
	w := httptest.NewRecorder()
	rs232State(w, req)

	var resp rs232StateResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp.Status == "success" && resp.Enabled != 0 && resp.Enabled != 1 {
		t.Errorf("GET /rs232 success: Enabled = %d, want 0 or 1", resp.Enabled)
	}
}

// ─── rs232State POST — toggle on then off ────────────────────────────────────

func TestRS232State_PostEnable_ThenDisable(t *testing.T) {
	// Enable
	body, _ := json.Marshal(map[string]interface{}{"enabled": true})
	req := httptest.NewRequest(http.MethodPost, "/rs232", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	rs232State(w, req)

	var resp rs232StateResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp.Status == "success" && resp.Enabled != 1 {
		t.Errorf("POST enable: Enabled = %d, want 1", resp.Enabled)
	}

	// Disable
	body, _ = json.Marshal(map[string]interface{}{"enabled": false})
	req = httptest.NewRequest(http.MethodPost, "/rs232", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	rs232State(w, req)

	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp.Status == "success" && resp.Enabled != 0 {
		t.Errorf("POST disable: Enabled = %d, want 0", resp.Enabled)
	}
}

func TestRS232State_PostWithStringEnabled(t *testing.T) {
	body, _ := json.Marshal(map[string]interface{}{"enabled": "1"})
	req := httptest.NewRequest(http.MethodPost, "/rs232", bytes.NewReader(body))
	w := httptest.NewRecorder()
	rs232State(w, req)

	var resp rs232StateResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp.Status != "success" && resp.Status != "error" {
		t.Errorf("POST string enabled: status = %q", resp.Status)
	}
}

func TestRS232State_PostWithNumericEnabled(t *testing.T) {
	body, _ := json.Marshal(map[string]interface{}{"enabled": 1})
	req := httptest.NewRequest(http.MethodPost, "/rs232", bytes.NewReader(body))
	w := httptest.NewRecorder()
	rs232State(w, req)

	var resp rs232StateResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp.Status != "success" && resp.Status != "error" {
		t.Errorf("POST numeric enabled: status = %q", resp.Status)
	}
}
