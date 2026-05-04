package apps

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeTarGz builds a small in-memory gzipped tarball from a list of
// entries. Each entry's body is empty when nil.
type tarEntry struct {
	name     string
	body     []byte
	typeflag byte
	linkname string
	mode     int64
}

func makeTarGz(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)
	for _, e := range entries {
		hdr := &tar.Header{
			Name:     e.name,
			Mode:     e.mode,
			Size:     int64(len(e.body)),
			Typeflag: e.typeflag,
			Linkname: e.linkname,
		}
		if hdr.Mode == 0 {
			hdr.Mode = 0644
		}
		if hdr.Typeflag == 0 {
			hdr.Typeflag = tar.TypeReg
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header %s: %v", e.name, err)
		}
		if len(e.body) > 0 {
			if _, err := tw.Write(e.body); err != nil {
				t.Fatalf("write body %s: %v", e.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tw close: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("gzw close: %v", err)
	}
	return buf.Bytes()
}

func TestExtractTarGz_WritesRegularFiles(t *testing.T) {
	dest := t.TempDir()
	archive := makeTarGz(t, []tarEntry{
		{name: "src/main.go", body: []byte("package main\n")},
		{name: "Dockerfile", body: []byte("FROM alpine\n")},
	})

	written, err := ExtractTarGz(bytes.NewReader(archive), dest, nil)
	if err != nil {
		t.Fatalf("ExtractTarGz: %v", err)
	}
	if written != 2 {
		t.Errorf("written = %d, want 2", written)
	}
	for _, p := range []string{"src/main.go", "Dockerfile"} {
		if _, err := os.Stat(filepath.Join(dest, p)); err != nil {
			t.Errorf("expected %s to exist: %v", p, err)
		}
	}
}

func TestExtractTarGz_RejectsZipSlipPath(t *testing.T) {
	dest := t.TempDir()
	archive := makeTarGz(t, []tarEntry{
		{name: "../escape.txt", body: []byte("malicious")},
	})

	_, err := ExtractTarGz(bytes.NewReader(archive), dest, nil)
	if err == nil {
		t.Fatal("expected error for path traversal entry")
	}
	if !strings.Contains(err.Error(), "escapes destination") {
		t.Errorf("error should mention escape, got: %v", err)
	}
	// Confirm nothing was written outside dest.
	if _, err := os.Stat(filepath.Join(filepath.Dir(dest), "escape.txt")); !os.IsNotExist(err) {
		t.Errorf("escape.txt should not exist: %v", err)
	}
}

func TestExtractTarGz_RejectsAbsolutePath(t *testing.T) {
	dest := t.TempDir()
	archive := makeTarGz(t, []tarEntry{
		{name: "/etc/evil", body: []byte("x")},
	})
	_, err := ExtractTarGz(bytes.NewReader(archive), dest, nil)
	if err == nil {
		t.Fatal("expected error for absolute path entry")
	}
}

func TestExtractTarGz_SkipsConfigFiles(t *testing.T) {
	dest := t.TempDir()
	// Pre-write the would-be-clobbered config so we can verify the
	// extractor leaves it alone.
	keepBody := []byte("local-config")
	if err := os.WriteFile(filepath.Join(dest, "runos.yaml"), keepBody, 0644); err != nil {
		t.Fatalf("seed runos.yaml: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dest, ".secret-files"), 0700); err != nil {
		t.Fatalf("seed secret dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dest, ".secret-files", "tls.key"), keepBody, 0600); err != nil {
		t.Fatalf("seed tls.key: %v", err)
	}

	archive := makeTarGz(t, []tarEntry{
		{name: "runos.yaml", body: []byte("archive-config")},
		{name: ".secret-files/tls.key", body: []byte("archive-secret")},
		{name: "src/main.go", body: []byte("package main\n")},
	})

	_, err := ExtractTarGz(bytes.NewReader(archive), dest, PulledCodeSkipPaths("k1", "ab12c"))
	if err != nil {
		t.Fatalf("ExtractTarGz: %v", err)
	}

	// Skipped: pulled config wins.
	got, _ := os.ReadFile(filepath.Join(dest, "runos.yaml"))
	if string(got) != "local-config" {
		t.Errorf("runos.yaml was overwritten, got %q", got)
	}
	got, _ = os.ReadFile(filepath.Join(dest, ".secret-files", "tls.key"))
	if string(got) != "local-config" {
		t.Errorf("secret file was overwritten, got %q", got)
	}
	// Source file outside the skip list extracted normally.
	got, _ = os.ReadFile(filepath.Join(dest, "src", "main.go"))
	if string(got) != "package main\n" {
		t.Errorf("src/main.go missing or wrong: %q", got)
	}
}

func TestExtractTarGz_RejectsSymlinkEscape(t *testing.T) {
	dest := t.TempDir()
	archive := makeTarGz(t, []tarEntry{
		{name: "evil-link", typeflag: tar.TypeSymlink, linkname: "../../../etc/passwd"},
	})
	_, err := ExtractTarGz(bytes.NewReader(archive), dest, nil)
	if err == nil {
		t.Fatal("expected error for escaping symlink")
	}
}

func TestExtractTarGz_RejectsAbsoluteSymlinkTarget(t *testing.T) {
	// filepath.Join silently strips the absolute-ness of its second arg, so
	// an absolute-target symlink can pass a naive containment check while
	// the symlink itself, once created, still resolves outside destDir.
	// The extractor must reject these outright.
	dest := t.TempDir()
	archive := makeTarGz(t, []tarEntry{
		{name: "evil-link", typeflag: tar.TypeSymlink, linkname: "/etc/passwd"},
	})
	_, err := ExtractTarGz(bytes.NewReader(archive), dest, nil)
	if err == nil {
		t.Fatal("expected error for absolute symlink target")
	}
	if !strings.Contains(err.Error(), "absolute target") {
		t.Errorf("error should mention absolute target, got: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dest, "evil-link")); !os.IsNotExist(err) {
		t.Errorf("evil-link should not exist: %v", err)
	}
}
