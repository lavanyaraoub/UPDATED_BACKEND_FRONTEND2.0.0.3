//go:build integration

package restapi

// Tests for rollback.go.
//
// rollbackCheck is fully safe to test: systemupdate.GetLatestBackup() only
// reads a directory and returns an error if no backup exists. Whether that
// happens depends on the machine running the test — a fresh checkout has no
// backups, a real deployed unit may have real ones on disk — so the test
// below accepts either outcome and checks the response shape is correct for
// whichever one is actually true.
//
// rollbackApply's POST path is NOT exercised here under any circumstance:
// it launches `go systemupdate.PerformRollback(true)`, and that function's
// own doc comment states it calls os.Exit(2) on success — which would kill
// the test binary outright. Only its OPTIONS/method-guard routing is tested.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ─── rollbackCheck ────────────────────────────────────────────────────────

func TestRollbackCheck_OptionsReturns200(t *testing.T) {
	req := httptest.NewRequest(http.MethodOptions, "/rollback/check", nil)
	w := httptest.NewRecorder()
	rollbackCheck(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("OPTIONS status = %d, want 200", w.Code)
	}
}

func TestRollbackCheck_PostReturnsMethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/rollback/check", nil)
	w := httptest.NewRecorder()
	rollbackCheck(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestRollbackCheck_Get_ReturnsConsistentResponseEitherWay(t *testing.T) {
	// Whether a backup exists depends on the machine this runs on — a fresh
	// test environment has none, but a real deployed unit may have real
	// backups on disk. Both are valid outcomes; what matters is the handler
	// responds consistently (right status code + right JSON shape) for
	// whichever state is actually true, without panicking either way.
	req := httptest.NewRequest(http.MethodGet, "/rollback/check", nil)
	w := httptest.NewRecorder()
	rollbackCheck(w, req)

	switch w.Code {
	case http.StatusNotFound:
		var resp TextResponse
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("GET (no backup) decode: %v", err)
		}
		if resp.Status != "error" {
			t.Errorf("GET (no backup) status field = %q, want error", resp.Status)
		}
		if resp.Response == "" {
			t.Error("GET (no backup) expected a non-empty error message")
		}
	case http.StatusOK:
		var resp struct {
			Status    string `json:"status"`
			Version   string `json:"version"`
			Timestamp string `json:"timestamp"`
		}
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("GET (backup found) decode: %v", err)
		}
		if resp.Status != "success" {
			t.Errorf("GET (backup found) status field = %q, want success", resp.Status)
		}
		if resp.Timestamp == "" {
			t.Error("GET (backup found) expected a non-empty timestamp")
		}
	default:
		t.Fatalf("GET status = %d, want %d or %d", w.Code, http.StatusNotFound, http.StatusOK)
	}
}

// ─── rollbackApply — routing guards only, POST path never exercised ───────

func TestRollbackApply_OptionsReturns200WithoutTriggeringRollback(t *testing.T) {
	req := httptest.NewRequest(http.MethodOptions, "/rollback/apply", nil)
	w := httptest.NewRecorder()
	rollbackApply(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("OPTIONS status = %d, want 200", w.Code)
	}
}

func TestRollbackApply_GetReturnsMethodNotAllowedWithoutTriggeringRollback(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/rollback/apply", nil)
	w := httptest.NewRecorder()
	rollbackApply(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}
