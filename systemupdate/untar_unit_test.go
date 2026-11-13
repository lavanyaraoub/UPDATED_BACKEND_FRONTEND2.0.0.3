//go:build unit

package systemupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

// buildTarGz constructs an in-memory gzip-compressed tar archive from the
// given entries, for feeding directly into Untar without touching any real
// files or network resources.
type tarEntry struct {
	name    string
	content string
	isDir   bool
}

func buildTarGz(t *testing.T, entries []tarEntry) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	for _, e := range entries {
		if e.isDir {
			hdr := &tar.Header{
				Name:     e.name,
				Typeflag: tar.TypeDir,
				Mode:     0755,
			}
			if err := tw.WriteHeader(hdr); err != nil {
				t.Fatalf("write dir header: %v", err)
			}
			continue
		}
		hdr := &tar.Header{
			Name: e.name,
			Mode: 0644,
			Size: int64(len(e.content)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header: %v", err)
		}
		if _, err := tw.Write([]byte(e.content)); err != nil {
			t.Fatalf("write content: %v", err)
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	return &buf
}

func TestUntar_SimpleFile_ExtractsCorrectly(t *testing.T) {
	dest := t.TempDir()
	archive := buildTarGz(t, []tarEntry{
		{name: "hello.txt", content: "hello world"},
	})

	if err := Untar(archive, dest); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dest, "hello.txt"))
	if err != nil {
		t.Fatalf("expected extracted file: %v", err)
	}
	if string(data) != "hello world" {
		t.Errorf("content = %q, want %q", string(data), "hello world")
	}
}

func TestUntar_NestedDirectories_CreatesStructure(t *testing.T) {
	dest := t.TempDir()
	archive := buildTarGz(t, []tarEntry{
		{name: "configs/", isDir: true},
		{name: "configs/device.yml", content: "vendor: test"},
		{name: "scripts/", isDir: true},
		{name: "scripts/run.sh", content: "#!/bin/sh"},
	})

	if err := Untar(archive, dest); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, path := range []string{"configs/device.yml", "scripts/run.sh"} {
		if _, err := os.Stat(filepath.Join(dest, path)); err != nil {
			t.Errorf("expected %s to exist: %v", path, err)
		}
	}
}

func TestUntar_MultipleFiles_AllExtracted(t *testing.T) {
	dest := t.TempDir()
	archive := buildTarGz(t, []tarEntry{
		{name: "a.txt", content: "aaa"},
		{name: "b.txt", content: "bbb"},
		{name: "c.txt", content: "ccc"},
	})

	if err := Untar(archive, dest); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for name, want := range map[string]string{"a.txt": "aaa", "b.txt": "bbb", "c.txt": "ccc"} {
		data, err := os.ReadFile(filepath.Join(dest, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if string(data) != want {
			t.Errorf("%s content = %q, want %q", name, string(data), want)
		}
	}
}

func TestUntar_PathTraversal_Rejected(t *testing.T) {
	dest := t.TempDir()
	archive := buildTarGz(t, []tarEntry{
		{name: "../../../etc/passwd", content: "malicious"},
	})

	if err := Untar(archive, dest); err == nil {
		t.Fatal("expected an error for a path-traversal entry, got nil")
	}
}

func TestUntar_AbsolutePath_Rejected(t *testing.T) {
	dest := t.TempDir()
	archive := buildTarGz(t, []tarEntry{
		{name: "/etc/passwd", content: "malicious"},
	})

	if err := Untar(archive, dest); err == nil {
		t.Fatal("expected an error for an absolute-path entry, got nil")
	}
}

func TestUntar_NotGzip_ReturnsError(t *testing.T) {
	dest := t.TempDir()
	notGzip := bytes.NewBufferString("this is not a gzip stream")

	if err := Untar(notGzip, dest); err == nil {
		t.Fatal("expected an error for non-gzip input, got nil")
	}
}

func TestUntar_EmptyArchive_Succeeds(t *testing.T) {
	dest := t.TempDir()
	archive := buildTarGz(t, nil)

	if err := Untar(archive, dest); err != nil {
		t.Fatalf("unexpected error for empty archive: %v", err)
	}
}

func TestValidRelPath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"file.txt", true},
		{"dir/file.txt", true},
		{"", false},
		{"/absolute/path", false},
		{"has\\backslash", false},
		{"../traversal", false},
		{"dir/../traversal", false},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			if got := validRelPath(c.path); got != c.want {
				t.Errorf("validRelPath(%q) = %v, want %v", c.path, got, c.want)
			}
		})
	}
}

func TestValidRelativeDir(t *testing.T) {
	cases := []struct {
		dir  string
		want bool
	}{
		{"configs", true},
		{"configs/sub", true},
		{"/absolute", false},
		{"has\\backslash", false},
		{"..", false},
		{"../escape", false},
		{"configs/../..", false},
	}
	for _, c := range cases {
		t.Run(c.dir, func(t *testing.T) {
			if got := validRelativeDir(c.dir); got != c.want {
				t.Errorf("validRelativeDir(%q) = %v, want %v", c.dir, got, c.want)
			}
		})
	}
}
