//go:build integration

package restapi

// Tests for hotspot.go.
//
// createHotspot, killHotspot, and configureWifi's success paths all call
// into EtherCAT/hotspot, which shells out to `sudo scripts/hotspot.sh` /
// `sudo scripts/wifi.sh` — real commands that would reconfigure the test
// machine's network state. None of that is exercised here. What's covered
// is the safe surface: CORS/OPTIONS handling, and configureWifi's JSON
// decode-failure path, which returns before ever reaching hotspot.ConfigureWifi.

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ─── createHotspot ────────────────────────────────────────────────────────

func TestCreateHotspot_OptionsReturns200WithoutCallingHotspot(t *testing.T) {
	req := httptest.NewRequest(http.MethodOptions, "/hotspot/create", nil)
	w := httptest.NewRecorder()
	createHotspot(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("OPTIONS status = %d, want 200", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Errorf("OPTIONS body should be empty, got %q", w.Body.String())
	}
}

func TestCreateHotspot_GetIsIgnored(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/hotspot/create", nil)
	w := httptest.NewRecorder()
	createHotspot(w, req)
	// Handler only branches on OPTIONS/POST — GET falls through untouched.
	if w.Body.Len() != 0 {
		t.Errorf("GET body should be empty (ignored), got %q", w.Body.String())
	}
}

// ─── killHotspot ──────────────────────────────────────────────────────────

func TestKillHotspot_OptionsReturns200WithoutCallingHotspot(t *testing.T) {
	req := httptest.NewRequest(http.MethodOptions, "/hotspot/kill", nil)
	w := httptest.NewRecorder()
	killHotspot(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("OPTIONS status = %d, want 200", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Errorf("OPTIONS body should be empty, got %q", w.Body.String())
	}
}

func TestKillHotspot_GetIsIgnored(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/hotspot/kill", nil)
	w := httptest.NewRecorder()
	killHotspot(w, req)
	if w.Body.Len() != 0 {
		t.Errorf("GET body should be empty (ignored), got %q", w.Body.String())
	}
}

// ─── configureWifi ────────────────────────────────────────────────────────

func TestConfigureWifi_OptionsReturns200WithoutCallingHotspot(t *testing.T) {
	req := httptest.NewRequest(http.MethodOptions, "/hotspot/wifi", nil)
	w := httptest.NewRecorder()
	configureWifi(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("OPTIONS status = %d, want 200", w.Code)
	}
}

func TestConfigureWifi_GetIsIgnored(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/hotspot/wifi", nil)
	w := httptest.NewRecorder()
	configureWifi(w, req)
	if w.Body.Len() != 0 {
		t.Errorf("GET body should be empty (ignored), got %q", w.Body.String())
	}
}

func TestConfigureWifi_PostMalformedJSON_ReturnsErrorWithoutCallingHotspot(t *testing.T) {
	// Malformed body fails json.Decode before ever reaching
	// hotspot.ConfigureWifi (which would run a real sudo script) — this
	// exercises the decode-error branch safely.
	req := httptest.NewRequest(http.MethodPost, "/hotspot/wifi", bytes.NewReader([]byte("not json")))
	w := httptest.NewRecorder()
	configureWifi(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("malformed JSON: status = %d, want %d", w.Code, http.StatusNotFound)
	}
	if w.Body.Len() == 0 {
		t.Error("malformed JSON: expected an error body, got empty")
	}
}

func TestConfigureWifi_PostEmptyBody_ReturnsErrorWithoutCallingHotspot(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/hotspot/wifi", bytes.NewReader(nil))
	w := httptest.NewRecorder()
	configureWifi(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("empty body: status = %d, want %d", w.Code, http.StatusNotFound)
	}
}
