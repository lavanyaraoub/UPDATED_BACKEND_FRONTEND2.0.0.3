//go:build unit

package helper

// Tests for line_managment.go (ReadLastLine / WriteLastLine) and
// file_management.go (CopyFile / CopyDir).
//
// All tests use t.TempDir() — no files are left behind after a test run.

import (
	"os"
	"path/filepath"
	"testing"
)

// ─── ReadLastLine / WriteLastLine ─────────────────────────────────────────

func TestReadLastLine_FileNotExist_ReturnsZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.txt")
	got, err := ReadLastLine(path)
	if err != nil {
		t.Fatalf("missing file should not error, got: %v", err)
	}
	if got != 0 {
		t.Errorf("got %d, want 0 for missing file", got)
	}
}

func TestReadLastLine_EmptyFile_ReturnsZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.txt")
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadLastLine(path)
	if err != nil {
		t.Fatalf("empty file should not error, got: %v", err)
	}
	if got != 0 {
		t.Errorf("got %d, want 0 for empty file", got)
	}
}

func TestReadLastLine_ReturnsLastLinePlusOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "last.txt")
	if err := os.WriteFile(path, []byte("5"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadLastLine(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 6 { // last completed = 5, so resume from 6
		t.Errorf("got %d, want 6 (last line 5 + 1)", got)
	}
}

func TestReadLastLine_WhitespaceAndNewlines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ws.txt")
	if err := os.WriteFile(path, []byte("  10\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadLastLine(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 11 {
		t.Errorf("got %d, want 11", got)
	}
}

func TestReadLastLine_CorruptContent_ReturnsErr(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt.txt")
	if err := os.WriteFile(path, []byte("not-a-number"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ReadLastLine(path)
	if err == nil {
		t.Errorf("corrupt content should return error, got nil")
	}
}

func TestWriteLastLine_ThenReadBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "line.txt")
	if err := WriteLastLine(path, 42); err != nil {
		t.Fatalf("WriteLastLine error: %v", err)
	}
	got, err := ReadLastLine(path)
	if err != nil {
		t.Fatalf("ReadLastLine error: %v", err)
	}
	if got != 43 { // wrote 42, read back 42+1=43
		t.Errorf("got %d, want 43", got)
	}
}

func TestWriteLastLine_OverwritesPreviousValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "line.txt")
	_ = WriteLastLine(path, 10)
	_ = WriteLastLine(path, 20)
	got, err := ReadLastLine(path)
	if err != nil {
		t.Fatalf("ReadLastLine error: %v", err)
	}
	if got != 21 {
		t.Errorf("got %d, want 21 (last write was 20)", got)
	}
}

// ─── CopyFile ─────────────────────────────────────────────────────────────

func TestCopyFile_ContentMatches(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "dst.txt")
	content := []byte("G90;\nA90;\n")

	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CopyFile(src, dst); err != nil {
		t.Fatalf("CopyFile error: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("dst content = %q, want %q", got, content)
	}
}

func TestCopyFile_MissingSource_ReturnsErr(t *testing.T) {
	dir := t.TempDir()
	err := CopyFile(filepath.Join(dir, "no-such-file.txt"), filepath.Join(dir, "dst.txt"))
	if err == nil {
		t.Errorf("CopyFile with missing source should error, got nil")
	}
}

func TestCopyFile_PreservesPermissions(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "dst.txt")
	if err := os.WriteFile(src, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CopyFile(src, dst); err != nil {
		t.Fatalf("CopyFile error: %v", err)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode() != 0o600 {
		t.Errorf("dst mode = %o, want 600", info.Mode())
	}
}

// ─── CopyDir ──────────────────────────────────────────────────────────────

func TestCopyDir_CopiesFilesRecursively(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "dst")

	// Create src/a.txt and src/sub/b.txt
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("aaa"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(src, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "b.txt"), []byte("bbb"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := CopyDir(src, dst); err != nil {
		t.Fatalf("CopyDir error: %v", err)
	}

	for _, rel := range []string{"a.txt", filepath.Join("sub", "b.txt")} {
		data, err := os.ReadFile(filepath.Join(dst, rel))
		if err != nil {
			t.Errorf("file %q missing in dst: %v", rel, err)
			continue
		}
		_ = data
	}
}

func TestCopyDir_SourceNotDirectory_ReturnsErr(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := CopyDir(file, filepath.Join(dir, "dst"))
	if err == nil {
		t.Errorf("CopyDir with file as source should error, got nil")
	}
}

func TestCopyDir_MissingSource_ReturnsErr(t *testing.T) {
	err := CopyDir("/no/such/path", t.TempDir())
	if err == nil {
		t.Errorf("CopyDir with missing source should error, got nil")
	}
}
