//go:build integration

package restapi

// Tests for tunnel.go.
//
// Both handlers' POST success paths shell out to scripts/tunnel.sh via
// EtherCAT/tunnel: startHTTPTunnel starts a real ngrok tunnel and blocks for
// up to 60 seconds waiting for a public URL, and stopTunnel invokes the same
// script to kill it. Neither is safe or fast to exercise in a test. Only the
// OPTIONS/method-guard routing — which returns before any of that runs — is
// tested here.

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// ─── startHTTPTunnel ──────────────────────────────────────────────────────

func TestStartHTTPTunnel_OptionsReturns200WithoutStartingTunnel(t *testing.T) {
	req := httptest.NewRequest(http.MethodOptions, "/tunnel/start", nil)
	w := httptest.NewRecorder()
	startHTTPTunnel(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("OPTIONS status = %d, want 200", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Errorf("OPTIONS body should be empty, got %q", w.Body.String())
	}
}

func TestStartHTTPTunnel_GetReturnsMethodNotAllowedWithoutStartingTunnel(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/tunnel/start", nil)
	w := httptest.NewRecorder()
	startHTTPTunnel(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

// ─── stopTunnel ───────────────────────────────────────────────────────────

func TestStopTunnel_OptionsReturns200WithoutStoppingTunnel(t *testing.T) {
	req := httptest.NewRequest(http.MethodOptions, "/tunnel/stop", nil)
	w := httptest.NewRecorder()
	stopTunnel(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("OPTIONS status = %d, want 200", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Errorf("OPTIONS body should be empty, got %q", w.Body.String())
	}
}

func TestStopTunnel_GetReturnsMethodNotAllowedWithoutStoppingTunnel(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/tunnel/stop", nil)
	w := httptest.NewRecorder()
	stopTunnel(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}
