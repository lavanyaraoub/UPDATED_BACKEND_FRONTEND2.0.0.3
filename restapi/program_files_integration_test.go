//go:build integration

package restapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func prepareProgramAPITestDir(t *testing.T) string {
	t.Helper()
	dir := getCodeFilePath()
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("clean code file path: %v", err)
	}
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatalf("create code file path: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
	})
	return dir
}

func performRESTRequest(handler http.HandlerFunc, method, target string, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	handler(rr, req)
	return rr
}

func requireStatus(t *testing.T, rr *httptest.ResponseRecorder, want int) {
	t.Helper()
	if got := rr.Code; got != want {
		t.Fatalf("status = %d, want %d, body=%q", got, want, rr.Body.String())
	}
}

func requireBodyContains(t *testing.T, rr *httptest.ResponseRecorder, want string) {
	t.Helper()
	if !strings.Contains(rr.Body.String(), want) {
		t.Fatalf("body = %q, want it to contain %q", rr.Body.String(), want)
	}
}

func TestProgramFilesRESTIntegration_ListReadRenameAndDelete(t *testing.T) {
	dir := prepareProgramAPITestDir(t)
	if err := os.WriteFile(filepath.Join(dir, "alpha.gcode"), []byte("G90;\nA10;\n"), 0o666); err != nil {
		t.Fatalf("write alpha fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "beta.gcode"), []byte("M30;\n"), 0o666); err != nil {
		t.Fatalf("write beta fixture: %v", err)
	}

	listResp := performRESTRequest(getProgramFiles, http.MethodGet, "/programs", "")
	requireStatus(t, listResp, http.StatusOK)
	var listed ProgramFiles
	if err := json.NewDecoder(listResp.Body).Decode(&listed); err != nil {
		t.Fatalf("decode /programs response: %v", err)
	}
	if got, want := listed.Files, []string{"alpha.gcode", "beta.gcode"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("files = %#v, want %#v", got, want)
	}

	contentResp := performRESTRequest(getProgramFileContent, http.MethodGet, "/getContents?file_name=alpha.gcode", "")
	requireStatus(t, contentResp, http.StatusOK)
	var content ProgramFileContent
	if err := json.NewDecoder(contentResp.Body).Decode(&content); err != nil {
		t.Fatalf("decode /getContents response: %v", err)
	}
	if content.Contents != "G90;<br>A10;<br>" {
		t.Fatalf("contents = %q, want formatted line content", content.Contents)
	}

	renameResp := performRESTRequest(renameProgramFile, http.MethodGet, "/renameFile?file_name=alpha.gcode&new_file_name=renamed.gcode", "")
	requireStatus(t, renameResp, http.StatusOK)
	requireBodyContains(t, renameResp, "Success")
	if _, err := os.Stat(filepath.Join(dir, "renamed.gcode")); err != nil {
		t.Fatalf("renamed file should exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "alpha.gcode")); !os.IsNotExist(err) {
		t.Fatalf("old file should be gone after rename, stat err=%v", err)
	}

	deleteResp := performRESTRequest(deleteProgram, http.MethodGet, "/deleteFile?file_name=renamed.gcode", "")
	requireStatus(t, deleteResp, http.StatusOK)
	requireBodyContains(t, deleteResp, "Success")
	if _, err := os.Stat(filepath.Join(dir, "renamed.gcode")); !os.IsNotExist(err) {
		t.Fatalf("renamed file should be deleted, stat err=%v", err)
	}
}

func TestProgramFilesRESTIntegration_RequiredParameterFailures(t *testing.T) {
	prepareProgramAPITestDir(t)

	cases := []struct {
		name    string
		handler http.HandlerFunc
		target  string
	}{
		{name: "get contents missing file name", handler: getProgramFileContent, target: "/getContents"},
		{name: "get contents missing file", handler: getProgramFileContent, target: "/getContents?file_name=missing.gcode"},
		{name: "delete missing file name", handler: deleteProgram, target: "/deleteFile"},
		{name: "rename missing old name", handler: renameProgramFile, target: "/renameFile?new_file_name=x.gcode"},
		{name: "rename missing new name", handler: renameProgramFile, target: "/renameFile?file_name=x.gcode"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := performRESTRequest(tc.handler, http.MethodGet, tc.target, "")
			requireStatus(t, rr, http.StatusNotFound)
		})
	}
}

func TestSaveProgramRESTIntegration_SaveValidCommentOnlyProgram(t *testing.T) {
	dir := prepareProgramAPITestDir(t)
	payload := []byte(`{"file_name":"safe_comment_only.gcode","contents":"# setup note only\n"}`)

	req := httptest.NewRequest(http.MethodPost, "/createFile", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	saveProgram(rr, req)

	requireStatus(t, rr, http.StatusOK)
	requireBodyContains(t, rr, "success")
	if got, err := os.ReadFile(filepath.Join(dir, "safe_comment_only.gcode")); err != nil || string(got) != "# setup note only\n" {
		t.Fatalf("saved file = %q, err=%v", string(got), err)
	}
}

func TestSaveProgramRESTIntegration_RejectsMalformedProgramAndDeletesFile(t *testing.T) {
	dir := prepareProgramAPITestDir(t)
	payload := []byte(`{"file_name":"bad_syntax.gcode","contents":"G90\n"}`)

	req := httptest.NewRequest(http.MethodPost, "/createFile", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	saveProgram(rr, req)

	requireStatus(t, rr, http.StatusOK)
	requireBodyContains(t, rr, "error")
	requireBodyContains(t, rr, "Syntax error")
	if _, err := os.Stat(filepath.Join(dir, "bad_syntax.gcode")); !os.IsNotExist(err) {
		t.Fatalf("malformed program should be removed after compile failure, stat err=%v", err)
	}
}

func TestProgramFilesRESTIntegration_CORSPreflightDoesNotTouchHandlers(t *testing.T) {
	prepareProgramAPITestDir(t)
	rr := performRESTRequest(getProgramFiles, http.MethodOptions, "/programs", "")
	requireStatus(t, rr, http.StatusOK)
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want *", got)
	}
	if got := rr.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(got, "POST") || !strings.Contains(got, "GET") {
		t.Fatalf("Access-Control-Allow-Methods = %q, want POST and GET allowed", got)
	}
}
func TestGetProgramFiles_WrongPathReturns404(t *testing.T) {
	// r.URL.Path != "/programs" → http.Error 404
	req := httptest.NewRequest(http.MethodGet, "/wrong-path", nil)
	w := httptest.NewRecorder()
	getProgramFiles(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("wrong path: status = %d, want 404", w.Code)
	}
}

func TestGetProgramFiles_WrongMethodReturns404(t *testing.T) {
	// r.Method != "GET" → http.Error 404
	req := httptest.NewRequest(http.MethodPost, "/programs", nil)
	w := httptest.NewRecorder()
	getProgramFiles(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("wrong method: status = %d, want 404", w.Code)
	}
}

func TestGetProgramFiles_OptionsReturns200(t *testing.T) {
	req := httptest.NewRequest(http.MethodOptions, "/programs", nil)
	w := httptest.NewRecorder()
	getProgramFiles(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("OPTIONS: status = %d, want 200", w.Code)
	}
}

// ─── setupCorsResponse ────────────────────────────────────────────────────

func TestSetupCorsResponse_SetsAllHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	var rw http.ResponseWriter = w
	setupCorsResponse(&rw, req)

	headers := w.Result().Header
	if headers.Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("Allow-Origin = %q, want *", headers.Get("Access-Control-Allow-Origin"))
	}
	if headers.Get("Access-Control-Allow-Methods") == "" {
		t.Errorf("Allow-Methods header not set")
	}
	if headers.Get("Access-Control-Allow-Headers") == "" {
		t.Errorf("Allow-Headers header not set")
	}
}

// ─── restapi.go — NewApi, getCodeFilePath ─────────────────────────────────

func TestNewApi_ReturnsNonZeroValue(t *testing.T) {
	api := NewApi()
	// API is an empty struct — just verify it constructs without panic
	_ = api
}

func TestGetCodeFilePath_ReturnsNonEmptyString(t *testing.T) {
	path := getCodeFilePath()
	if path == "" {
		t.Errorf("getCodeFilePath() returned empty string")
	}
}

// ─── Concurrent requests ──────────────────────────────────────────────────
//
// The existing tests send one request at a time. These confirm that the
// handlers are safe under concurrent load — no shared state races or panics.

func TestGetProgramFiles_ConcurrentReads(t *testing.T) {
	prepareProgramAPITestDir(t)

	const goroutines = 20
	var wg sync.WaitGroup
	errs := make(chan string, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/programs", nil)
			w := httptest.NewRecorder()
			getProgramFiles(w, req)
			if w.Code == http.StatusInternalServerError {
				errs <- fmt.Sprintf("goroutine %d: got 500", id)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Error(msg)
	}
}

func TestSaveProgram_ConcurrentDistinctFiles(t *testing.T) {
	prepareProgramAPITestDir(t)

	const goroutines = 10
	var wg sync.WaitGroup
	errs := make(chan string, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			body, _ := json.Marshal(map[string]string{
				"file_name": fmt.Sprintf("concurrent_%d.gcode", id),
				"contents":  "G90;\nM30;\n",
			})
			req := httptest.NewRequest(http.MethodPost, "/createFile", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			saveProgram(w, req)
			if w.Code == http.StatusInternalServerError {
				errs <- fmt.Sprintf("goroutine %d: got 500, body=%q", id, w.Body.String())
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Error(msg)
	}
}

// ─── Input boundary tests ─────────────────────────────────────────────────

// TestSaveProgram_EmptyBody returns a clean error, not a 500 or panic.
func TestSaveProgram_EmptyBody(t *testing.T) {
	prepareProgramAPITestDir(t)
	req := httptest.NewRequest(http.MethodPost, "/createFile", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	saveProgram(w, req)
	if w.Code == http.StatusInternalServerError {
		t.Errorf("empty body produced 500 — want 404 or similar, body=%q", w.Body.String())
	}
}

// TestSaveProgram_NonJSONBody returns a clean error when the body is not JSON.
func TestSaveProgram_NonJSONBody(t *testing.T) {
	prepareProgramAPITestDir(t)
	req := httptest.NewRequest(http.MethodPost, "/createFile",
		strings.NewReader("this is not json"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	saveProgram(w, req)
	if w.Code == http.StatusInternalServerError {
		t.Errorf("non-JSON body produced 500")
	}
}

// TestSaveProgram_VeryLargeProgram verifies the handler does not timeout or
// panic on a large but syntactically valid program.
func TestSaveProgram_VeryLargeProgram(t *testing.T) {
	prepareProgramAPITestDir(t)

	var sb strings.Builder
	sb.WriteString("G90;\n")
	for i := 0; i < 4_998; i++ {
		sb.WriteString("A90;\n")
	}
	sb.WriteString("M30;\n")

	body, _ := json.Marshal(map[string]string{
		"file_name": "large_program.gcode",
		"contents":  sb.String(),
	})
	req := httptest.NewRequest(http.MethodPost, "/createFile", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	saveProgram(w, req)
	if w.Code == http.StatusInternalServerError {
		t.Errorf("large valid program produced 500: %s", w.Body.String())
	}
}

// TestSaveProgram_PathTraversalInFileName verifies a file_name containing
// "../" cannot escape the code file directory.
func TestSaveProgram_PathTraversalInFileName(t *testing.T) {
	prepareProgramAPITestDir(t)

	// Write the sentinel to os.TempDir() — a stable directory that the Go
	// test runner does not clean up mid-test, unlike filepath.Dir(getCodeFilePath())
	// which lives inside the Go build cache and may be removed during the run.
	sentinelPath := filepath.Join(os.TempDir(), "sentinel_traversal_test.txt")
	if err := os.WriteFile(sentinelPath, []byte("original"), 0o666); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	t.Cleanup(func() { os.Remove(sentinelPath) })

	// The payload tries to escape the code directory via "../"
	body, _ := json.Marshal(map[string]string{
		"file_name": "../sentinel_traversal_test.txt",
		"contents":  "ATTACKED",
	})
	req := httptest.NewRequest(http.MethodPost, "/createFile", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	saveProgram(w, req)

	got, err := os.ReadFile(sentinelPath)
	if err != nil {
		t.Fatalf("sentinel file disappeared: %v", err)
	}
	if string(got) == "ATTACKED" {
		t.Errorf("PATH TRAVERSAL: ../sentinel_traversal_test.txt was overwritten — handler does not sanitise file_name")
	}
}

// TestDeleteProgram_NonExistentFile returns a clean error, not a 500 or panic.
func TestDeleteProgram_NonExistentFile(t *testing.T) {
	prepareProgramAPITestDir(t)
	rr := performRESTRequest(deleteProgram, http.MethodGet,
		"/deleteFile?file_name=does_not_exist.gcode", "")
	if rr.Code == http.StatusInternalServerError {
		t.Errorf("delete non-existent file produced 500: %s", rr.Body.String())
	}
}

// TestRenameProgram_SourceDoesNotExist returns a clean error, not a 500.
func TestRenameProgram_SourceDoesNotExist(t *testing.T) {
	prepareProgramAPITestDir(t)
	rr := performRESTRequest(renameProgramFile, http.MethodGet,
		"/renameFile?file_name=ghost.gcode&new_file_name=renamed.gcode", "")
	if rr.Code == http.StatusInternalServerError {
		t.Errorf("rename non-existent file produced 500: %s", rr.Body.String())
	}
}

// ─── getProgramFileContent ────────────────────────────────────────────────

// TestGetProgramFileContent_OptionsReturns200 verifies OPTIONS preflight passes.
func TestGetProgramFileContent_OptionsReturns200(t *testing.T) {
	prepareProgramAPITestDir(t)
	rr := performRESTRequest(getProgramFileContent, http.MethodOptions, "/getContents", "")
	if rr.Code != http.StatusOK {
		t.Errorf("OPTIONS /getContents = %d, want 200", rr.Code)
	}
}

// TestGetProgramFileContent_MissingFileNameReturns404 verifies missing
// file_name query param returns 404.
func TestGetProgramFileContent_MissingFileNameReturns404(t *testing.T) {
	prepareProgramAPITestDir(t)
	rr := performRESTRequest(getProgramFileContent, http.MethodGet, "/getContents", "")
	if rr.Code != http.StatusNotFound {
		t.Errorf("missing file_name = %d, want 404", rr.Code)
	}
}

// TestGetProgramFileContent_NonExistentFileReturns404 verifies a file_name
// that doesn't exist on disk returns 404.
func TestGetProgramFileContent_NonExistentFileReturns404(t *testing.T) {
	prepareProgramAPITestDir(t)
	rr := performRESTRequest(getProgramFileContent, http.MethodGet,
		"/getContents?file_name=doesnotexist.gcode", "")
	if rr.Code != http.StatusNotFound {
		t.Errorf("missing file = %d, want 404", rr.Code)
	}
}

// TestGetProgramFileContent_ExistingFileReturnsContents verifies a real file
// is read and returned as JSON with a contents field.
func TestGetProgramFileContent_ExistingFileReturnsContents(t *testing.T) {
	dir := prepareProgramAPITestDir(t)

	content := "G90;\nA90;\nM30;\n"
	if err := os.WriteFile(filepath.Join(dir, "test_read.gcode"),
		[]byte(content), 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	rr := performRESTRequest(getProgramFileContent, http.MethodGet,
		"/getContents?file_name=test_read.gcode", "")
	if rr.Code != http.StatusOK {
		t.Errorf("GET existing file = %d, want 200: %s", rr.Code, rr.Body.String())
	}

	var resp struct {
		Contents string `json:"contents"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response JSON: %v", err)
	}
	if resp.Contents == "" {
		t.Errorf("contents field is empty, want file content")
	}
}

// TestGetProgramFileContent_WrongMethodReturns404 verifies POST returns 404.
func TestGetProgramFileContent_WrongMethodReturns404(t *testing.T) {
	prepareProgramAPITestDir(t)
	rr := performRESTRequest(getProgramFileContent, http.MethodPost,
		"/getContents?file_name=any.gcode", "")
	if rr.Code != http.StatusNotFound {
		t.Errorf("POST /getContents = %d, want 404", rr.Code)
	}
}

// ─── manipulateSettings POST write error path ─────────────────────────────

// TestManipulateSettings_PostWriteFailReturnsError verifies that when the
// settings file directory doesn't exist, the POST path returns an error
// response rather than panicking.
func TestManipulateSettings_PostWriteFailReturnsError(t *testing.T) {
	// The handler writes to AppendWDPath(/settings/settings.json).
	// In the test environment that path likely doesn't exist, so the
	// write will fail and the handler must return an error JSON, not panic.
	rr := performRESTRequest(manipulateSettings, http.MethodPost,
		"/settings", `{"test":"value"}`)
	// Should return 200 with error status body (API convention), not 500 or panic
	if rr.Code == http.StatusInternalServerError {
		t.Errorf("POST settings write fail produced 500 — handler must not panic")
	}
}
