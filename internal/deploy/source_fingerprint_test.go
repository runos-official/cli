package deploy

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeSrc(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustFingerprint(t *testing.T, dir string) string {
	t.Helper()
	fp, err := SourceFingerprint(dir)
	if err != nil {
		t.Fatalf("SourceFingerprint: %v", err)
	}
	if fp == "" {
		t.Fatal("SourceFingerprint returned empty")
	}
	return fp
}

// FCR 168: apps diff needs a content fingerprint of the deploy archive that
// changes exactly when the uploaded bytes would change.
func TestSourceFingerprint(t *testing.T) {
	base := func(t *testing.T) string {
		dir := t.TempDir()
		writeSrc(t, dir, "app.py", "print('v1')\n")
		writeSrc(t, dir, "Dockerfile", "FROM scratch\n")
		return dir
	}

	t.Run("same content gives the same fingerprint", func(t *testing.T) {
		a, b := base(t), base(t)
		if mustFingerprint(t, a) != mustFingerprint(t, b) {
			t.Error("identical trees must fingerprint the same")
		}
	})

	t.Run("mtime alone does not change it", func(t *testing.T) {
		dir := base(t)
		before := mustFingerprint(t, dir)
		later := time.Now().Add(2 * time.Hour)
		if err := os.Chtimes(filepath.Join(dir, "app.py"), later, later); err != nil {
			t.Fatal(err)
		}
		if mustFingerprint(t, dir) != before {
			t.Error("touching a file must not change the fingerprint")
		}
	})

	t.Run("edited content changes it", func(t *testing.T) {
		dir := base(t)
		before := mustFingerprint(t, dir)
		writeSrc(t, dir, "app.py", "print('v2')\n")
		if mustFingerprint(t, dir) == before {
			t.Error("an edited source file must change the fingerprint")
		}
	})

	t.Run("new file changes it", func(t *testing.T) {
		dir := base(t)
		before := mustFingerprint(t, dir)
		writeSrc(t, dir, "lib/extra.py", "x = 1\n")
		if mustFingerprint(t, dir) == before {
			t.Error("a new source file must change the fingerprint")
		}
	})

	t.Run("excluded files do not change it", func(t *testing.T) {
		dir := base(t)
		before := mustFingerprint(t, dir)
		writeSrc(t, dir, ".runos.c1.a1.source-version", "some-id\n")
		writeSrc(t, dir, "runos.yaml", "app: x\n")
		if mustFingerprint(t, dir) != before {
			t.Error("files the archive excludes must not change the fingerprint")
		}
	})
}
