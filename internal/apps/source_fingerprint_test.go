package apps

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSourceFingerprintSidecar_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := WriteSourceFingerprint(dir, testSidecarCID, testSidecarApp, "up-1", "fp-1"); err != nil {
		t.Fatalf("write: %v", err)
	}
	up, fp, err := ReadSourceFingerprint(dir, testSidecarCID, testSidecarApp)
	if err != nil || up != "up-1" || fp != "fp-1" {
		t.Fatalf("got (%q, %q, %v), want (up-1, fp-1, nil)", up, fp, err)
	}
	up, fp, err = ReadSourceFingerprint(t.TempDir(), testSidecarCID, testSidecarApp)
	if err != nil || up != "" || fp != "" {
		t.Fatalf("missing sidecar: got (%q, %q, %v), want empty", up, fp, err)
	}
}

// FCR 168: apps diff printed "code" as in sync and "No drift." for a local
// edit that was never uploaded. CompareLocalSource must say "changed".
func TestCompareLocalSource(t *testing.T) {
	setup := func(t *testing.T) (string, *CodeVersionStatus) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "app.py"), []byte("v1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		fp, err := localSourceFingerprint(dir, "")
		if err != nil {
			t.Fatal(err)
		}
		if err := WriteSourceFingerprint(dir, testSidecarCID, testSidecarApp, "up-1", fp); err != nil {
			t.Fatal(err)
		}
		return dir, &CodeVersionStatus{Recorded: "up-1", RecordedFound: true}
	}

	t.Run("unchanged source", func(t *testing.T) {
		dir, st := setup(t)
		CompareLocalSource(st, dir, "", testSidecarCID, testSidecarApp)
		if st.LocalSource != LocalSourceUnchanged || st.LocalChanged() {
			t.Errorf("LocalSource = %q, want %q", st.LocalSource, LocalSourceUnchanged)
		}
	})

	t.Run("edited source is drift", func(t *testing.T) {
		dir, st := setup(t)
		if err := os.WriteFile(filepath.Join(dir, "app.py"), []byte("v2\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		CompareLocalSource(st, dir, "", testSidecarCID, testSidecarApp)
		if !st.LocalChanged() {
			t.Errorf("LocalSource = %q, want %q", st.LocalSource, LocalSourceChanged)
		}
		r := &DiffReport{Code: st}
		if !r.HasDrift() {
			t.Error("HasDrift() = false for changed local source, want true")
		}
	})

	t.Run("fingerprint for another upload is not compared", func(t *testing.T) {
		dir, st := setup(t)
		st.Recorded = "up-2"
		CompareLocalSource(st, dir, "", testSidecarCID, testSidecarApp)
		if st.LocalSource != LocalSourceNotCompared {
			t.Errorf("LocalSource = %q, want %q", st.LocalSource, LocalSourceNotCompared)
		}
	})

	t.Run("no fingerprint sidecar is not compared", func(t *testing.T) {
		st := &CodeVersionStatus{Recorded: "up-1", RecordedFound: true}
		CompareLocalSource(st, t.TempDir(), "", testSidecarCID, testSidecarApp)
		if st.LocalSource != LocalSourceNotCompared {
			t.Errorf("LocalSource = %q, want %q", st.LocalSource, LocalSourceNotCompared)
		}
	})
}
